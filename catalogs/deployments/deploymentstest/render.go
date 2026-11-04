/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package deploymentstest

import (
	"bytes"
	"cmp"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/util/yaml"
)

// HelmBinary returns the helm the Makefile runs: $HELM, or helm on PATH. CI must have
// one; elsewhere the test is skipped without it.
func HelmBinary(t *testing.T) string {
	t.Helper()
	name := cmp.Or(os.Getenv("HELM"), "helm")
	path, err := exec.LookPath(name)
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("helm is required in CI: %v", err)
		}
		t.Skipf("helm not found, skipping: %v", err)
	}
	return path
}

// WriteValues writes a values file for one test and returns its path.
func WriteValues(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// RenderChart runs helm template on chart with the values files, later files winning,
// and returns the rendered objects.
func RenderChart(t *testing.T, helm, chart string, valueFiles ...string) []map[string]any {
	t.Helper()
	// go test caches a result by the files the test process reads, and helm reads the
	// chart in its own process. Stat every chart file so an edited template reruns.
	if err := filepath.WalkDir(chart, func(path string, _ fs.DirEntry, err error) error {
		if err == nil {
			_, err = os.Stat(path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	args := append(make([]string, 0, 5+2*len(valueFiles)), "template", "ci", chart, "--namespace", "ccluster-ci")
	for _, file := range valueFiles {
		args = append(args, "--values", file)
	}
	out, err := exec.CommandContext(t.Context(), helm, args...).Output()
	if err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			t.Fatalf("helm template %s: %v\n%s", chart, err, exitErr.Stderr)
		}
		t.Fatalf("helm template %s: %v", chart, err)
	}
	var objects []map[string]any
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(out), 4096)
	for {
		var obj map[string]any
		if err := decoder.Decode(&obj); errors.Is(err, io.EOF) {
			return objects
		} else if err != nil {
			t.Fatalf("decode helm template %s: %v", chart, err)
		}
		if obj != nil {
			objects = append(objects, obj)
		}
	}
}

// Visit calls fn for every map entry at any depth of v.
func Visit(v any, fn func(key string, value any)) {
	switch v := v.(type) {
	case map[string]any:
		for key, child := range v {
			fn(key, child)
			Visit(child, fn)
		}
	case []any:
		for _, child := range v {
			Visit(child, fn)
		}
	}
}
