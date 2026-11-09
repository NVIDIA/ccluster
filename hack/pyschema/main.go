/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// pyschema is the first half of `make python-generate`. It writes what
// datamodel-code-generator turns into the ccluster-py package:
//
//   - for every CRD under --crds and every version it serves, <out>/<version>/<kind>.json:
//     the kind's spec schema at the root and its status schema under definitions. Spec and
//     status are the only parts that differ between kinds, so they are the only parts
//     generated. Their class names are the schemas' titles, which controller-gen writes
//     from +kubebuilder:title markers on the Go types; every name in the Python package
//     therefore comes from a Go declaration, and this tool invents none;
//   - for every chart in catalogs/components.All, <out>/<chart path>.json, reflected from
//     the chart's Go Values type with its doc comments;
//   - for every API version, <python>/<version>/__init__.py: the Kind enum and one concrete
//     CustomResource[<Kind>Spec, <Kind>Status] per kind, specialising the hand-written
//     template in <python>/<version>/base.py. It is a fixed template filled with kind names,
//     not a translation.
//
// Nothing here lists kinds or charts: kinds come from the CRD files, charts from the
// catalog's registry, so adding either needs no change to this tool. The generated Python
// files (<version>/<kind>.py, <version>/__init__.py, the chart packages) are deleted
// before each run so removed kinds and charts disappear from the package.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/invopop/jsonschema"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"

	"github.com/NVIDIA/ccluster/catalogs/components"
)

// modulePath is the Go module, which invopop/jsonschema needs to match the comments it
// parses out of the source tree to the types it reflects.
const modulePath = "github.com/NVIDIA/ccluster"

func main() {
	out := flag.String("out", "dist/pyschema", "directory to write the JSON schemas into")
	python := flag.String("python", "python/src/ccluster",
		"the Python package whose generated files are rebuilt")
	repo := flag.String("repo", ".", "repository root, holding catalogs/")
	crds := flag.String("crds", "config/crd/bases", "directory holding the CRD manifests")
	header := flag.String("header", "hack/boilerplate.py.txt", "file header for generated Python")
	flag.Parse()

	if err := run(*repo, *out, *python, *crds, *header); err != nil {
		fmt.Fprintln(os.Stderr, "pyschema:", err)
		os.Exit(1)
	}
}

func run(repo, out, python, crds, headerFile string) error {
	header, err := os.ReadFile(headerFile)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(out); err != nil {
		return err
	}
	if err := writeCRDs(out, python, crds, string(header)); err != nil {
		return fmt.Errorf("CRDs: %w", err)
	}
	if err := writeCharts(repo, out, python); err != nil {
		return fmt.Errorf("charts: %w", err)
	}
	return nil
}

// writeCRDs writes one schema per kind and version and one __init__.py per version.
func writeCRDs(out, python, dir, header string) error {
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no CRDs under %s; run `make manifests` first", dir)
	}

	group := ""
	kinds := map[string][]apiKind{} // version -> kinds
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		var crd apiextensionsv1.CustomResourceDefinition
		if err := yaml.Unmarshal(raw, &crd); err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
		kind := crd.Spec.Names.Kind
		if group == "" {
			group = crd.Spec.Group
		} else if group != crd.Spec.Group {
			return fmt.Errorf("%s: group %s differs from %s", file, crd.Spec.Group, group)
		}

		for _, version := range crd.Spec.Versions {
			if version.Schema == nil || version.Schema.OpenAPIV3Schema == nil {
				return fmt.Errorf("%s: version %s has no schema", kind, version.Name)
			}
			props := version.Schema.OpenAPIV3Schema.Properties
			spec, ok := props["spec"]
			if !ok {
				return fmt.Errorf("%s %s: no spec", kind, version.Name)
			}
			status, ok := props["status"]
			if !ok {
				return fmt.Errorf("%s %s: no status", kind, version.Name)
			}
			// The class names come from the schemas' titles, not from a naming rule
			// here: a type without a title marker gets a field-derived name from the
			// generator, so refuse the two we import by name.
			for field, title := range map[string]string{"spec": spec.Title, "status": status.Title} {
				if title == "" {
					return fmt.Errorf("%s %s: %s has no title; add +kubebuilder:title to its Go type",
						kind, version.Name, field)
				}
			}
			spec.Definitions = apiextensionsv1.JSONSchemaDefinitions{status.Title: status}

			module := strings.ToLower(kind)
			// Rebuild the kind's module from scratch; base.py in the same directory is
			// hand-written and stays.
			if err := os.Remove(filepath.Join(python, version.Name, module+".py")); err != nil && !os.IsNotExist(err) {
				return err
			}
			if err := writeJSON(filepath.Join(out, version.Name, module+".json"), spec); err != nil {
				return err
			}
			kinds[version.Name] = append(kinds[version.Name], apiKind{
				Kind: kind, Spec: spec.Title, Status: status.Title,
			})
		}
	}

	for version, names := range kinds {
		sort.Slice(names, func(i, j int) bool { return names[i].Kind < names[j].Kind })
		file := filepath.Join(python, version, "__init__.py")
		if err := os.WriteFile(file, []byte(renderInit(header, group, version, names)), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// apiKind is one CRD kind and the class names of its spec and status, as titled in the CRD.
type apiKind struct {
	Kind, Spec, Status string
}

// renderInit is the __init__.py of one API version package: the Kind enum and one
// concrete CustomResource per kind, over the hand-written template in base.py.
func renderInit(header, group, version string, kinds []apiKind) string {
	var b strings.Builder
	b.WriteString(strings.TrimRight(header, "\n"))
	fmt.Fprintf(&b, "\n\n\"\"\"The %s/%s kinds: one concrete CustomResource per CRD, and the Kind\n"+
		"enum naming them. The template is in .base; each kind's Spec and Status are in its\n"+
		"own module.\"\"\"\n\n", group, version)
	b.WriteString("from enum import Enum\nfrom typing import Literal\n\n")
	b.WriteString("from .base import API_VERSION, CustomResource, ObjectMeta\n")
	exports := make([]string, 0, len(kinds)+4)
	exports = append(exports, "API_VERSION")
	for _, k := range kinds {
		fmt.Fprintf(&b, "from .%s import %s, %s\n", strings.ToLower(k.Kind), k.Spec, k.Status)
		exports = append(exports, k.Kind)
	}
	exports = append(exports, "CustomResource", "Kind", "ObjectMeta")
	b.WriteString("\n__all__ = [\n")
	for _, name := range exports {
		fmt.Fprintf(&b, "    %q,\n", name)
	}
	b.WriteString("]\n\n\n")
	b.WriteString("class Kind(str, Enum):\n")
	fmt.Fprintf(&b, "    \"\"\"The kinds of %s/%s, as they appear on the wire.\"\"\"\n\n", group, version)
	for _, k := range kinds {
		fmt.Fprintf(&b, "    %s = %q\n", enumMember(k.Kind), k.Kind)
	}
	for _, k := range kinds {
		fmt.Fprintf(&b, "\n\nclass %s(CustomResource[%s, %s]):\n", k.Kind, k.Spec, k.Status)
		fmt.Fprintf(&b, "    \"\"\"A %s object; see %s and %s for its contents.\"\"\"\n\n", k.Kind, k.Spec, k.Status)
		fmt.Fprintf(&b, "    kind: Literal[Kind.%s] = Kind.%s\n", enumMember(k.Kind), enumMember(k.Kind))
	}
	return b.String()
}

var (
	acronymBoundary = regexp.MustCompile(`([A-Z]+)([A-Z][a-z])`)
	wordBoundary    = regexp.MustCompile(`([a-z0-9])([A-Z])`)
)

// enumMember turns a CamelCase kind into an UPPER_SNAKE enum member:
// CCLComponentSet -> CCL_COMPONENT_SET.
func enumMember(kind string) string {
	s := acronymBoundary.ReplaceAllString(kind, "${1}_${2}")
	s = wordBoundary.ReplaceAllString(s, "${1}_${2}")
	return strings.ToUpper(s)
}

// writeCharts reflects every registered chart's Values into a schema, after deleting the
// Python directories the charts generate into so removed charts disappear.
func writeCharts(repo, out, python string) error {
	reflector := jsonschema.Reflector{
		// No $id: these are documents of ours, not published schemas.
		Anonymous: true,
		// Put Values itself at the root rather than behind a $ref, so the generator
		// makes it a class named by the title instead of a RootModel.
		ExpandedStruct: true,
		// Leave additionalProperties open: a chart accepts more values than its Go type
		// models, and the operator's validator lets unmodelled keys through, so the
		// Python models keep them too.
		AllowAdditionalProperties: true,
	}
	// Go doc comments become descriptions, which the generator turns into docstrings.
	// WithFullComment keeps the whole comment rather than its first sentence.
	if err := reflector.AddGoComments(modulePath, filepath.Join(repo, "catalogs"),
		jsonschema.WithFullComment()); err != nil {
		return err
	}

	// A chart directly under catalogs/ is its own directory; a chart in a catalog shares
	// the catalog directory. catalogs/__init__.py is hand-written and stays.
	for _, c := range components.All {
		dir := c.Chart
		if parent := path.Dir(c.Chart); parent != "catalogs" {
			dir = parent
		}
		if err := os.RemoveAll(filepath.Join(python, filepath.FromSlash(dir))); err != nil {
			return err
		}
	}

	for _, c := range components.All {
		s := reflector.Reflect(c.Values)
		if s.Type != "object" {
			return fmt.Errorf("%s: Values did not reflect to a struct schema", c.Chart)
		}
		s.Title = "Values"
		s.Description = fmt.Sprintf("Values of the %s chart, generated from its Go package.", c.Chart)
		if err := writeJSON(filepath.Join(out, filepath.FromSlash(c.Chart)+".json"), s); err != nil {
			return fmt.Errorf("%s: %w", c.Chart, err)
		}
	}
	return nil
}

func writeJSON(file string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	return os.WriteFile(file, append(raw, '\n'), 0o644)
}
