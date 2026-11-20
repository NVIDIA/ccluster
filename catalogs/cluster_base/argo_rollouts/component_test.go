/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package argorollouts

import (
	"context"
	"strings"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
)

// TestVerify checks that Verify passes only when the Rollout CRD exists and a controller
// has renewed the Argo Rollouts Lease lately.
func TestVerify(t *testing.T) {
	rolloutCRD := &apiextensionsv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: "rollouts.argoproj.io"}}
	lease := func(age time.Duration) client.Object {
		return &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{Namespace: "argo-rollouts", Name: "argo-rollouts-controller-lock"},
			Spec: coordinationv1.LeaseSpec{
				HolderIdentity:       new("argo-rollouts-0_uid"),
				LeaseDurationSeconds: new(int32(15)),
				RenewTime:            &metav1.MicroTime{Time: time.Now().Add(-age)},
			},
		}
	}
	tests := []struct {
		name    string
		objects []client.Object
		want    string // "" means "expect nil"; otherwise a prefix of the error
	}{
		{
			name:    "CRD installed and controller leading",
			objects: []client.Object{rolloutCRD, lease(time.Second)},
		},
		{
			name: "not installed",
			want: "CustomResourceDefinition rollouts.argoproj.io not found",
		},
		{
			name:    "CRD without a controller",
			objects: []client.Object{rolloutCRD},
			want:    "no Lease argo-rollouts-controller-lock found in any namespace",
		},
		{
			// Uninstalling leaves the CRDs and the Lease the controller held.
			name:    "CRD and Lease left by an uninstall",
			objects: []client.Object{rolloutCRD, lease(time.Hour)},
			want: "no controller holds Lease argo-rollouts-controller-lock: " +
				"Lease argo-rollouts/argo-rollouts-controller-lock last renewed at ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := apiextensionsv1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := coordinationv1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			// The index stands in for the API server's metadata.name field selector.
			r := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.objects...).
				WithIndex(&coordinationv1.Lease{}, "metadata.name", func(o client.Object) []string {
					return []string{o.GetName()}
				}).Build()
			err := Verify(context.Background(), r, cclusternvidiacomv1alpha1.CCLClusterConfigSpec{})
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("want nil, got %v", err)
			case tt.want != "" && (err == nil || !strings.HasPrefix(err.Error(), tt.want)):
				t.Fatalf("want an error starting %q, got %v", tt.want, err)
			}
		})
	}
}
