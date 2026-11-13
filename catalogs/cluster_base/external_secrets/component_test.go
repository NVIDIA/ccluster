/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package externalsecrets

import (
	"context"
	"errors"
	"testing"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		admissionregistrationv1.AddToScheme, corev1.AddToScheme, discoveryv1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return scheme
}

// webhookService is where the upstream chart's webhook Service lands for a release named
// external-secrets in namespace external-secrets.
var webhookService = &admissionregistrationv1.ServiceReference{
	Namespace: "external-secrets",
	Name:      "external-secrets-webhook",
}

// webhookConfiguration returns WebhookConfiguration with one entry, name, calling
// service, or a URL when service is nil.
func webhookConfiguration(
	name string, service *admissionregistrationv1.ServiceReference,
) *admissionregistrationv1.ValidatingWebhookConfiguration {
	w := admissionregistrationv1.ValidatingWebhook{Name: name}
	if service != nil {
		w.ClientConfig.Service = service
	} else {
		w.ClientConfig.URL = new("https://eso.example.com/validate")
	}
	return &admissionregistrationv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: WebhookConfiguration},
		Webhooks:   []admissionregistrationv1.ValidatingWebhook{w},
	}
}

func service() *corev1.Service {
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: webhookService.Namespace, Name: webhookService.Name}}
}

func readyEndpoints() *discoveryv1.EndpointSlice {
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: webhookService.Namespace,
			Name:      webhookService.Name + "-abcde",
			Labels:    map[string]string{discoveryv1.LabelServiceName: webhookService.Name},
		},
		Endpoints: []discoveryv1.Endpoint{{
			Addresses:  []string{"10.0.0.1"},
			Conditions: discoveryv1.EndpointConditions{Ready: new(true)},
		}},
	}
}

// listErr makes the fake client's ExternalSecret List fail with err. The fake client
// serves any kind it is asked for, so this is how a test sees an API the server does
// not serve.
func listErr(err error) interceptor.Funcs {
	return interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*metav1.PartialObjectMetadataList); ok {
				return err
			}
			return c.List(ctx, list, opts...)
		},
	}
}

var notServed = &meta.NoKindMatchError{
	GroupKind:        schema.GroupKind{Group: "external-secrets.io", Kind: "ExternalSecret"},
	SearchedVersions: []string{"v1"},
}

// TestVerify covers states of an ESO installation that envtest cannot reach without
// running ESO: its webhook's readiness, and an API server serving only older versions.
func TestVerify(t *testing.T) {
	tests := []struct {
		name    string
		objects []client.Object
		funcs   interceptor.Funcs
		want    string // "" means "expect nil"
	}{
		{
			name: "verified",
			objects: []client.Object{
				webhookConfiguration(webhook, webhookService), service(), readyEndpoints(),
			},
		},
		{
			// ESO is optional: a plane that does not install it passes.
			name: "not installed",
		},
		{
			// An ESO before v0.16.0 has the same webhook configuration, but serves only
			// v1beta1.
			name:    "v1 not served",
			objects: []client.Object{webhookConfiguration(webhook, webhookService), service(), readyEndpoints()},
			funcs:   listErr(notServed),
			want:    "external-secrets.io/v1 ExternalSecret not served; ESO before v0.16.0 does not serve v1",
		},
		{
			// The webhook pod has not passed its readiness probe, for example while
			// cert-controller has not yet written its serving certificate.
			name:    "webhook not ready",
			objects: []client.Object{webhookConfiguration(webhook, webhookService), service()},
			want:    "no ready endpoints for Service external-secrets/external-secrets-webhook",
		},
		{
			name: "no ExternalSecret webhook",
			objects: []client.Object{
				webhookConfiguration("validate.secretstore.external-secrets.io", webhookService),
			},
			want: "ValidatingWebhookConfiguration externalsecret-validate has no webhook " +
				"validate.externalsecret.external-secrets.io calling a Service",
		},
		{
			name:    "webhook calls a URL",
			objects: []client.Object{webhookConfiguration(webhook, nil)},
			want: "ValidatingWebhookConfiguration externalsecret-validate has no webhook " +
				"validate.externalsecret.external-secrets.io calling a Service",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(tt.objects...).
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

// TestVerifyReturnsReadErrors: a read the operator cannot make is not reported as ESO
// missing or old, which would send a user looking for an install problem that is really
// RBAC or the API server.
func TestVerifyReturnsReadErrors(t *testing.T) {
	forbidden := func(resource string) error {
		return apierrors.NewForbidden(schema.GroupResource{Resource: resource}, "", errors.New("no RBAC"))
	}
	tests := []struct {
		name    string
		objects []client.Object
		funcs   interceptor.Funcs
	}{
		{
			name: "webhook configuration",
			funcs: interceptor.Funcs{
				Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
					return forbidden("validatingwebhookconfigurations")
				},
			},
		},
		{
			name:    "ExternalSecrets",
			objects: []client.Object{webhookConfiguration(webhook, webhookService)},
			funcs:   listErr(forbidden("externalsecrets")),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(tt.objects...).
				WithInterceptorFuncs(tt.funcs).Build()
			err := Verify(context.Background(), r, cclusternvidiacomv1alpha1.CCLClusterConfigSpec{})
			if !apierrors.IsForbidden(err) {
				t.Fatalf("want the Forbidden error wrapped, got %v", err)
			}
		})
	}
}
