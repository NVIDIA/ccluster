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

// pyschema is the catalog half of `make python-generate`: for every chart registered in
// catalogs/components.All it reflects the chart's Go Values type into a JSON Schema
// document, <out>/<chart path>.json, with the Go doc comments as descriptions.
// datamodel-code-generator then turns each document into the chart's Python package; the
// Makefile drives that half and appends the CHART constant.
//
// The CRD models are not produced here. Their spec and status schemas already exist in
// config/crd/bases, written by controller-gen, and the Makefile extracts them with yq and
// feeds them to the same generator. Nothing here reads or shapes them.
//
// There is no list of charts here: the registry is the catalog's own. The Python
// directories the charts generate into are deleted before every run, so a removed chart
// disappears from Python too.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path"
	"path/filepath"

	"github.com/invopop/jsonschema"

	"github.com/NVIDIA/ccluster/catalogs/components"
)

// modulePath is the Go module, which invopop/jsonschema needs to match the comments it
// parses out of the source tree to the types it reflects.
const modulePath = "github.com/NVIDIA/ccluster"

func main() {
	out := flag.String("out", "dist/pyschema", "directory to write the JSON schemas into")
	python := flag.String("python", "python/src/ccluster",
		"the Python package whose generated chart packages are deleted before the schemas are written")
	repo := flag.String("repo", ".", "repository root, holding catalogs/")
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
		// Put Values itself at the root rather than behind a $ref, so the generator
		// makes it a class named by the title instead of a RootModel.
		ExpandedStruct: true,
	}
	// Go doc comments become descriptions, which the generator turns into docstrings.
	// WithFullComment keeps the whole comment rather than its first sentence.
	if err := reflector.AddGoComments(modulePath, filepath.Join(repo, "catalogs"),
		jsonschema.WithFullComment()); err != nil {
		return err
	}

	// Rebuild from scratch so nothing stale survives: the schema directory and the
	// catalog directories the charts live in. A chart directly under catalogs/ is its
	// own directory; catalogs/__init__.py is hand-written and stays.
	dirs := []string{out}
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
