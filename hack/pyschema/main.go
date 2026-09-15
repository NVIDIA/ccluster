/*
Copyright 2026 NVIDIA CORPORATION & AFFILIATES.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// pyschema is the first half of `make python-generate`.
//
// It writes JSON Schema documents for everything the Python package under python/ is
// generated from, and the hand-shaped parts of that package which the code generator
// cannot produce:
//
//   - one schema per CRD, lifted from the openAPIV3Schema in config/crd/bases/, into
//     <out>/v1alpha1/<kind>.json;
//   - one schema per catalog component, reflected from that component's Go Values type,
//     into <out>/<chart path>/values.json;
//   - the __init__.py of every generated Python package, which re-exports the generated
//     classes and, for a component, carries the chart path as CHART.
//
// datamodel-code-generator then turns each schema into a Pydantic module next to its
// __init__.py. The Makefile drives that second half, so the tool and its version are
// pinned there and not here.
//
// The charts come from catalogs/components.All, the catalog's own registry; this tool
// keeps no list of its own. The Python tree under --python that it owns (v1alpha1/, one
// catalogs/<catalog>/ per nested catalog and one catalogs/<chart>/ per chart directly
// under catalogs/) is deleted and rebuilt on every run, so a component removed from the
// registry disappears from Python too. Everything else in the package is hand-written.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/invopop/jsonschema"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"

	"github.com/NVIDIA/ccluster/catalogs/components"
)

// modulePath is the Go module, which invopop/jsonschema needs to match the comments
// it parses out of the source tree to the types it reflects.
const modulePath = "github.com/NVIDIA/ccluster"

// catalogsRoot is the directory holding the catalog. Charts directly under it have no
// catalog package of their own in Python, since ccluster.catalogs is hand-written.
const catalogsRoot = "catalogs"

// catalogDirs are the distinct parent directories of the catalog's charts relative to
// the repository root, in registry order: "catalogs", "catalogs/cluster_base",
// "catalogs/deployments", "catalogs/resources". Each maps to the Python package of the same path; "catalogs"
// itself is hand-written, and the charts directly under it are rebuilt one by one.
func catalogDirs() []string {
	var dirs []string
	seen := map[string]bool{}
	for _, c := range components.All {
		dir := path.Dir(c.Chart)
		if !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// generatedDirs are the Python directories this tool owns under python: v1alpha1, one
// per nested catalog, and one per chart that sits directly under catalogs/.
func generatedDirs(python string) []string {
	dirs := []string{filepath.Join(python, "v1alpha1")}
	for _, dir := range catalogDirs() {
		if dir != catalogsRoot {
			dirs = append(dirs, filepath.Join(python, filepath.FromSlash(dir)))
		}
	}
	for _, c := range components.All {
		if path.Dir(c.Chart) == catalogsRoot {
			dirs = append(dirs, filepath.Join(python, filepath.FromSlash(c.Chart)))
		}
	}
	return dirs
}

// crdTypes names the nested schemas of each CRD, which the CRD itself does not do:
// controller-gen inlines every Go type, so the openAPIV3Schema only knows property
// names. Each named node is hoisted into the document's $defs under its Go type name,
// which the code generator uses as the class name; without this it would invent Spec,
// Status, Component and Status1. Paths are JSON paths under the root, with "[]" for an
// array's items. The order is the order the names are re-exported from the package's
// __init__.py.
//
// controller-gen also drops a type's own doc comment in favour of the field's, so the
// class docstring would repeat the field docstring. description restores the type's
// comment where the two differ; left empty, the field's description is used for both.
//
// A path missing here still generates - under the generator's derived name - so a new
// field never breaks the build. It just gets a worse class name until it is added.
var crdTypes = map[string][]crdType{
	"CCLComponentSet": {
		{path: "spec", name: "CCLComponentSetSpec"},
		{path: "spec.components[]", name: "ComponentSpec"},
		{path: "spec.components[].infraPlaneRef", name: "LocalObjectReference",
			description: localObjectReferenceDoc},
		{path: "status", name: "CCLComponentSetStatus"},
		{path: "status.conditions[]", name: "Condition"},
		{path: "status.conditions[].status", name: "ConditionStatus"},
	},
	"CCLInfraPlane": {
		{path: "spec", name: "CCLInfraPlaneSpec"},
		{path: "spec.clusterCredentialsRef", name: "LocalObjectReference",
			description: localObjectReferenceDoc},
		{path: "status", name: "CCLInfraPlaneStatus"},
		{path: "status.conditions[]", name: "Condition"},
		{path: "status.conditions[].status", name: "ConditionStatus"},
	},
}

type crdType struct {
	path, name, description string
}

// localObjectReferenceDoc is the doc comment of api/v1alpha1.LocalObjectReference.
const localObjectReferenceDoc = "LocalObjectReference references an object in the same " +
	"namespace as the referring object. The kind of the target is fixed by the field " +
	"that uses it."

// objectMeta replaces the CRD's `metadata: {type: object}`. It is the one place this
// export adds shape the CRD does not have: the API server validates metadata against
// the built-in ObjectMeta rather than the CRD schema, so controller-gen leaves it
// opaque, and an opaque dict is not useful to a client that has to set a name.
// Only the fields a client writes are here.
var objectMeta = map[string]any{
	"type": "object",
	"description": "ObjectMeta is the metadata a client sets on an object. " +
		"The fields the API server manages (uid, resourceVersion, generation, ...) " +
		"are not modelled.",
	"properties": map[string]any{
		"name": map[string]any{
			"type": "string", "maxLength": 253,
			"description": "name is the object's name. Unique within its namespace.",
		},
		"namespace": map[string]any{
			"type": "string", "maxLength": 63,
			"description": "namespace is the object's namespace.",
		},
		"labels": map[string]any{
			"type": "object", "additionalProperties": map[string]any{"type": "string"},
			"description": "labels are string keys and values that can be used to " +
				"organize and categorize objects.",
		},
		"annotations": map[string]any{
			"type": "object", "additionalProperties": map[string]any{"type": "string"},
			"description": "annotations are arbitrary string keys and values that " +
				"tools can read and set.",
		},
	},
}

func main() {
	out := flag.String("out", "dist/pyschema", "directory to write the JSON schemas into")
	python := flag.String("python", "python/src/ccluster",
		"the Python package whose generated subpackages are rebuilt")
	repo := flag.String("repo", ".", "repository root, holding config/ and catalogs/")
	header := flag.String("header", "hack/boilerplate.py.txt",
		"file header for the generated Python")
	flag.Parse()

	if err := run(*repo, *out, *python, *header); err != nil {
		fmt.Fprintln(os.Stderr, "pyschema:", err)
		os.Exit(1)
	}
}

func run(repo, out, python, headerFile string) error {
	header, err := os.ReadFile(headerFile)
	if err != nil {
		return err
	}

	// Rebuild the generated subpackages from scratch so nothing stale survives.
	for _, dir := range append([]string{out}, generatedDirs(python)...) {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}

	if err := writeCRDs(repo, out, python, header); err != nil {
		return fmt.Errorf("CRDs: %w", err)
	}
	if err := writeComponents(repo, out, python, header); err != nil {
		return fmt.Errorf("catalog: %w", err)
	}
	return nil
}

// writeCRDs lifts the openAPIV3Schema out of every CRD under config/crd/bases and
// writes it as JSON Schema, with its nested types hoisted into $defs under their Go names.
func writeCRDs(repo, out, python string, header []byte) error {
	files, err := filepath.Glob(filepath.Join(repo, "config", "crd", "bases", "*.yaml"))
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return errors.New("no CRDs under config/crd/bases; run `make manifests` first")
	}

	// The re-exports of v1alpha1/__init__.py: kind first, then its nested types, with
	// the types shared between kinds (Condition, LocalObjectReference, ObjectMeta)
	// imported from the first module that defines them.
	var (
		modules  []string
		exports  = map[string][]string{}
		exported = map[string]bool{}
	)
	for _, file := range files {
		var crd apiextensionsv1.CustomResourceDefinition
		raw, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if err := yaml.Unmarshal(raw, &crd); err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
		kind := crd.Spec.Names.Kind
		if len(crd.Spec.Versions) != 1 || crd.Spec.Versions[0].Schema == nil {
			return fmt.Errorf("%s: expected exactly one version with a schema", kind)
		}
		version := crd.Spec.Versions[0]

		schema, err := toGeneric(version.Schema.OpenAPIV3Schema)
		if err != nil {
			return err
		}
		schema["title"] = kind
		props := schema["properties"].(map[string]any)
		props["apiVersion"].(map[string]any)["default"] = crd.Spec.Group + "/" + version.Name
		props["kind"].(map[string]any)["default"] = kind
		// values is x-kubernetes-preserve-unknown-fields with no type. Helm, and Argo
		// CD's valuesObject, only accept a map there, so tell Python that much.
		if values, err := descend(schema, "spec.components[].values"); err == nil {
			values["type"] = "object"
		}

		// Hoist the named types into $defs, leaving a $ref (plus the field's own
		// description, which the generator turns into the field docstring) in place.
		// Deeper paths first: hoisting "spec" moves its children into $defs with it,
		// and a $ref cannot be descended.
		defs := map[string]any{"ObjectMeta": objectMeta}
		props["metadata"] = map[string]any{
			"$ref": "#/$defs/ObjectMeta", "description": "metadata is a standard object metadata",
		}
		types := crdTypes[kind]
		for i := len(types) - 1; i >= 0; i-- {
			t := types[i]
			node, err := descend(schema, t.path)
			if err != nil {
				return fmt.Errorf("%s: %w", kind, err)
			}
			hoisted := maps.Clone(node)
			fieldDescription, _ := node["description"].(string)
			if t.description != "" {
				hoisted["description"] = t.description
			}
			defs[t.name] = hoisted
			clear(node)
			node["$ref"] = "#/$defs/" + t.name
			if fieldDescription != "" {
				node["description"] = fieldDescription
			}
		}
		schema["$defs"] = defs

		titles := []string{kind, "ObjectMeta"}
		for _, t := range types {
			titles = append(titles, t.name)
		}

		module := strings.ToLower(kind)
		if err := writeJSON(filepath.Join(out, "v1alpha1", module+".json"), schema); err != nil {
			return err
		}
		modules = append(modules, module)
		for _, t := range titles {
			if !exported[t] {
				exported[t] = true
				exports[module] = append(exports[module], t)
			}
		}
	}

	var b bytes.Buffer
	b.Write(header)
	b.WriteString("\"\"\"The ccluster.nvidia.com/v1alpha1 API, generated from the CRDs.\"\"\"\n\n")
	for _, module := range modules {
		writeImport(&b, "."+module, exports[module])
	}
	b.WriteString("\n")
	var all []string
	for _, module := range modules {
		all = append(all, exports[module]...)
	}
	writeAll(&b, all)
	return writeFile(filepath.Join(python, "v1alpha1", "__init__.py"), b.Bytes())
}

// writeComponents reflects every component's Values into a schema and writes the
// component's __init__.py alongside where the generator will put values.py, then one
// __init__.py per catalog re-exporting its charts.
func writeComponents(repo, out, python string, header []byte) error {
	reflector := jsonschema.Reflector{
		// No $id: these are documents of ours, not published schemas.
		Anonymous: true,
		// Put Values itself at the root rather than behind a $ref, so the generator
		// makes it a class named by the title instead of wrapping a RootModel around it.
		ExpandedStruct: true,
	}
	// Go doc comments become descriptions, which the generator turns into docstrings.
	// WithFullComment keeps the whole comment rather than its first sentence.
	if err := reflector.AddGoComments(modulePath, filepath.Join(repo, "catalogs"),
		jsonschema.WithFullComment()); err != nil {
		return err
	}

	packages := map[string][]string{} // catalog directory -> chart package names
	for _, c := range components.All {
		schema := reflector.Reflect(c.Values)
		if schema.Type != "object" {
			return fmt.Errorf("%s: Values did not reflect to a struct schema", c.Chart)
		}
		schema.Title = "Values"
		if err := writeJSON(filepath.Join(out, c.Chart, "values.json"), schema); err != nil {
			return err
		}

		// The generator names a class after its $defs key, which is the Go type name,
		// or after the title of an inline enum, which the reflector does not hoist.
		names := []string{"Values"}
		for name := range schema.Definitions {
			names = append(names, name)
		}
		names = append(names, enumTitles(schema)...)
		sort.Strings(names)

		var b bytes.Buffer
		b.Write(header)
		fmt.Fprintf(&b, "\"\"\"Values of the %s chart, generated from its Go package.\"\"\"\n\n", c.Chart)
		b.WriteString("from typing import Final\n\n")
		writeImport(&b, ".values", names)
		b.WriteString("\n")
		writeAll(&b, append([]string{"CHART"}, names...))
		b.WriteString("\n# CHART is the chart's directory, relative to the root of the catalog repository.\n")
		fmt.Fprintf(&b, "CHART: Final = %q\n", c.Chart)
		if err := writeFile(filepath.Join(python, c.Chart, "__init__.py"), b.Bytes()); err != nil {
			return err
		}
		catalog := path.Dir(c.Chart)
		packages[catalog] = append(packages[catalog], path.Base(c.Chart))
	}

	// ccluster.catalogs itself is hand-written; charts directly under it are imported
	// by their own path and need no re-export.
	for _, catalog := range catalogDirs() {
		if catalog == catalogsRoot {
			continue
		}
		var b bytes.Buffer
		b.Write(header)
		fmt.Fprintf(&b, "\"\"\"The %s catalog: one subpackage per chart, each with a Values\n"+
			"model and its CHART path.\"\"\"\n\n", path.Base(catalog))
		fmt.Fprintf(&b, "from . import %s\n\n", strings.Join(packages[catalog], ", "))
		writeAll(&b, packages[catalog])
		if err := writeFile(filepath.Join(python, filepath.FromSlash(catalog), "__init__.py"),
			b.Bytes()); err != nil {
			return err
		}
	}
	return nil
}

// enumTitles returns the titles of the enum-valued properties of schema and of its
// definitions. invopop/jsonschema reflects a named string type inline, so an enum only
// becomes a Python class of its own - and needs re-exporting - through the title its
// field's jsonschema tag gives it.
func enumTitles(schema *jsonschema.Schema) []string {
	var titles []string
	collect := func(s *jsonschema.Schema) {
		if s.Properties == nil {
			return
		}
		for pair := s.Properties.Oldest(); pair != nil; pair = pair.Next() {
			if p := pair.Value; len(p.Enum) > 0 && p.Title != "" {
				titles = append(titles, p.Title)
			}
		}
	}
	collect(schema)
	for _, def := range schema.Definitions {
		collect(def)
	}
	return titles
}

// toGeneric round-trips a JSONSchemaProps through JSON so its nodes can be edited as
// plain maps, which is what the path-based edits above need.
func toGeneric(props *apiextensionsv1.JSONSchemaProps) (map[string]any, error) {
	raw, err := json.Marshal(props)
	if err != nil {
		return nil, err
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, err
	}
	return schema, nil
}

// descend follows a dotted property path from an object schema, stepping into an
// array's items for each "[]" suffix.
func descend(schema map[string]any, p string) (map[string]any, error) {
	node := schema
	for seg := range strings.SplitSeq(p, ".") {
		name, items := strings.CutSuffix(seg, "[]")
		props, _ := node["properties"].(map[string]any)
		next, ok := props[name].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("no property %q on the way to %q", name, p)
		}
		if items {
			if next, ok = next["items"].(map[string]any); !ok {
				return nil, fmt.Errorf("%q is not an array on the way to %q", name, p)
			}
		}
		node = next
	}
	return node, nil
}

// writeImport writes a from-import in the multi-line form black settles on, so the
// hand-shaped files match the generated ones.
func writeImport(b *bytes.Buffer, module string, names []string) {
	fmt.Fprintf(b, "from %s import (\n", module)
	for _, n := range names {
		fmt.Fprintf(b, "    %s,\n", n)
	}
	b.WriteString(")\n")
}

func writeAll(b *bytes.Buffer, names []string) {
	b.WriteString("__all__ = [\n")
	for _, n := range names {
		fmt.Fprintf(b, "    %q,\n", n)
	}
	b.WriteString("]\n")
}

func writeJSON(file string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(file, append(raw, '\n'))
}

func writeFile(file string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	return os.WriteFile(file, data, 0o644)
}
