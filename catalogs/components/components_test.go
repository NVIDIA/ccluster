/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package components

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/NVIDIA/ccluster/catalogs"
)

// TestAllCoversTheTree is the check that keeps All and the catalogs/ directory in step:
// every catalogs/<catalog>/<chart> or catalogs/<chart> holding a Chart.yaml and a
// component.go must be listed, under the path it actually lives at, and nothing may be
// listed that is not there. It is what turns a forgotten registration into a failing test rather than a
// chart silently missing from Python.
func TestAllCoversTheTree(t *testing.T) {
	root := filepath.Join("..", "..")
	dirs := make([]string, 0, len(All))
	for _, pattern := range []string{
		filepath.Join(root, "catalogs", "*", "Chart.yaml"),
		filepath.Join(root, "catalogs", "*", "*", "Chart.yaml"),
	} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, matches...)
	}

	var onDisk []string
	for _, chartYAML := range dirs {
		dir := filepath.Dir(chartYAML)
		if _, err := os.Stat(filepath.Join(dir, "component.go")); err != nil {
			continue // a chart without a Go package is not a catalog component
		}
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			t.Fatal(err)
		}
		onDisk = append(onDisk, filepath.ToSlash(rel))
	}
	slices.Sort(onDisk)

	listed := make([]string, 0, len(All))
	for _, c := range All {
		listed = append(listed, c.Chart)
	}
	slices.Sort(listed)

	if !slices.Equal(onDisk, listed) {
		t.Fatalf("All and catalogs/ disagree\n on disk: %s\n listed:  %s",
			strings.Join(onDisk, " "), strings.Join(listed, " "))
	}
}

// TestCIValuesAreValid keeps each chart's ci/*-values.yaml, which make
// helm-lint-catalogs renders the chart with, in step with its Values type: every file
// decodes with no unknown key and passes Validate. Helm ignores a misspelled key, and
// helm template renders a chart whose required values are missing without complaint.
// The top-level global key is Helm's values namespace shared with subcharts, which no
// Values type models.
func TestCIValuesAreValid(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, c := range All {
		files, err := filepath.Glob(filepath.Join(root, c.Chart, "ci", "*-values.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			t.Run(c.Chart+"/"+filepath.Base(file), func(t *testing.T) {
				raw, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				doc, err := yaml.YAMLToJSONStrict(raw)
				if err != nil {
					t.Fatal(err)
				}
				var top map[string]json.RawMessage
				if err := json.Unmarshal(doc, &top); err != nil {
					t.Fatal(err)
				}
				delete(top, "global")
				if doc, err = json.Marshal(top); err != nil {
					t.Fatal(err)
				}

				values := reflect.New(reflect.TypeOf(c.Values))
				dec := json.NewDecoder(bytes.NewReader(doc))
				dec.DisallowUnknownFields()
				if err := dec.Decode(values.Interface()); err != nil {
					t.Fatalf("does not decode into %T: %v", c.Values, err)
				}
				if v, ok := reflect.TypeAssert[catalogs.Validator](values.Elem()); ok {
					if err := v.Validate(); err != nil {
						t.Errorf("Validate: %v", err)
					}
				}
			})
		}
	}
}

// TestAllValuesAreStructs: the Python generator reflects each Values into an object
// schema, and a package that exported something else would only fail there.
func TestAllValuesAreStructs(t *testing.T) {
	for _, c := range All {
		if reflect.TypeOf(c.Values).Kind() != reflect.Struct {
			t.Errorf("%s: Values is %T, want a struct", c.Chart, c.Values)
		}
	}
}

// TestGroupsMatchTheirDirectories: each group holds exactly the charts of one catalogs/
// directory, so the groups are disjoint and a chart registered in the wrong one fails
// here. TestAllCoversTheTree checks that together they cover the tree.
func TestGroupsMatchTheirDirectories(t *testing.T) {
	for _, group := range []struct {
		name   string
		charts []Component
		inDir  func(chart string) bool
	}{
		{"NamespaceBase", NamespaceBase, func(c string) bool { return c == "catalogs/namespace_base" }},
		{"ClusterBase", ClusterBase, func(c string) bool { return strings.HasPrefix(c, "catalogs/cluster_base/") }},
		{"Deployments", Deployments, func(c string) bool { return strings.HasPrefix(c, "catalogs/deployments/") }},
		{"Resources", Resources, func(c string) bool { return strings.HasPrefix(c, "catalogs/resources/") }},
	} {
		for _, c := range group.charts {
			if !group.inDir(c.Chart) {
				t.Errorf("%s: %s does not belong in this group", group.name, c.Chart)
			}
		}
	}
}
