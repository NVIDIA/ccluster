/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package components

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
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

// TestAllValuesAreStructs: the Python generator reflects each Values into an object
// schema, and a package that exported something else would only fail there.
func TestAllValuesAreStructs(t *testing.T) {
	for _, c := range All {
		if reflect.TypeOf(c.Values).Kind() != reflect.Struct {
			t.Errorf("%s: Values is %T, want a struct", c.Chart, c.Values)
		}
	}
}
