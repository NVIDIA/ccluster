/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package gatewayapi

import (
	"context"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
)

func crd(name string) client.Object {
	return &apiextensionsv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func TestVerify(t *testing.T) {
	installed := []client.Object{
		crd("gateways.gateway.networking.k8s.io"), crd("httproutes.gateway.networking.k8s.io"),
	}
	tests := []struct {
		name    string
		objects []client.Object
		funcs   interceptor.Funcs
		want    string // "" means "expect nil"
	}{
		{
			name:    "verified",
			objects: installed,
		},
		{
			name: "not installed",
			want: "CustomResourceDefinition gateways.gateway.networking.k8s.io not found",
		},
		{
			name:    "no HTTPRoute CRD",
			objects: []client.Object{crd("gateways.gateway.networking.k8s.io")},
			want:    "CustomResourceDefinition httproutes.gateway.networking.k8s.io not found",
		},
		{
			// CRDs from before Gateway API v1.0 serve HTTPRoute as v1beta1 at most. The
			// fake client serves any kind, so the interceptor answers as the API server
			// would.
			name:    "v1 not served",
			objects: installed,
			funcs: interceptor.Funcs{
				List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
					return &meta.NoKindMatchError{
						GroupKind:        schema.GroupKind{Group: "gateway.networking.k8s.io", Kind: "HTTPRoute"},
						SearchedVersions: []string{"v1"},
					}
				},
			},
			want: "gateway.networking.k8s.io/v1 HTTPRoute not served; Gateway API before v1.0 does not serve v1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := apiextensionsv1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			r := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.objects...).
				WithInterceptorFuncs(tt.funcs).Build()
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
