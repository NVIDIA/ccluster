/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package keda

import (
	"context"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	apiregistrationv1 "k8s.io/kube-aggregator/pkg/apis/apiregistration/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
)

var scaledObjectCRD = &apiextensionsv1.CustomResourceDefinition{
	ObjectMeta: metav1.ObjectMeta{Name: "scaledobjects.keda.sh"},
}

// externalMetrics returns the external metrics APIService, with an Available condition
// of status unless status is "".
func externalMetrics(status apiregistrationv1.ConditionStatus, reason, message string) *apiregistrationv1.APIService {
	svc := &apiregistrationv1.APIService{ObjectMeta: metav1.ObjectMeta{Name: "v1beta1.external.metrics.k8s.io"}}
	if status != "" {
		svc.Status.Conditions = []apiregistrationv1.APIServiceCondition{{
			Type: apiregistrationv1.Available, Status: status, Reason: reason, Message: message,
		}}
	}
	return svc
}

// TestVerify needs both: the ScaledObject API the deployments render, and the external
// metrics API the HPAs KEDA creates read.
func TestVerify(t *testing.T) {
	tests := []struct {
		name    string
		objects []client.Object
		want    string // "" means "expect nil"
	}{
		{
			name:    "both served",
			objects: []client.Object{scaledObjectCRD, externalMetrics(apiregistrationv1.ConditionTrue, "Passed", "")},
		},
		{
			name: "not installed",
			want: "CustomResourceDefinition scaledobjects.keda.sh not found",
		},
		{
			// Another server of the external metrics API is not KEDA.
			name:    "external metrics without KEDA",
			objects: []client.Object{externalMetrics(apiregistrationv1.ConditionTrue, "Passed", "")},
			want:    "CustomResourceDefinition scaledobjects.keda.sh not found",
		},
		{
			name:    "no APIService",
			objects: []client.Object{scaledObjectCRD},
			want:    "APIService v1beta1.external.metrics.k8s.io not found",
		},
		{
			// KEDA's metrics server is down or unreachable.
			name: "APIService unavailable",
			objects: []client.Object{scaledObjectCRD, externalMetrics(apiregistrationv1.ConditionFalse,
				"MissingEndpoints", "endpoints for service/keda-operator-metrics-apiserver in \"keda\" have no addresses")},
			want: "APIService v1beta1.external.metrics.k8s.io not available: MissingEndpoints: " +
				"endpoints for service/keda-operator-metrics-apiserver in \"keda\" have no addresses",
		},
		{
			name:    "APIService without an Available condition yet",
			objects: []client.Object{scaledObjectCRD, externalMetrics("", "", "")},
			want:    "APIService v1beta1.external.metrics.k8s.io not available",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			for _, add := range []func(*runtime.Scheme) error{apiextensionsv1.AddToScheme, apiregistrationv1.AddToScheme} {
				if err := add(scheme); err != nil {
					t.Fatal(err)
				}
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
