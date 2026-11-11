/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
			if tc.value == "" && !strings.Contains(err.Error(), "CCLUSTER_BASE_DOMAIN") {
				t.Errorf("error %q does not name $CCLUSTER_BASE_DOMAIN", err)
			}
		})
	}
}

func TestDefaultFlagsFromEnv(t *testing.T) {
	newFlags := func() (*flag.FlagSet, *string, *int, *time.Duration, *string) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		inheritedFlag := fs.String("kubeconfig", "", "")
		revision := fs.String("catalog-revision", "main", "")
		workers := fs.Int("componentset-max-concurrent-reconciles", 4, "")
		resync := fs.Duration("infraplane-resync-period", 5*time.Minute, "")
		return fs, revision, workers, resync, inheritedFlag
	}
	inherited := map[string]bool{"kubeconfig": true}
	env := func(vars map[string]string) func(string) string {
		return func(name string) string { return vars[name] }
	}

	t.Run("environment sets each type", func(t *testing.T) {
		fs, revision, workers, resync, _ := newFlags()
		err := defaultFlagsFromEnv(fs, inherited, env(map[string]string{
			"CCLUSTER_CATALOG_REVISION":                       "v1.2.3",
			"CCLUSTER_COMPONENTSET_MAX_CONCURRENT_RECONCILES": "8",
			"CCLUSTER_INFRAPLANE_RESYNC_PERIOD":               "1m",
		}))
		if err != nil {
			t.Fatal(err)
		}
		if *revision != "v1.2.3" || *workers != 8 || *resync != time.Minute {
			t.Errorf("got %q, %d, %s", *revision, *workers, *resync)
		}
	})

	t.Run("command line wins", func(t *testing.T) {
		fs, revision, _, _, _ := newFlags()
		if err := defaultFlagsFromEnv(fs, inherited,
			env(map[string]string{"CCLUSTER_CATALOG_REVISION": "v1.2.3"})); err != nil {
			t.Fatal(err)
		}
		if err := fs.Parse([]string{"--catalog-revision=feature"}); err != nil {
			t.Fatal(err)
		}
		if *revision != "feature" {
			t.Errorf("revision = %q, want the command line's", *revision)
		}
	})

	t.Run("unset and empty keep the default", func(t *testing.T) {
		fs, revision, _, _, _ := newFlags()
		if err := defaultFlagsFromEnv(fs, inherited,
			env(map[string]string{"CCLUSTER_CATALOG_REVISION": ""})); err != nil {
			t.Fatal(err)
		}
		if *revision != "main" {
			t.Errorf("revision = %q, want the default", *revision)
		}
	})

	t.Run("inherited flags have no variable", func(t *testing.T) {
		fs, _, _, _, kubeconfig := newFlags()
		if err := defaultFlagsFromEnv(fs, inherited,
			env(map[string]string{"CCLUSTER_KUBECONFIG": "/tmp/config"})); err != nil {
			t.Fatal(err)
		}
		if *kubeconfig != "" {
			t.Errorf("kubeconfig = %q, want it untouched", *kubeconfig)
		}
	})

	t.Run("invalid value names the variable", func(t *testing.T) {
		fs, _, _, _, _ := newFlags()
		err := defaultFlagsFromEnv(fs, inherited,
			env(map[string]string{"CCLUSTER_COMPONENTSET_MAX_CONCURRENT_RECONCILES": "many"}))
		if err == nil || !strings.Contains(err.Error(), "CCLUSTER_COMPONENTSET_MAX_CONCURRENT_RECONCILES") {
			t.Errorf("error = %v, want one naming the variable", err)
		}
	})
}
