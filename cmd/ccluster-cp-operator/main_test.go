/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveArgoCDNamespace(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	ownNamespace := write("namespace", "ccluster-cp-system\n")
	empty := write("empty", "")
	missing := filepath.Join(dir, "missing")

	for _, tc := range []struct {
		name      string
		flagValue string
		file      string
		want      string
		wantErr   bool
	}{
		{name: "flag wins over the file", flagValue: "argocd", file: ownNamespace, want: "argocd"},
		{name: "flag outside a Pod", flagValue: "argocd", file: missing, want: "argocd"},
		{name: "own namespace", file: ownNamespace, want: "ccluster-cp-system"},
		{name: "outside a Pod", file: missing, wantErr: true},
		{name: "empty file", file: empty, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveArgoCDNamespace(tc.flagValue, tc.file)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, want error: %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("namespace = %q, want %q", got, tc.want)
			}
		})
	}
}
