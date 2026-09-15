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

// pyschema is the first half of `make python-generate`: it writes the JSON Schema
// documents the Python package under python/ is generated from, and nothing else.
//
//   - one document per API version, <out>/<version>.json, with every kind of that version
//     under $defs, reflected from the Go API types registered in the runtime scheme;
//   - one document per catalog chart, <out>/<chart path>.json, reflected from the chart's
//     Go Values type as registered in catalogs/components.All.
//
// datamodel-code-generator then turns each document into one Python package, written as
// its __init__.py; the Makefile drives that half and appends the per-chart CHART constant.
//
// There is no list of kinds or charts here. Kinds come from v1alpha1.AddToScheme (a new
// CRD registered the kubebuilder way appears by itself, and a new API version becomes a
// new document), charts from the catalog's own registry. The Python directories this tool
// generates into are deleted before every run, so a removed kind or chart disappears from
// Python too.
//
// Reflecting the Go types rather than lifting the CRD's openAPIV3Schema means kubebuilder
// validation markers (MinLength, Pattern, MaxItems, CEL rules) do not reach the Pydantic
// models; the API server enforces them. Doc comments on our own types do reach them.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/invopop/jsonschema"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs/components"
)

// modulePath is the Go module, which invopop/jsonschema needs to match the comments it
// parses out of the source tree to the types it reflects, and which selects our own API
// types out of the scheme (the scheme also registers apimachinery's meta types).
const modulePath = "github.com/NVIDIA/ccluster"

// addToScheme registers every API package this repository ships. This is the one place
// a new API *package* (not a new kind) has to be added, because Go cannot discover
// packages at runtime.
var addToScheme = []func(*runtime.Scheme) error{
	cclusternvidiacomv1alpha1.AddToScheme,
}

// objectMeta replaces metav1.ObjectMeta in the reflected API types. The API server
// validates metadata against the built-in ObjectMeta rather than the CRD schema, so a
// client only ever writes these four fields; the ones the server manages (uid,
// resourceVersion, generation, ...) are not modelled.
type objectMeta struct {
	Name        string            `json:"name,omitempty" jsonschema:"maxLength=253"`
	Namespace   string            `json:"namespace,omitempty" jsonschema:"maxLength=63"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// objectMetaDocs are objectMeta's field descriptions; a package main type has no
// module path for AddGoComments to match, so they are set by hand.
var objectMetaDocs = map[string]string{
	"name":        "name is the object's name. Unique within its namespace.",
	"namespace":   "namespace is the object's namespace.",
	"labels":      "labels are string keys and values that can be used to organize and categorize objects.",
	"annotations": "annotations are arbitrary string keys and values that tools can read and set.",
}

// mapped are the apimachinery types whose Go shape is not their wire shape. Each maps to
// a $ref into the version document, whose definition is supplied by sharedDefs, so the
// generator emits one class per document rather than one per use.
var mapped = map[reflect.Type]string{
	reflect.TypeFor[metav1.ObjectMeta]():      "ObjectMeta",
	reflect.TypeFor[metav1.ConditionStatus](): "ConditionStatus",
}

// sharedDefs are the definitions the mapped types refer to, plus the ones for the types
// that reflect inline.
func sharedDefs(r *jsonschema.Reflector) jsonschema.Definitions {
	meta := r.Reflect(objectMeta{})
	meta.Version, meta.Title = "", "ObjectMeta"
	// Objects read back from the API server carry the fields it manages (uid,
	// resourceVersion, managedFields, ...); they must validate, not fail as extras.
	meta.AdditionalProperties = jsonschema.TrueSchema
	for name, doc := range objectMetaDocs {
		prop, _ := meta.Properties.Get(name)
		prop.Description = doc
	}
	meta.Description = "ObjectMeta is the metadata a client sets on an object. The fields " +
		"the API server manages (uid, resourceVersion, generation, ...) are not modelled."
	return jsonschema.Definitions{
		"ObjectMeta": meta,
		"ConditionStatus": {
			Type:        "string",
			Title:       "ConditionStatus",
			Enum:        []any{"True", "False", "Unknown"},
			Description: "ConditionStatus is the status of a condition: True, False or Unknown.",
		},
	}
}

// mapper implements jsonschema.Reflector.Mapper: $refs for the mapped types, and the
// wire shape of the two whose Go shape is an implementation detail.
func mapper(t reflect.Type) *jsonschema.Schema {
	if name, ok := mapped[t]; ok {
		return &jsonschema.Schema{Ref: "#/$defs/" + name}
	}
	switch t {
	case reflect.TypeFor[metav1.Time]():
		return &jsonschema.Schema{Type: "string", Format: "date-time"}
	case reflect.TypeFor[apiextensionsv1.JSON]():
		// A free-form object: Helm values, or anything else the CRD leaves to
		// x-kubernetes-preserve-unknown-fields. Helm and Argo CD's valuesObject only
		// accept a map there.
		return &jsonschema.Schema{Type: "object", AdditionalProperties: jsonschema.TrueSchema}
	}
	return nil
}

func main() {
	out := flag.String("out", "dist/pyschema", "directory to write the JSON schemas into")
	python := flag.String("python", "python/src/ccluster",
		"the Python package whose generated subpackages are deleted before the schemas are written")
	repo := flag.String("repo", ".", "repository root, holding api/ and catalogs/")
	flag.Parse()

	if err := run(*repo, *out, *python); err != nil {
		fmt.Fprintln(os.Stderr, "pyschema:", err)
		os.Exit(1)
	}
}

func run(repo, out, python string) error {
	reflector := jsonschema.Reflector{
		// No $id: these are documents of ours, not published schemas.
		Anonymous: true,
		// Put the reflected struct at the root rather than behind a $ref, so the
		// generator makes it a class named by the title instead of a RootModel.
		ExpandedStruct: true,
		Mapper:         mapper,
	}
	// Go doc comments become descriptions, which the generator turns into docstrings.
	// WithFullComment keeps the whole comment rather than its first sentence.
	for _, dir := range []string{"api", "catalogs"} {
		if err := reflector.AddGoComments(modulePath, filepath.Join(repo, dir),
			jsonschema.WithFullComment()); err != nil {
			return err
		}
	}
	// controller-gen markers (+required, +kubebuilder:validation:...) are instructions
	// to controller-gen, not documentation; keep them out of the docstrings.
	for key, comment := range reflector.CommentMap {
		reflector.CommentMap[key] = stripMarkers(comment)
	}

	versions, err := apiVersions()
	if err != nil {
		return err
	}

	// Rebuild everything from scratch so nothing stale survives: the schema directory,
	// one Python package per API version, and the catalog directories the charts live
	// in (a chart directly under catalogs/ is its own directory; catalogs/__init__.py is
	// hand-written and stays).
	dirs := []string{out}
	for version := range versions {
		dirs = append(dirs, filepath.Join(python, version))
	}
	for _, c := range components.All {
		dir := c.Chart
		if parent := path.Dir(c.Chart); parent != "catalogs" {
			dir = parent
		}
		dirs = append(dirs, filepath.Join(python, filepath.FromSlash(dir)))
	}
	for _, dir := range dirs {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}

	// One document per version. The generator makes a class of the document root, so
	// the first kind sits at the root and every other kind and nested type in $defs:
	// one definition per Go type for the whole version, however many kinds use it.
	for version, kinds := range versions {
		var doc *jsonschema.Schema
		defs := sharedDefs(&reflector)
		for _, kind := range kinds {
			s := reflector.Reflect(reflect.New(kind.Type).Elem().Interface())
			s.Version, s.Title = "", kind.GVK.Kind
			for key, value := range map[string]string{
				"apiVersion": kind.GVK.GroupVersion().String(), "kind": kind.GVK.Kind,
			} {
				prop, ok := s.Properties.Get(key)
				if !ok {
					return fmt.Errorf("%s has no %s property; is TypeMeta inlined?", kind.GVK.Kind, key)
				}
				prop.Default = value
			}
			maps.Copy(defs, s.Definitions)
			s.Definitions = nil
			if doc == nil {
				doc = s
			} else {
				defs[kind.GVK.Kind] = s
			}
		}
		doc.Definitions = defs
		if err := writeJSON(filepath.Join(out, version+".json"), doc); err != nil {
			return fmt.Errorf("%s: %w", version, err)
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

// apiKind is one CRD kind: its GroupVersionKind and the Go type registered for it.
type apiKind struct {
	GVK  schema.GroupVersionKind
	Type reflect.Type
}

// apiVersions returns this repository's API kinds grouped by version, sorted by kind.
// AddToScheme also registers apimachinery's meta types (Status, WatchEvent, ...) under
// our group version, so kinds are selected by the package they are declared in.
func apiVersions() (map[string][]apiKind, error) {
	scheme := runtime.NewScheme()
	for _, add := range addToScheme {
		if err := add(scheme); err != nil {
			return nil, err
		}
	}
	versions := map[string][]apiKind{}
	for gvk, t := range scheme.AllKnownTypes() {
		if !strings.HasPrefix(t.PkgPath(), modulePath+"/api/") || strings.HasSuffix(gvk.Kind, "List") {
			continue
		}
		versions[gvk.Version] = append(versions[gvk.Version], apiKind{GVK: gvk, Type: t})
	}
	for _, kinds := range versions {
		sort.Slice(kinds, func(i, j int) bool { return kinds[i].GVK.Kind < kinds[j].GVK.Kind })
	}
	return versions, nil
}

// stripMarkers drops the marker lines from a Go doc comment.
func stripMarkers(comment string) string {
	var kept []string
	for line := range strings.SplitSeq(comment, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "+") {
			kept = append(kept, line)
		}
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
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
