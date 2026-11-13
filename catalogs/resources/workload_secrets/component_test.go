/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package workloadsecrets

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"text/template"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/NVIDIA/ccluster/catalogs/deployments/deploymentstest"
)

// validValues sets every required value, and both blocks.
func validValues() Values {
	return Values{
		Name:           "app",
		Suffix:         "8f3a21c0",
		SecretStoreRef: SecretStoreRef{Name: "vault", Kind: ClusterSecretStore},
		Env:            map[string]RemoteRef{"HF_TOKEN": {Key: "tenants/42/hf-token", Version: "3"}},
		ImagePull: &ExternalImagePull{
			Server:   "nvcr.io",
			Username: "$oauthtoken",
			Password: RemoteRef{Key: "tenants/42/ngc-key"},
		},
	}
}

func TestValidate(t *testing.T) {
	for _, tt := range []struct {
		name    string
		edit    func(*Values)
		wantErr string
	}{
		{name: "valid", edit: func(*Values) {}},
		{name: "store only", edit: func(v *Values) { v.Env, v.ImagePull = nil, nil }},
		{name: "longest suffix", edit: func(v *Values) { v.Suffix = strings.Repeat("a", MaxSuffixLength) }},
		{name: "no name", edit: func(v *Values) { v.Name = "" }, wantErr: "name"},
		{name: "no suffix", edit: func(v *Values) { v.Suffix = "" }, wantErr: "suffix"},
		{
			name:    "no store",
			edit:    func(v *Values) { v.SecretStoreRef = SecretStoreRef{} },
			wantErr: "secretStoreRef.name, secretStoreRef.kind",
		},
		{
			name:    "suffix too long",
			edit:    func(v *Values) { v.Suffix = strings.Repeat("a", MaxSuffixLength+1) },
			wantErr: "is not a DNS label",
		},
		{name: "suffix upper case", edit: func(v *Values) { v.Suffix = "ABC" }, wantErr: "is not a DNS label"},
		{name: "suffix ends in a dash", edit: func(v *Values) { v.Suffix = "abc-" }, wantErr: "is not a DNS label"},
		{
			name:    "unknown store kind",
			edit:    func(v *Values) { v.SecretStoreRef.Kind = "Vault" },
			wantErr: `"Vault" is not SecretStore`,
		},
		{name: "env without key", edit: func(v *Values) { v.Env["HF_TOKEN"] = RemoteRef{} }, wantErr: "env.HF_TOKEN.key"},
		{
			name:    "env name not a Secret key",
			edit:    func(v *Values) { v.Env["HF TOKEN"] = RemoteRef{Key: "k"} },
			wantErr: "env.HF TOKEN is not a valid Secret key",
		},
		{
			name:    "imagePull incomplete",
			edit:    func(v *Values) { v.ImagePull = &ExternalImagePull{} },
			wantErr: "imagePull.server, imagePull.username, imagePull.password.key",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			v := validValues()
			tt.edit(&v)
			err := v.Validate()
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("Validate() = %v, want nil", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("Validate() = %v, want an error containing %q", err, tt.wantErr)
			}
		})
	}
}

// TestRender checks the ExternalSecrets the chart renders: the names the workload reads,
// a Secret ESO fills once under that name and that goes with its ExternalSecret, and no
// Argo CD annotation. An explicit empty optional field renders as an unset one.
func TestRender(t *testing.T) {
	helm := deploymentstest.HelmBinary(t)
	objects := deploymentstest.RenderChart(t, helm, ".", "ci/env-and-pull-values.yaml",
		deploymentstest.WriteValues(t, "env: {HF_TOKEN: {property: \"\"}}\n"))

	externalSecrets := map[string]map[string]any{}
	for _, obj := range objects {
		if obj["kind"] != "ExternalSecret" {
			t.Errorf("rendered a %v, want only ExternalSecrets", obj["kind"])
		}
		name, _, _ := unstructured.NestedString(obj, "metadata", "name")
		externalSecrets[name] = obj
	}
	got, want := slices.Sorted(maps.Keys(externalSecrets)), []string{"app-env-8f3a21c0", "app-pull-8f3a21c0"}
	if !slices.Equal(got, want) {
		t.Fatalf("ExternalSecrets = %v, want %v", got, want)
	}
	for name, es := range externalSecrets {
		if annotations, found, _ := unstructured.NestedStringMap(es, "metadata", "annotations"); found {
			t.Errorf("%s annotations = %v, want none", name, annotations)
		}
		for _, field := range []struct {
			path []string
			want string
		}{
			{[]string{"spec", "refreshPolicy"}, "CreatedOnce"},
			{[]string{"spec", "secretStoreRef", "name"}, "vault"},
			{[]string{"spec", "secretStoreRef", "kind"}, "ClusterSecretStore"},
			{[]string{"spec", "target", "name"}, name},
			{[]string{"spec", "target", "creationPolicy"}, "Owner"},
		} {
			if got, _, _ := unstructured.NestedString(es, field.path...); got != field.want {
				t.Errorf("%s %s = %q, want %q", name, strings.Join(field.path, "."), got, field.want)
			}
		}
		if immutable, _, _ := unstructured.NestedBool(es, "spec", "target", "immutable"); !immutable {
			t.Errorf("%s spec.target.immutable is not true", name)
		}
		// Without a template ESO copies the ExternalSecret's labels and annotations onto
		// the Secret.
		if _, found, _ := unstructured.NestedMap(es, "spec", "target", "template", "metadata"); !found {
			t.Errorf("%s has no spec.target.template.metadata", name)
		}
	}

	data, _, _ := unstructured.NestedSlice(externalSecrets["app-env-8f3a21c0"], "spec", "data")
	wantData := []any{map[string]any{
		"secretKey": "HF_TOKEN",
		"remoteRef": map[string]any{"key": "tenants/42/hf-token", "version": "3"},
	}}
	if !reflect.DeepEqual(data, wantData) {
		t.Errorf("env data = %v, want %v", data, wantData)
	}
}

// TestRenderStoreOnly checks that values without env or imagePull render nothing.
func TestRenderStoreOnly(t *testing.T) {
	helm := deploymentstest.HelmBinary(t)
	if objects := deploymentstest.RenderChart(t, helm, ".", "ci/required-values.yaml"); len(objects) != 0 {
		t.Fatalf("rendered %d objects, want none", len(objects))
	}
}

// TestDockerConfig runs the template the pull ExternalSecret hands ESO, with stand-ins
// for the sprig functions ESO provides, and checks it builds the dockerconfigjson the
// kubelet reads. The username carries characters that would break the template, or the
// JSON, if the chart did not quote them.
func TestDockerConfig(t *testing.T) {
	const (
		server   = "registry.example.com:5000"
		username = `robot$"{{ ci }}"\`
		password = `p@ss"word`
	)
	helm := deploymentstest.HelmBinary(t)
	quoted, err := json.Marshal(username)
	if err != nil {
		t.Fatal(err)
	}
	values := deploymentstest.WriteValues(t, `
name: app
suffix: 8f3a21c0
secretStoreRef: {name: vault, kind: SecretStore}
imagePull:
  server: `+server+`
  username: `+string(quoted)+`
  password: {key: tenants/42/registry}
`)
	var text string
	for _, obj := range deploymentstest.RenderChart(t, helm, ".", values) {
		text, _, _ = unstructured.NestedString(obj, "spec", "target", "template", "data", ".dockerconfigjson")
	}
	tmpl, err := template.New("dockerconfigjson").Funcs(template.FuncMap{
		"dict": func(pairs ...any) map[string]any {
			m := map[string]any{}
			for i := 0; i+1 < len(pairs); i += 2 {
				m[pairs[i].(string)] = pairs[i+1]
			}
			return m
		},
		"b64enc": func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) },
		"toJson": func(v any) (string, error) {
			out, err := json.Marshal(v)
			return string(out), err
		},
	}).Parse(text)
	if err != nil {
		t.Fatalf("parse %q: %v", text, err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, map[string]string{"password": password}); err != nil {
		t.Fatalf("execute %q: %v", text, err)
	}
	var config struct {
		Auths map[string]struct {
			Username, Password, Auth string
		}
	}
	if err := json.Unmarshal(out.Bytes(), &config); err != nil {
		t.Fatalf("dockerconfigjson %q: %v", out.String(), err)
	}
	got := config.Auths[server]
	if got.Username != username || got.Password != password ||
		got.Auth != base64.StdEncoding.EncodeToString([]byte(username+":"+password)) {
		t.Fatalf("auths[%s] = %+v, want username %q and password %q", server, got, username, password)
	}
}
