/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package catalogs

import (
	"context"
	"errors"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// crd returns a CustomResourceDefinition named name, with Established set to status
// unless status is empty, in which case the condition is absent: the state before the
// API server has looked at a new CRD.
func crd(name string, status apiextensionsv1.ConditionStatus) *apiextensionsv1.CustomResourceDefinition {
	c := &apiextensionsv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if status != "" {
		c.Status.Conditions = []apiextensionsv1.CustomResourceDefinitionCondition{
			{Type: apiextensionsv1.NamesAccepted, Status: apiextensionsv1.ConditionTrue},
			{Type: apiextensionsv1.Established, Status: status},
		}
	}
	return c
}

// deleting marks c as deleted while a finalizer still holds it, as the API server does
// when custom resources of the CRD remain.
func deleting(c *apiextensionsv1.CustomResourceDefinition) *apiextensionsv1.CustomResourceDefinition {
	now := metav1.Now()
	c.DeletionTimestamp = &now
	c.Finalizers = []string{"customresourcecleanup.apiextensions.k8s.io"}
	return c
}

// TestCRDsEstablished covers the states of a CRD envtest cannot cheaply hold still: an
// API server establishes a CRD as soon as it accepts it.
func TestCRDsEstablished(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		objects []client.Object
		want    string // "" means "expect nil"
	}{
		{
			name:    "every CRD established",
			objects: []client.Object{crd("a.example.com", "True"), crd("b.example.com", "True")},
		},
		{
			name:    "one CRD missing",
			objects: []client.Object{crd("a.example.com", "True")},
			want:    "CRD b.example.com not found",
		},
		{
			name:    "one CRD not established",
			objects: []client.Object{crd("a.example.com", "False"), crd("b.example.com", "True")},
			want:    "CRD a.example.com not established",
		},
		{
			name:    "no Established condition yet",
			objects: []client.Object{crd("a.example.com", ""), crd("b.example.com", "True")},
			want:    "CRD a.example.com not established",
		},
		{
			// Established stays True while deletion waits on remaining custom resources.
			name:    "one CRD being deleted",
			objects: []client.Object{deleting(crd("a.example.com", "True")), crd("b.example.com", "True")},
			want:    "CRD a.example.com being deleted",
		},
		{
			// Every problem is named, so a user sees the whole gap at once.
			name: "every CRD missing",
			want: "CRD a.example.com not found, CRD b.example.com not found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.objects...).Build()
			err := CRDsEstablished(context.Background(), r, "a.example.com", "b.example.com")
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("want nil, got %v", err)
			case tt.want != "" && (err == nil || err.Error() != tt.want):
				t.Fatalf("want %q, got %v", tt.want, err)
			}
		})
	}
}

// TestCRDsEstablishedReturnsReadErrors: a CRD the operator cannot read is not reported
// as missing, which would send a user looking for an install problem that is really
// RBAC or the API server.
func TestCRDsEstablishedReturnsReadErrors(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	forbidden := apierrors.NewForbidden(
		schema.GroupResource{Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions"},
		"a.example.com", errors.New("no RBAC"))
	r := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return forbidden
		},
	}).Build()

	err := CRDsEstablished(context.Background(), r, "a.example.com")
	if !apierrors.IsForbidden(err) {
		t.Fatalf("want the Forbidden error wrapped, got %v", err)
	}
}
