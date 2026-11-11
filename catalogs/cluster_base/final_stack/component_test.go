/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package finalstack

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/yaml"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	certmanagerv1 "github.com/NVIDIA/ccluster/internal/api/certmanager/v1"
)

func scheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := certmanagerv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

// issuer returns a ClusterIssuer named name with conditions.
func issuer(name string, conditions ...certmanagerv1.IssuerCondition) *certmanagerv1.ClusterIssuer {
	return &certmanagerv1.ClusterIssuer{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     certmanagerv1.IssuerStatus{Conditions: conditions},
	}
}

func ready(status metav1.ConditionStatus, reason, message string) certmanagerv1.IssuerCondition {
	return certmanagerv1.IssuerCondition{
		Type: certmanagerv1.IssuerConditionReady, Status: status, Reason: reason, Message: message,
	}
}

// TestVerify pins the issuer to the name the catalog's Ingresses request, and passes only
// once cert-manager reports it Ready.
func TestVerify(t *testing.T) {
	tests := []struct {
		name    string
		objects []client.Object
		want    string // "" means "expect nil"
	}{
		{
			name:    "ready",
			objects: []client.Object{issuer("acme-ca", ready(metav1.ConditionTrue, "ACMEAccountRegistered", ""))},
		},
		{
			name: "not found",
			want: "ClusterIssuer acme-ca not found",
		},
		{
			// A ready issuer the Ingresses do not name is no use to them.
			name:    "another name",
			objects: []client.Object{issuer("letsencrypt", ready(metav1.ConditionTrue, "ACMEAccountRegistered", ""))},
			want:    "ClusterIssuer acme-ca not found",
		},
		{
			// The ACME server cannot be reached, for one.
			name: "not ready",
			objects: []client.Object{issuer("acme-ca", ready(metav1.ConditionFalse, "ErrRegisterACMEAccount",
				"Failed to register ACME account: connection refused"))},
			want: "ClusterIssuer acme-ca not ready: ErrRegisterACMEAccount: " +
				"Failed to register ACME account: connection refused",
		},
		{
			// cert-manager has not reconciled it yet, or its controller is not running.
			name:    "no conditions",
			objects: []client.Object{issuer("acme-ca")},
			want:    "ClusterIssuer acme-ca not ready",
		},
		{
			name: "only other conditions",
			objects: []client.Object{issuer("acme-ca",
				certmanagerv1.IssuerCondition{Type: "Other", Status: metav1.ConditionTrue})},
			want: "ClusterIssuer acme-ca not ready",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(tt.objects...).Build()
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

// TestVerifyReadErrors: a cluster without cert-manager has no ClusterIssuer kind, which
// is reported as such, and any other failed read is returned rather than reported as a
// missing issuer.
func TestVerifyReadErrors(t *testing.T) {
	gvk := certmanagerv1.GroupVersion.WithKind("ClusterIssuer")
	tests := []struct {
		name  string
		err   error
		check func(error) bool
	}{
		{
			name: "kind not served",
			err:  &meta.NoKindMatchError{GroupKind: gvk.GroupKind(), SearchedVersions: []string{gvk.Version}},
			check: func(err error) bool {
				return err != nil && err.Error() == "ClusterIssuer kind not served, is cert-manager installed?"
			},
		},
		{
			name: "forbidden",
			err: apierrors.NewForbidden(schema.GroupResource{Group: "cert-manager.io", Resource: "clusterissuers"},
				"acme-ca", errors.New("no RBAC")),
			check: apierrors.IsForbidden,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := fake.NewClientBuilder().WithScheme(scheme(t)).WithInterceptorFuncs(interceptor.Funcs{
				Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
					return tt.err
				},
			}).Build()
			if err := Verify(context.Background(), r, cclusternvidiacomv1alpha1.CCLClusterConfigSpec{}); !tt.check(err) {
				t.Fatalf("unexpected error %v", err)
			}
		})
	}
}

// TestClusterIssuerMatchesTemplate keeps ClusterIssuer the name the chart renders.
func TestClusterIssuerMatchesTemplate(t *testing.T) {
	raw, err := os.ReadFile("templates/cluster-issuers.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// The template holds no actions but its license comment.
	_, doc, ok := strings.Cut(string(raw), "-}}")
	if !ok {
		t.Fatal("license comment not found")
	}
	var obj metav1.PartialObjectMetadata
	if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
		t.Fatal(err)
	}
	if obj.Kind != "ClusterIssuer" || obj.Name != ClusterIssuer {
		t.Fatalf("template renders %s %q, want ClusterIssuer %q", obj.Kind, obj.Name, ClusterIssuer)
	}
}
