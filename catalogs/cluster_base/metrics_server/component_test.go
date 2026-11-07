/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package metricsserver

import (
	"context"
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	apiregistrationv1 "k8s.io/kube-aggregator/pkg/apis/apiregistration/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := apiregistrationv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

// apiService returns the metrics APIService with Available set to status, reason and
// message, unless status is empty, in which case the condition is absent: the state
// before the aggregator has checked the Service behind it.
func apiService(status apiregistrationv1.ConditionStatus, reason, message string) *apiregistrationv1.APIService {
	s := &apiregistrationv1.APIService{ObjectMeta: metav1.ObjectMeta{Name: APIService}}
	if status != "" {
		s.Status.Conditions = []apiregistrationv1.APIServiceCondition{
			{Type: apiregistrationv1.Available, Status: status, Reason: reason, Message: message},
		}
	}
	return s
}

// TestVerify covers the APIService states an API server reaches only with a real
// metrics server behind it, which envtest does not run.
func TestVerify(t *testing.T) {
	tests := []struct {
		name    string
		objects []client.Object
		want    string // "" means "expect nil"
	}{
		{
			name:    "available",
			objects: []client.Object{apiService(apiregistrationv1.ConditionTrue, "Passed", "all checks passed")},
		},
		{
			// No metrics server is installed: a bare kind or kubeadm cluster.
			name: "not registered",
			want: "APIService v1beta1.metrics.k8s.io not found",
		},
		{
			// The aggregator's reason says why, e.g. no ready endpoints while the
			// server has not scraped the kubelets yet.
			name: "registered but unavailable",
			objects: []client.Object{apiService(apiregistrationv1.ConditionFalse,
				"MissingEndpoints", "endpoints for service/metrics-server in \"kube-system\" have no addresses")},
			want: "APIService v1beta1.metrics.k8s.io not available: MissingEndpoints: " +
				"endpoints for service/metrics-server in \"kube-system\" have no addresses",
		},
		{
			name:    "no Available condition yet",
			objects: []client.Object{apiService("", "", "")},
			want:    "APIService v1beta1.metrics.k8s.io not available",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(tt.objects...).Build()
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

// TestVerifyReturnsReadErrors: an APIService the operator cannot read is not reported
// as missing, which would send a user looking for an install problem that is really
// RBAC or the API server.
func TestVerifyReturnsReadErrors(t *testing.T) {
	forbidden := apierrors.NewForbidden(
		schema.GroupResource{Group: "apiregistration.k8s.io", Resource: "apiservices"},
		APIService, errors.New("no RBAC"))
	r := fake.NewClientBuilder().WithScheme(testScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return forbidden
		},
	}).Build()

	err := Verify(context.Background(), r, cclusternvidiacomv1alpha1.CCLClusterConfigSpec{})
	if !apierrors.IsForbidden(err) {
		t.Fatalf("want the Forbidden error wrapped, got %v", err)
	}
}
