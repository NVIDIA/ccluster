/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package deployments

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"k8s.io/apimachinery/pkg/util/yaml"
)

// helmBinary returns the helm the Makefile runs: $HELM, or helm on PATH. CI must have
// one; elsewhere the test is skipped without it.
func helmBinary(t *testing.T) string {
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

// writeValues writes a values file for one test and returns its path.
func writeValues(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// renderChart runs helm template on chart with the values files, later files winning,
// and returns the rendered objects.
func renderChart(t *testing.T, helm, chart string, valueFiles ...string) []map[string]any {
	t.Helper()
	out, err := helmTemplate(t, helm, chart, valueFiles...)
	if err != nil {
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

// helmTemplate runs helm template on chart with the values files, later files winning,
// and returns its output. A failed render's error carries helm's message.
func helmTemplate(t *testing.T, helm, chart string, valueFiles ...string) ([]byte, error) {
	t.Helper()
	args := append(make([]string, 0, 5+2*len(valueFiles)),
		"template", "ci", filepath.Join(builtCharts(t, helm), chart), "--namespace", "ccluster-ci")
	for _, file := range valueFiles {
		args = append(args, "--values", file)
	}
	out, err := exec.CommandContext(t.Context(), helm, args...).Output()
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return nil, fmt.Errorf("%w\n%s", err, exitErr.Stderr)
	}
	return out, err
}

// The copy of this directory the charts render from: made by the first builtCharts call,
// shared by every test in the binary, and removed by TestMain.
var (
	builtDir     string
	builtErr     error
	builtDirOnce sync.Once
)

func TestMain(m *testing.M) {
	code := m.Run()
	if builtDir != "" {
		_ = os.RemoveAll(builtDir)
	}
	os.Exit(code)
}

// builtCharts returns a copy of this directory in which helm dependency build has run
// for every chart, so that a chart with a file:// dependency, such as the edge library,
// renders, and the source tree gets no charts/ or Chart.lock. Copying opens every file,
// which go test's cache records: helm reads the charts in its own process, and an edited
// template must rerun the tests.
func builtCharts(t *testing.T, helm string) string {
	t.Helper()
	builtDirOnce.Do(func() { builtDir, builtErr = copyAndBuildCharts(t.Context(), helm) })
	if builtErr != nil {
		t.Fatal(builtErr)
	}
	return builtDir
}

// copyAndBuildCharts copies this directory into a new temporary one and builds the
// dependencies of every chart in it. It returns the directory even on error, for
// TestMain to remove.
func copyAndBuildCharts(ctx context.Context, helm string) (string, error) {
	dir, err := os.MkdirTemp("", "catalogs-deployments-")
	if err != nil {
		return "", err
	}
	if err := os.CopyFS(dir, os.DirFS(".")); err != nil {
		return dir, err
	}
	charts, err := filepath.Glob(filepath.Join(dir, "*", "Chart.yaml"))
	if err != nil {
		return dir, err
	}
	for _, chartYAML := range charts {
		chart := filepath.Dir(chartYAML)
		// A dependency build run in the source tree leaves these behind.
		for _, leftover := range []string{"charts", "Chart.lock"} {
			if err := os.RemoveAll(filepath.Join(chart, leftover)); err != nil {
				return dir, err
			}
		}
		build := exec.CommandContext(ctx, helm, "dependency", "build", "--skip-refresh", chart)
		if out, err := build.CombinedOutput(); err != nil {
			return dir, fmt.Errorf("helm dependency build %s: %w\n%s", filepath.Base(chart), err, out)
		}
	}
	return dir, nil
}

// visit calls fn for every map entry at any depth of v.
func visit(v any, fn func(key string, value any)) {
	switch v := v.(type) {
	case map[string]any:
		for key, child := range v {
			fn(key, child)
			visit(child, fn)
		}
	case []any:
		for _, child := range v {
			visit(child, fn)
		}
	}
}
