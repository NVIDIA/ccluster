/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package catalogs

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func verifyScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := discoveryv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func svc(namespace string) *corev1.Service {
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "web"}}
}

// slice returns an EndpointSlice of Service namespace/name with one endpoint per entry
// of ready; a nil entry is an endpoint whose readiness is unknown.
func slice(namespace, name string, ready ...*bool) *discoveryv1.EndpointSlice {
	s := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{
		Namespace: namespace,
		Name:      name + "-abcde",
		Labels:    map[string]string{discoveryv1.LabelServiceName: name},
	}}
	for _, r := range ready {
		s.Endpoints = append(s.Endpoints, discoveryv1.Endpoint{
			Addresses:  []string{"10.0.0.1"},
			Conditions: discoveryv1.EndpointConditions{Ready: r},
		})
	}
	return s
}

func TestVerifyServiceReady(t *testing.T) {
	tests := []struct {
		name    string
		objects []client.Object
		want    string // "" means "expect nil"
	}{
		{
			name:    "ready",
			objects: []client.Object{svc("ns"), slice("ns", "web", new(true))},
		},
		{
			name:    "readiness unknown counts as ready",
			objects: []client.Object{svc("ns"), slice("ns", "web", nil)},
		},
		{
			name:    "one of two endpoints ready",
			objects: []client.Object{svc("ns"), slice("ns", "web", new(false), new(true))},
		},
		{
			name: "no Service",
			want: "no Service ns/web found",
		},
		{
			name:    "Service in another namespace",
			objects: []client.Object{svc("other"), slice("other", "web", new(true))},
			want:    "no Service ns/web found",
		},
		{
			// The workload behind the Service is not created yet, or not scheduled.
			name:    "no endpoints",
			objects: []client.Object{svc("ns")},
			want:    "no ready endpoints for Service ns/web",
		},
		{
			name:    "endpoint not ready",
			objects: []client.Object{svc("ns"), slice("ns", "web", new(false))},
			want:    "no ready endpoints for Service ns/web",
		},
		{
			// Another namespace's endpoints must not vouch for this Service.
			name:    "endpoints of another namespace",
			objects: []client.Object{svc("ns"), slice("other", "web", new(true))},
			want:    "no ready endpoints for Service ns/web",
		},
		{
			name:    "endpoints of another Service",
			objects: []client.Object{svc("ns"), slice("ns", "db", new(true))},
			want:    "no ready endpoints for Service ns/web",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := fake.NewClientBuilder().WithScheme(verifyScheme(t)).WithObjects(tt.objects...).Build()
			err := VerifyServiceReady(context.Background(), r, "ns", "web")
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("want nil, got %v", err)
			case tt.want != "" && (err == nil || err.Error() != tt.want):
				t.Fatalf("want %q, got %v", tt.want, err)
			}
		})
	}
}

// TestVerifyServiceReadyReturnsReadErrors: a read the operator cannot make is not
// reported as a missing Service, which would send a user looking for an install problem
// that is really RBAC or the API server.
func TestVerifyServiceReadyReturnsReadErrors(t *testing.T) {
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "services"}, "web", errors.New("no RBAC"))
	r := fake.NewClientBuilder().WithScheme(verifyScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return forbidden
		},
	}).Build()

	if err := VerifyServiceReady(context.Background(), r, "ns", "web"); !apierrors.IsForbidden(err) {
		t.Fatalf("want the Forbidden error wrapped, got %v", err)
	}
}
