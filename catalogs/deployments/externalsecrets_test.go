/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package deployments

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"text/template"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// deleteAfterRollout is what the library's ccluster.deleteAfterRollout renders.
var deleteAfterRollout = map[string]any{
	"argocd.argoproj.io/sync-wave":       "-1",
	"argocd.argoproj.io/sync-options":    "Prune=false",
	"argocd.argoproj.io/compare-options": "IgnoreExtraneous",
}

const externalSecretsValues = `
externalSecrets:
  secretStoreRef: {name: vault, kind: ClusterSecretStore}
  env:
    HF_TOKEN: {key: tenants/42/hf-token, version: "3"}
    API_KEY: {key: tenants/42/api, property: key}
  imagePull:
    server: registry.example.com
    username: robot
    password: {key: tenants/42/registry}
`

// TestExternalSecrets checks each workload chart against the library: the env and pull
// ExternalSecrets are named from their references and annotated to outlive the
// rollout, and the pod reads exactly those Secrets.
func TestExternalSecrets(t *testing.T) {
	helm := helmBinary(t)
	values := writeValues(t, externalSecretsValues)
	named := regexp.MustCompile(`-(env|pull)-[0-9a-f]{8}$`)

	for _, chart := range []string{"inference_deployment", "compute_deployment", "job_deployment"} {
		t.Run(chart, func(t *testing.T) {
			objects := renderChart(t, helm, chart, filepath.Join(chart, "ci", "default-values.yaml"), values)
			secrets := map[string]map[string]any{}
			var pod map[string]any
			for _, obj := range objects {
				switch obj["kind"] {
				case "ExternalSecret":
					match := named.FindStringSubmatch(nestedString(obj, "metadata", "name"))
					if match == nil {
						t.Fatalf("ExternalSecret %q is not named <name>-(env|pull)-<hash>", nestedString(obj, "metadata", "name"))
					}
					secrets[match[1]] = obj
				case "Rollout", "Deployment", "Job":
					pod, _, _ = unstructured.NestedMap(obj, "spec", "template", "spec")
				}
			}
			if len(secrets) != 2 || pod == nil {
				t.Fatalf("rendered ExternalSecrets %v and a pod spec %v, want env and pull and a pod",
					slices.Sorted(maps.Keys(secrets)), pod != nil)
			}
			for block, obj := range secrets {
				annotations, _, _ := unstructured.NestedMap(obj, "metadata", "annotations")
				if !maps.Equal(annotations, deleteAfterRollout) {
					t.Errorf("%s ExternalSecret annotations = %v, want %v", block, annotations, deleteAfterRollout)
				}
				if target := nestedString(obj, "spec", "target", "name"); target != nestedString(obj, "metadata", "name") {
					t.Errorf("%s ExternalSecret creates Secret %q, not one of its own name", block, target)
				}
			}

			envSecret := nestedString(secrets["env"], "metadata", "name")
			read := map[string]string{}
			containers, _, _ := unstructured.NestedSlice(pod, "containers")
			for _, c := range containers {
				env, _, _ := unstructured.NestedSlice(c.(map[string]any), "env")
				for _, e := range env {
					e := e.(map[string]any)
					if name := nestedString(e, "valueFrom", "secretKeyRef", "name"); name == envSecret {
						read[e["name"].(string)] = nestedString(e, "valueFrom", "secretKeyRef", "key")
					}
				}
			}
			if want := map[string]string{"HF_TOKEN": "HF_TOKEN", "API_KEY": "API_KEY"}; !maps.Equal(read, want) {
				t.Errorf("container reads %v from %s, want %v", read, envSecret, want)
			}
			pullSecret := nestedString(secrets["pull"], "metadata", "name")
			pulls, _, _ := unstructured.NestedSlice(pod, "imagePullSecrets")
			if !slices.ContainsFunc(pulls, func(p any) bool { return p.(map[string]any)["name"] == pullSecret }) {
				t.Errorf("imagePullSecrets = %v, want %s", pulls, pullSecret)
			}
		})
	}
}

// TestExternalSecretNames checks what changes a Secret's name: its own references and
// the store, not the order the caller wrote them in or the other Secret's references.
func TestExternalSecretNames(t *testing.T) {
	helm := helmBinary(t)
	chart := "inference_deployment"
	names := func(t *testing.T, values string) (env, pull string) {
		t.Helper()
		for _, obj := range renderChart(t, helm, chart, filepath.Join(chart, "ci", "default-values.yaml"),
			writeValues(t, values)) {
			if obj["kind"] != "ExternalSecret" {
				continue
			}
			if name := nestedString(obj, "metadata", "name"); strings.Contains(name, "-env-") {
				env = name
			} else {
				pull = name
			}
		}
		return env, pull
	}
	env, pull := names(t, externalSecretsValues)

	for _, tt := range []struct {
		name              string
		values            string
		envSame, pullSame bool
	}{
		{
			name: "references in another order",
			values: `
externalSecrets:
  imagePull:
    password: {key: tenants/42/registry}
    username: robot
    server: registry.example.com
  env:
    API_KEY: {property: key, key: tenants/42/api}
    HF_TOKEN: {version: "3", key: tenants/42/hf-token}
  secretStoreRef: {kind: ClusterSecretStore, name: vault}
`,
			envSame: true, pullSame: true,
		},
		{
			name: "new env version",
			values: `
externalSecrets:
  secretStoreRef: {name: vault, kind: ClusterSecretStore}
  env:
    HF_TOKEN: {key: tenants/42/hf-token, version: "4"}
    API_KEY: {key: tenants/42/api, property: key}
  imagePull:
    server: registry.example.com
    username: robot
    password: {key: tenants/42/registry}
`,
			envSame: false, pullSame: true,
		},
		{
			name: "another store",
			values: `
externalSecrets:
  secretStoreRef: {name: vault-2, kind: ClusterSecretStore}
  env:
    HF_TOKEN: {key: tenants/42/hf-token, version: "3"}
    API_KEY: {key: tenants/42/api, property: key}
  imagePull:
    server: registry.example.com
    username: robot
    password: {key: tenants/42/registry}
`,
			envSame: false, pullSame: false,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gotEnv, gotPull := names(t, tt.values)
			if (gotEnv == env) != tt.envSame || (gotPull == pull) != tt.pullSame {
				t.Errorf("names %s, %s from %s, %s: want env same %v, pull same %v",
					gotEnv, gotPull, env, pull, tt.envSame, tt.pullSame)
			}
		})
	}
}

// TestPerRevisionObjectsOutliveRollout checks that the objects named per revision carry
// the library's annotations, so the old one stays until the rollout ends.
func TestPerRevisionObjectsOutliveRollout(t *testing.T) {
	helm := helmBinary(t)
	for _, tt := range []struct{ chart, kind, values string }{
		{"inference_deployment", "ConfigMap", `
configFile: {filename: config.yaml, mountPath: /etc/app, content: "a: 1\n"}
`},
		{"dynamo_deployment", "ResourceClaimTemplate", `
servingMode: disaggregated
workerPool: null
prefillPool: &pool
  tensorParallelSize: 1
  resources:
    limits: {cpu: "1", memory: 1Gi, acceleratorCount: 1}
    requests: {cpu: "1", memory: 1Gi, acceleratorCount: 1}
decodePool: *pool
kvTransfer: {backend: LIBFABRIC, efa: {}}
`},
	} {
		t.Run(tt.chart, func(t *testing.T) {
			files := []string{filepath.Join(tt.chart, "ci", "default-values.yaml")}
			if tt.values != "" {
				files = append(files, writeValues(t, tt.values))
			}
			for _, obj := range renderChart(t, helm, tt.chart, files...) {
				if obj["kind"] != tt.kind {
					continue
				}
				annotations, _, _ := unstructured.NestedMap(obj, "metadata", "annotations")
				if !maps.Equal(annotations, deleteAfterRollout) {
					t.Fatalf("%s %s annotations = %v, want %v",
						tt.kind, nestedString(obj, "metadata", "name"), annotations, deleteAfterRollout)
				}
				return
			}
			t.Fatalf("no %s rendered", tt.kind)
		})
	}
}

// TestDockerConfig runs the template ESO runs for the pull Secret, with the functions it
// uses, and checks the dockerconfigjson it builds. The username carries template and
// JSON metacharacters, which the chart must pass through as a literal.
func TestDockerConfig(t *testing.T) {
	const (
		server   = "registry.example.com:5000"
		username = `robot$"{{ ci }}"\`
		password = `p@ss"word`
	)
	helm := helmBinary(t)
	quoted, err := json.Marshal(username)
	if err != nil {
		t.Fatal(err)
	}
	values := writeValues(t, `
externalSecrets:
  secretStoreRef: {name: vault, kind: SecretStore}
  imagePull:
    server: `+server+`
    username: `+string(quoted)+`
    password: {key: tenants/42/registry}
`)
	chart := "inference_deployment"
	var text string
	for _, obj := range renderChart(t, helm, chart, filepath.Join(chart, "ci", "default-values.yaml"), values) {
		if obj["kind"] == "ExternalSecret" {
			text = nestedString(obj, "spec", "target", "template", "data", ".dockerconfigjson")
		}
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

// nestedString returns the string at fields in obj, or "".
func nestedString(obj map[string]any, fields ...string) string {
	s, _, _ := unstructured.NestedString(obj, fields...)
	return s
}
