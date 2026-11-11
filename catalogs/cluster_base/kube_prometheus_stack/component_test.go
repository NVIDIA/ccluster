/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package kubeprometheusstack

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

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
