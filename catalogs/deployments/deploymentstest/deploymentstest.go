/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package deploymentstest holds the test helpers the deployment charts share.
package deploymentstest

import (
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

// literalEnv matches an env entry whose name the template spells out, such as
// `- name: "JOB_COMPLETIONS"`, followed by value or valueFrom with only comment lines
// between. A name rendered from values, such as `{{ $key | quote }}`, does not match.
var literalEnv = regexp.MustCompile(
	`(?m)^\s*-\s+name:\s*"?([A-Z_][A-Z0-9_]*)"?\s*\n(?:\s*#.*\n)*\s*value(?:From)?:`)

// TemplateEnv returns the variables chartDir's templates set under a literal name,
// sorted and without repeats.
func TemplateEnv(t testing.TB, chartDir string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(chartDir, "templates", "*"))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range literalEnv.FindAllSubmatch(raw, -1) {
			names[string(match[1])] = true
		}
	}
	return slices.Sorted(maps.Keys(names))
}
