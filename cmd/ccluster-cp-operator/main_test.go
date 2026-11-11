/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveNamespaces(t *testing.T) {
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

	for _, resolver := range []struct {
		flag    string
		resolve func(flagValue, namespaceFile string) (string, error)
	}{
		{flag: "--argocd-namespace", resolve: resolveArgoCDNamespace},
		{flag: "--dns-endpoint-namespace", resolve: resolveDNSEndpointNamespace},
	} {
		for _, tc := range []struct {
			name      string
			flagValue string
			file      string
			want      string
			wantErr   bool
		}{
			{name: "flag wins over the file", flagValue: "other", file: ownNamespace, want: "other"},
			{name: "flag outside a Pod", flagValue: "other", file: missing, want: "other"},
			{name: "own namespace", file: ownNamespace, want: "ccluster-cp-system"},
			{name: "outside a Pod", file: missing, wantErr: true},
			{name: "empty file", file: empty, wantErr: true},
		} {
			t.Run(resolver.flag+"/"+tc.name, func(t *testing.T) {
				got, err := resolver.resolve(tc.flagValue, tc.file)
				if (err != nil) != tc.wantErr {
					t.Fatalf("error = %v, want error: %v", err, tc.wantErr)
				}
				if got != tc.want {
					t.Errorf("namespace = %q, want %q", got, tc.want)
				}
				if err != nil && !strings.Contains(err.Error(), resolver.flag) {
					t.Errorf("error %q does not name %s", err, resolver.flag)
				}
			})
		}
	}
}

func TestValidateBaseDomain(t *testing.T) {
	for _, tc := range []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "two labels", value: "example.com"},
		{name: "one label", value: "test"},
		{name: "longest", value: strings.Repeat("a.", maxBaseDomainLength/2) + "a"},
		{name: "label too long", value: strings.Repeat("a", 64) + ".com", wantErr: true},
		{name: "empty label", value: "example..com", wantErr: true},
		{name: "empty", value: "", wantErr: true},
		{name: "uppercase", value: "Example.com", wantErr: true},
		{name: "underscore", value: "bad_domain.com", wantErr: true},
		{name: "trailing dot", value: "example.com.", wantErr: true},
		{name: "too long", value: strings.Repeat("a.", maxBaseDomainLength/2) + "ab", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBaseDomain(tc.value)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, want error: %v", err, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "--base-domain") {
				t.Errorf("error %q does not name --base-domain", err)
			}
			// The chart sets the base domain through the environment, so a missing one
			// has to point there too.
			if tc.value == "" && !strings.Contains(err.Error(), baseDomainEnv) {
				t.Errorf("error %q does not name $%s", err, baseDomainEnv)
			}
		})
	}
}
