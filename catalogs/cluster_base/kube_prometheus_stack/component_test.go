/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package kubeprometheusstack

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
)

// prometheusAt returns a ready Prometheus Service and endpoint at namespace/name.
func prometheusAt(namespace, name string) []client.Object {
	return []client.Object{
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}},
		&discoveryv1.EndpointSlice{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      name + "-abcde",
				Labels:    map[string]string{discoveryv1.LabelServiceName: name},
			},
			Endpoints: []discoveryv1.Endpoint{{
				Addresses:  []string{"10.0.0.1"},
				Conditions: discoveryv1.EndpointConditions{Ready: new(true)},
			}},
		},
	}
}

// TestVerify pins the address the catalog's consumers hardcode: a ready Prometheus
// anywhere else, under another name or namespace, is not one they can reach.
func TestVerify(t *testing.T) {
	tests := []struct {
		name    string
		objects []client.Object
		want    string // "" means "expect nil"
	}{
		{
			name:    "at the expected address",
			objects: prometheusAt("prometheus", "prometheus-op-kube-prometh-prometheus"),
		},
		{
			name: "not installed",
			want: "no Service prometheus/prometheus-op-kube-prometh-prometheus found",
		},
		{
			name:    "another namespace",
			objects: prometheusAt("monitoring", "prometheus-op-kube-prometh-prometheus"),
			want:    "no Service prometheus/prometheus-op-kube-prometh-prometheus found",
		},
		{
			// kube-prometheus-stack's default naming, without the chart's fullnameOverride.
			name:    "another name",
			objects: prometheusAt("prometheus", "kube-prometheus-stack-prometheus"),
			want:    "no Service prometheus/prometheus-op-kube-prometh-prometheus found",
		},
		{
			name: "no ready endpoint",
			objects: []client.Object{&corev1.Service{ObjectMeta: metav1.ObjectMeta{
				Namespace: "prometheus", Name: "prometheus-op-kube-prometh-prometheus"}}},
			want: "no ready endpoints for Service prometheus/prometheus-op-kube-prometh-prometheus",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := discoveryv1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			r := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.objects...).Build()
			err := Verify(context.Background(), r, cclusternvidiacomv1alpha1.CCLClusterConfigSpec{})
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("want nil, got %v", err)
			case tt.want != "" && (err == nil || err.Error() != tt.want):
				t.Fatalf("want %q, got %v", tt.want, err)
			}
		})
	}
}

type alertmanagerRoute struct {
	Receiver string              `json:"receiver"`
	Routes   []alertmanagerRoute `json:"routes"`
}

type alertmanagerReceiver struct {
	Name string `json:"name"`
}

// checkAlertmanagerValues merges raw (a values document for this chart) over the
// upstream kube-prometheus-stack 73.1.0 Alertmanager defaults using Helm's rules
// (maps merge, non-nil slices replace) and checks that every receiver named in the
// resulting route tree is defined in receivers.
func checkAlertmanagerValues(raw []byte) error {
	var doc struct {
		KubePrometheusStack struct {
			Alertmanager struct {
				Enabled *bool `json:"enabled"`
				Config  struct {
					Route struct {
						Receiver *string              `json:"receiver"`
						Routes   *[]alertmanagerRoute `json:"routes"`
					} `json:"route"`
					Receivers *[]alertmanagerReceiver `json:"receivers"`
				} `json:"config"`
			} `json:"alertmanager"`
		} `json:"kube-prometheus-stack"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return err
	}
	am := doc.KubePrometheusStack.Alertmanager
	if am.Enabled != nil && !*am.Enabled {
		return nil
	}

	// Upstream kube-prometheus-stack defaults: route.receiver is "null",
	// route.routes sends Watchdog to "null", and receivers defines "null".
	route := alertmanagerRoute{
		Receiver: "null",
		Routes:   []alertmanagerRoute{{Receiver: "null"}},
	}
	if am.Config.Route.Receiver != nil {
		route.Receiver = *am.Config.Route.Receiver
	}
	if am.Config.Route.Routes != nil {
		route.Routes = *am.Config.Route.Routes
	}
	receivers := []alertmanagerReceiver{{Name: "null"}}
	if am.Config.Receivers != nil {
		receivers = *am.Config.Receivers
	}

	if len(receivers) == 0 {
		if route.Receiver != "" {
			return fmt.Errorf("undefined receiver %q used in route", route.Receiver)
		}
		return errors.New("alertmanager.config.receivers is empty")
	}
	defined := make([]string, 0, len(receivers))
	for _, r := range receivers {
		if r.Name == "" {
			return errors.New("alertmanager.config.receivers has an unnamed receiver")
		}
		defined = append(defined, r.Name)
	}
	if route.Receiver == "" {
		return errors.New("alertmanager.config.route.receiver is empty")
	}
	var checkRoute func(r alertmanagerRoute) error
	checkRoute = func(r alertmanagerRoute) error {
		if r.Receiver != "" && !slices.Contains(defined, r.Receiver) {
			return fmt.Errorf("undefined receiver %q used in route", r.Receiver)
		}
		for _, child := range r.Routes {
			if err := checkRoute(child); err != nil {
				return err
			}
		}
		return nil
	}
	return checkRoute(route)
}

// TestValuesAlertmanagerConfig checks that values.yaml leaves Alertmanager with a
// reconcilable configuration once Helm merges it with kube-prometheus-stack's defaults.
func TestValuesAlertmanagerConfig(t *testing.T) {
	raw, err := os.ReadFile("values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := checkAlertmanagerValues(raw); err != nil {
		t.Fatalf("values.yaml produces an invalid Alertmanager config: %v", err)
	}

	tests := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "empty route and receivers leaves upstream null receiver undefined",
			yaml: `
kube-prometheus-stack:
  alertmanager:
    config:
      route: {}
      receivers: []
`,
			want: `undefined receiver "null" used in route`,
		},
		{
			name: "route receiver missing from receivers",
			yaml: `
kube-prometheus-stack:
  alertmanager:
    config:
      route:
        receiver: sns
        routes: []
      receivers:
        - name: 'null'
`,
			want: `undefined receiver "sns" used in route`,
		},
		{
			name: "upstream Watchdog subroute still needs null receiver unless routes is replaced",
			yaml: `
kube-prometheus-stack:
  alertmanager:
    config:
      route:
        receiver: sns
      receivers:
        - name: sns
`,
			want: `undefined receiver "null" used in route`,
		},
		{
			name: "custom receiver with routes cleared",
			yaml: `
kube-prometheus-stack:
  alertmanager:
    config:
      route:
        receiver: sns
        routes: []
      receivers:
        - name: sns
`,
		},
		{
			name: "disabled alertmanager ignores config",
			yaml: `
kube-prometheus-stack:
  alertmanager:
    enabled: false
    config:
      route: {}
      receivers: []
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkAlertmanagerValues([]byte(tt.yaml))
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("want nil, got %v", err)
			case tt.want != "" && (err == nil || err.Error() != tt.want):
				t.Fatalf("want %q, got %v", tt.want, err)
			}
		})
	}
}
