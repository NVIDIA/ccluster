/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package catalogs

import (
	"context"
	"errors"
	"testing"
	"time"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
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

func crd(name string) *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func crdScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func TestVerifyCRDExists(t *testing.T) {
	tests := []struct {
		name    string
		objects []client.Object
		want    string // "" means "expect nil"
	}{
		{
			name:    "exists",
			objects: []client.Object{crd("widgets.example.com"), crd("gadgets.example.com")},
		},
		{
			name: "no CRDs",
			want: "CustomResourceDefinition widgets.example.com not found",
		},
		{
			// The same kind in another group is not the API the consumers render.
			name:    "another group",
			objects: []client.Object{crd("widgets.example.org")},
			want:    "CustomResourceDefinition widgets.example.com not found",
		},
		{
			// Another kind of the same group does not stand in for it either.
			name:    "another kind in the group",
			objects: []client.Object{crd("gadgets.example.com")},
			want:    "CustomResourceDefinition widgets.example.com not found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := fake.NewClientBuilder().WithScheme(crdScheme(t)).WithObjects(tt.objects...).Build()
			err := VerifyCRDExists(context.Background(), r, "widgets.example.com")
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("want nil, got %v", err)
			case tt.want != "" && (err == nil || err.Error() != tt.want):
				t.Fatalf("want %q, got %v", tt.want, err)
			}
		})
	}
}

// TestVerifyCRDExistsReadsMetadataOnly pins the read as metadata-only, so that the ip
// operator's cache never holds CRD schemas.
func TestVerifyCRDExistsReadsMetadataOnly(t *testing.T) {
	var got client.Object
	r := fake.NewClientBuilder().WithScheme(crdScheme(t)).WithObjects(crd("widgets.example.com")).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(
				ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption,
			) error {
				got = obj
				return c.Get(ctx, key, obj, opts...)
			},
		}).Build()

	if err := VerifyCRDExists(context.Background(), r, "widgets.example.com"); err != nil {
		t.Fatal(err)
	}
	if _, ok := got.(*metav1.PartialObjectMetadata); !ok {
		t.Fatalf("want a *metav1.PartialObjectMetadata read, got %T", got)
	}
}

// TestVerifyCRDExistsReturnsReadErrors: a CRD the operator cannot read is not reported
// as missing.
func TestVerifyCRDExistsReturnsReadErrors(t *testing.T) {
	forbidden := apierrors.NewForbidden(schema.GroupResource{
		Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions"}, "widgets.example.com", errors.New("no RBAC"))
	r := fake.NewClientBuilder().WithScheme(crdScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return forbidden
		},
	}).Build()

	if err := VerifyCRDExists(context.Background(), r, "widgets.example.com"); !apierrors.IsForbidden(err) {
		t.Fatalf("want the Forbidden error wrapped, got %v", err)
	}
}

var widget = schema.GroupVersionKind{Group: "example.com", Version: "v1", Kind: "Widget"}

// kindClient returns a fake client whose List fails with listErr, and fails the test
// unless the List asks for the metadata of WidgetList. The fake client serves any kind,
// so listErr is how a test sees what the API server would answer.
func kindClient(t *testing.T, listErr error) client.WithWatch {
	t.Helper()
	return fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			gvk := list.GetObjectKind().GroupVersionKind()
			if _, ok := list.(*metav1.PartialObjectMetadataList); !ok || gvk != widget.GroupVersion().WithKind("WidgetList") {
				t.Errorf("want a *metav1.PartialObjectMetadataList of WidgetList, got %T of %v", list, gvk)
			}
			if listErr != nil {
				return listErr
			}
			return c.List(ctx, list, opts...)
		},
	}).Build()
}

func TestVerifyKindServed(t *testing.T) {
	notServed := "example.com/v1 Widget not served; Widgets before v2 do not serve v1"
	tests := []struct {
		name    string
		listErr error
		want    string // "" means "expect nil"
	}{
		{name: "served"},
		{
			name:    "not served",
			listErr: &meta.NoKindMatchError{GroupKind: widget.GroupKind(), SearchedVersions: []string{"v1"}},
			want:    notServed,
		},
		{
			// What the RESTMapper returns when it learns of the group from aggregated
			// discovery but the group does not serve the version.
			name: "not served, from aggregated discovery",
			listErr: &apiutil.ErrResourceDiscoveryFailed{
				widget.GroupVersion(): &meta.NoResourceMatchError{PartialResource: widget.GroupVersion().WithResource("")},
			},
			want: notServed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := VerifyKindServed(context.Background(), kindClient(t, tt.listErr), widget,
				"Widgets before v2 do not serve v1")
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("want nil, got %v", err)
			case tt.want != "" && (err == nil || err.Error() != tt.want):
				t.Fatalf("want %q, got %v", tt.want, err)
			}
		})
	}
}

// TestVerifyKindServedReturnsReadErrors: a List the operator cannot make is not reported
// as the kind not served, which would send a user looking for an old release when the
// cause is RBAC or the API server.
func TestVerifyKindServedReturnsReadErrors(t *testing.T) {
	forbidden := apierrors.NewForbidden(schema.GroupResource{Group: "example.com", Resource: "widgets"}, "",
		errors.New("no RBAC"))
	err := VerifyKindServed(context.Background(), kindClient(t, forbidden), widget, "unused")
	if !apierrors.IsForbidden(err) {
		t.Fatalf("want the Forbidden error wrapped, got %v", err)
	}
}

func webhookScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := verifyScheme(t)
	if err := admissionregistrationv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

// webhookConfig returns a ValidatingWebhookConfiguration named name with entries.
func webhookConfig(
	name string, entries ...admissionregistrationv1.ValidatingWebhook,
) *admissionregistrationv1.ValidatingWebhookConfiguration {
	return &admissionregistrationv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Webhooks:   entries,
	}
}

// entry returns a webhook entry named name that calls Service ns/web, with a CA bundle
// when injected is true.
func entry(name string, injected bool) admissionregistrationv1.ValidatingWebhook {
	w := admissionregistrationv1.ValidatingWebhook{
		Name: name,
		ClientConfig: admissionregistrationv1.WebhookClientConfig{
			Service: &admissionregistrationv1.ServiceReference{Namespace: "ns", Name: "web"},
		},
	}
	if injected {
		w.ClientConfig.CABundle = []byte("ca")
	}
	return w
}

func TestVerifyValidatingWebhookServed(t *testing.T) {
	const hook = "check.example.com"
	served := []client.Object{svc("ns"), slice("ns", "web", new(true))}
	url := "https://hooks.example.com/validate"
	byURL := admissionregistrationv1.ValidatingWebhook{
		Name:         hook,
		ClientConfig: admissionregistrationv1.WebhookClientConfig{URL: &url, CABundle: []byte("ca")},
	}
	byURLNoCA := admissionregistrationv1.ValidatingWebhook{
		Name:         hook,
		ClientConfig: admissionregistrationv1.WebhookClientConfig{URL: &url},
	}
	// Left by an earlier installation whose namespace was deleted: webhook
	// configurations are cluster-scoped and outlive it.
	stale := entry(hook, true)
	stale.ClientConfig.Service = &admissionregistrationv1.ServiceReference{Namespace: "old", Name: "web"}

	tests := []struct {
		name    string
		objects []client.Object
		want    string // "" means "expect nil"
	}{
		{
			name:    "served",
			objects: append([]client.Object{webhookConfig("release-a", entry(hook, true))}, served...),
		},
		{
			// The entry, not the configuration, carries the fixed name.
			name: "found among other entries",
			objects: append([]client.Object{webhookConfig("release-a",
				entry("other.example.com", true), entry(hook, true))}, served...),
		},
		{
			name: "no configurations",
			want: "no ValidatingWebhookConfiguration has webhook check.example.com",
		},
		{
			name:    "only other webhooks",
			objects: append([]client.Object{webhookConfig("check.example.com", entry("other.example.com", true))}, served...),
			want:    "no ValidatingWebhookConfiguration has webhook check.example.com",
		},
		{
			// cainjector has not run: the API server cannot trust the webhook.
			name:    "no CA bundle",
			objects: append([]client.Object{webhookConfig("release-a", entry(hook, false))}, served...),
			want:    "webhook check.example.com in ValidatingWebhookConfiguration release-a: no CA bundle injected",
		},
		{
			name:    "no Service",
			objects: []client.Object{webhookConfig("release-a", entry(hook, true))},
			want:    "webhook check.example.com in ValidatingWebhookConfiguration release-a: no Service ns/web found",
		},
		{
			name:    "not served",
			objects: []client.Object{webhookConfig("release-a", entry(hook, true)), svc("ns")},
			want: "webhook check.example.com in ValidatingWebhookConfiguration release-a: " +
				"no ready endpoints for Service ns/web",
		},
		{
			// No Service to read endpoints of: the CA bundle is all there is to check.
			name:    "reached by URL",
			objects: []client.Object{webhookConfig("release-a", byURL)},
		},
		{
			name:    "reached by URL without a CA bundle",
			objects: []client.Object{webhookConfig("release-a", byURLNoCA)},
			want:    "webhook check.example.com in ValidatingWebhookConfiguration release-a: no CA bundle injected",
		},
		{
			name: "both registrations pass",
			objects: append([]client.Object{
				webhookConfig("release-a", entry(hook, true)),
				webhookConfig("release-b", entry(hook, true)),
			}, served...),
		},
		{
			// The API server calls both, so the stale one rejects writes the served one
			// would accept.
			name: "a stale registration beside a served one",
			objects: append([]client.Object{
				webhookConfig("release-a", entry(hook, true)),
				webhookConfig("release-b", stale),
			}, served...),
			want: "webhook check.example.com in ValidatingWebhookConfiguration release-b: no Service old/web found",
		},
		{
			name: "neither registration passes",
			objects: []client.Object{
				webhookConfig("release-b", entry(hook, false)),
				webhookConfig("release-a", entry(hook, true)),
			},
			want: "webhook check.example.com in ValidatingWebhookConfiguration release-a: no Service ns/web found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := fake.NewClientBuilder().WithScheme(webhookScheme(t)).WithObjects(tt.objects...).Build()
			err := VerifyValidatingWebhookServed(context.Background(), r, hook)
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("want nil, got %v", err)
			case tt.want != "" && (err == nil || err.Error() != tt.want):
				t.Fatalf("want %q, got %v", tt.want, err)
			}
		})
	}
}

// TestVerifyValidatingWebhookServedReturnsReadErrors: configurations the operator cannot
// list are not reported as a missing webhook.
func TestVerifyValidatingWebhookServedReturnsReadErrors(t *testing.T) {
	forbidden := apierrors.NewForbidden(schema.GroupResource{
		Group: "admissionregistration.k8s.io", Resource: "validatingwebhookconfigurations"}, "", errors.New("no RBAC"))
	r := fake.NewClientBuilder().WithScheme(webhookScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return forbidden
		},
	}).Build()

	if err := VerifyValidatingWebhookServed(context.Background(), r, "check.example.com"); !apierrors.IsForbidden(err) {
		t.Fatalf("want the Forbidden error wrapped, got %v", err)
	}
}

// lease returns a Lease named name in namespace, held by holder ("" for none), with a
// 15-second duration, renewed age ago; a negative age leaves renewTime unset.
func lease(namespace, name, holder string, age time.Duration) *coordinationv1.Lease {
	l := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       coordinationv1.LeaseSpec{LeaseDurationSeconds: new(int32(15))},
	}
	if holder != "" {
		l.Spec.HolderIdentity = new(holder)
	}
	if age >= 0 {
		l.Spec.RenewTime = &metav1.MicroTime{Time: time.Now().Add(-age)}
	}
	return l
}

// leaseClient serves objects as the API server does for VerifyLeaseRenewed's list: the
// fake client needs an index to filter by metadata.name.
func leaseClient(t *testing.T, objects ...client.Object) client.WithWatch {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := coordinationv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).
		WithIndex(&coordinationv1.Lease{}, "metadata.name", func(o client.Object) []string {
			return []string{o.GetName()}
		}).Build()
}

func TestVerifyLeaseRenewed(t *testing.T) {
	const stale = 10 * time.Minute
	staleAt := func(l *coordinationv1.Lease) string {
		return l.Spec.RenewTime.UTC().Format(time.RFC3339)
	}
	staleA := lease("a", "lock", "pod-a", stale)
	staleB := lease("b", "lock", "pod-b", stale)
	tests := []struct {
		name    string
		objects []client.Object
		want    string // "" means "expect nil"
	}{
		{
			name:    "renewed",
			objects: []client.Object{lease("a", "lock", "pod-a", time.Second)},
		},
		{
			name:    "within the duration plus skew",
			objects: []client.Object{lease("a", "lock", "pod-a", 40*time.Second)},
		},
		{
			name: "no Lease",
			want: "no Lease lock found in any namespace",
		},
		{
			name:    "another name",
			objects: []client.Object{lease("a", "other-lock", "pod-a", time.Second)},
			want:    "no Lease lock found in any namespace",
		},
		{
			name:    "stale",
			objects: []client.Object{staleA},
			want:    "no controller holds Lease lock: Lease a/lock last renewed at " + staleAt(staleA),
		},
		{
			name:    "no holder",
			objects: []client.Object{lease("a", "lock", "", time.Second)},
			want:    "no controller holds Lease lock: Lease a/lock has no holder",
		},
		{
			name:    "never renewed",
			objects: []client.Object{lease("a", "lock", "pod-a", -1)},
			want:    "no controller holds Lease lock: Lease a/lock was never renewed",
		},
		{
			// A reinstall into another namespace leaves the old Lease behind.
			name:    "stale in one namespace, renewed in another",
			objects: []client.Object{staleA, lease("b", "lock", "pod-b", time.Second)},
		},
		{
			name:    "stale in every namespace",
			objects: []client.Object{staleA, staleB},
			want: "no controller holds Lease lock: Lease a/lock last renewed at " + staleAt(staleA) +
				"; Lease b/lock last renewed at " + staleAt(staleB),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := VerifyLeaseRenewed(context.Background(), leaseClient(t, tt.objects...), "lock")
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("want nil, got %v", err)
			case tt.want != "" && (err == nil || err.Error() != tt.want):
				t.Fatalf("want %q, got %v", tt.want, err)
			}
		})
	}
}

// TestVerifyLeaseRenewedReturnsReadErrors: Leases the operator cannot list are not
// reported as missing.
func TestVerifyLeaseRenewedReturnsReadErrors(t *testing.T) {
	forbidden := apierrors.NewForbidden(schema.GroupResource{
		Group: "coordination.k8s.io", Resource: "leases"}, "", errors.New("no RBAC"))
	scheme := runtime.NewScheme()
	if err := coordinationv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	r := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return forbidden
		},
	}).Build()

	if err := VerifyLeaseRenewed(context.Background(), r, "lock"); !apierrors.IsForbidden(err) {
		t.Fatalf("want the Forbidden error wrapped, got %v", err)
	}
}
