/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package cclclient_test runs the client against a real API server serving the ccluster
// CRDs, so server-side apply, the CRDs' validation and 404s behave as they do in a
// cluster. No operator runs: status is written by the tests, and nothing has finalizers.
package cclclient_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/pkg/cclclient"
	cclusterac "github.com/NVIDIA/ccluster/pkg/client/applyconfiguration/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/pkg/client/clientset/versioned"
)

var (
	clientset  versioned.Interface
	kubeClient kubernetes.Interface
)

const (
	token  = "infra-token"
	server = "https://10.0.0.1:6443"
	domain = "gpu-a.example.com"
)

var caCert = []byte("-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n")

func TestMain(m *testing.M) {
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	// As in the controller suites: outside make, use the binaries make setup-envtest put
	// in bin/k8s.
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		if entries, err := os.ReadDir(filepath.Join("..", "..", "bin", "k8s")); err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					env.BinaryAssetsDirectory = filepath.Join("..", "..", "bin", "k8s", entry.Name())
					break
				}
			}
		}
	}

	config, err := env.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, "starting envtest:", err)
		os.Exit(1)
	}
	if clientset, err = versioned.NewForConfig(config); err != nil {
		fmt.Fprintln(os.Stderr, "creating clientset:", err)
		os.Exit(1)
	}
	if kubeClient, err = kubernetes.NewForConfig(config); err != nil {
		fmt.Fprintln(os.Stderr, "creating Kubernetes clientset:", err)
		os.Exit(1)
	}

	code := m.Run()
	if err := env.Stop(); err != nil {
		fmt.Fprintln(os.Stderr, "stopping envtest:", err)
	}
	os.Exit(code)
}

func TestEnsureNamespaceCreatesItOnce(t *testing.T) {
	ctx := t.Context()
	client := cclclient.New(uniqueName(t, "tenant"), kubeClient, clientset)

	for range 2 {
		if err := client.EnsureNamespace(ctx); err != nil {
			t.Fatalf("EnsureNamespace() error = %v", err)
		}
	}
	if _, err := kubeClient.CoreV1().Namespaces().Get(ctx, client.Namespace(), metav1.GetOptions{}); err != nil {
		t.Fatalf("namespace %s: %v", client.Namespace(), err)
	}
}

func TestApplyInfraPlaneWritesPlaneAndArgoCDSecret(t *testing.T) {
	ctx := t.Context()
	client := newClient(t)

	plane, err := client.ApplyInfraPlane(ctx, cclclient.InfraPlaneOptions{
		Name: "gpu-a", Domain: domain, Server: server, BearerToken: token, CACert: caCert,
	})
	if err != nil {
		t.Fatalf("ApplyInfraPlane() error = %v", err)
	}
	if plane.Namespace != client.Namespace() || plane.Name != "gpu-a" || plane.UID == "" {
		t.Errorf("ApplyInfraPlane() returned %s/%s (uid %q), want the stored %s/gpu-a",
			plane.Namespace, plane.Name, plane.UID, client.Namespace())
	}
	if plane.Spec.Domain != domain || plane.Spec.ClusterCredentialsRef.Name != "gpu-a-credentials" {
		t.Errorf("spec = %+v, want domain %s and clusterCredentialsRef gpu-a-credentials", plane.Spec, domain)
	}

	got, err := client.GetInfraPlane(ctx, "gpu-a")
	if err != nil {
		t.Fatalf("GetInfraPlane() error = %v", err)
	}
	if got.UID != plane.UID {
		t.Errorf("GetInfraPlane() uid = %s, want %s", got.UID, plane.UID)
	}

	secret := credentialsSecret(t, client, "gpu-a")
	if secret.Type != corev1.SecretTypeOpaque {
		t.Errorf("Secret type = %s, want Opaque", secret.Type)
	}
	if got := string(secret.Data["server"]); got != server {
		t.Errorf("server = %q, want %q", got, server)
	}
	wantConfig := map[string]any{
		"bearerToken": token,
		"tlsClientConfig": map[string]any{
			"insecure": false,
			// Argo CD reads caData as the base64 of the PEM.
			"caData": base64.StdEncoding.EncodeToString(caCert),
		},
	}
	if got := clusterConfig(t, secret); !reflect.DeepEqual(got, wantConfig) {
		t.Errorf("config = %v, want %v", got, wantConfig)
	}
}

// Re-registering rotates the token in place: the same field manager owns every field, so
// the second apply updates rather than conflicts.
func TestApplyInfraPlaneAgainUpdatesTheCredentials(t *testing.T) {
	ctx := t.Context()
	client := newClient(t)
	opts := cclclient.InfraPlaneOptions{Name: "gpu-a", Domain: domain, Server: server, BearerToken: token}

	if _, err := client.ApplyInfraPlane(ctx, opts); err != nil {
		t.Fatal(err)
	}
	opts.BearerToken = "rotated"
	if _, err := client.ApplyInfraPlane(ctx, opts); err != nil {
		t.Fatalf("second ApplyInfraPlane() error = %v", err)
	}

	if got := clusterConfig(t, credentialsSecret(t, client, "gpu-a"))["bearerToken"]; got != "rotated" {
		t.Errorf("bearerToken = %v, want rotated", got)
	}
}

// With no server, token or CA, the plane is the cluster the operator runs in.
func TestApplyInfraPlaneDefaultsToTheInClusterServer(t *testing.T) {
	ctx := t.Context()
	client := newClient(t)

	if _, err := client.ApplyInfraPlane(ctx, cclclient.InfraPlaneOptions{Name: "local", Domain: domain}); err != nil {
		t.Fatal(err)
	}

	secret := credentialsSecret(t, client, "local")
	if got := string(secret.Data["server"]); got != cclclient.InClusterServer {
		t.Errorf("server = %q, want %q", got, cclclient.InClusterServer)
	}
	want := map[string]any{"tlsClientConfig": map[string]any{"insecure": false}}
	if got := clusterConfig(t, secret); !reflect.DeepEqual(got, want) {
		t.Errorf("config = %v, want %v", got, want)
	}
}

// The plane is applied first, so a domain the API server rejects leaves the credentials
// as they were: new credentials under the old domain would retarget the existing plane.
func TestApplyInfraPlaneWithRejectedDomainKeepsTheCredentials(t *testing.T) {
	ctx := t.Context()
	client := newClient(t)
	opts := cclclient.InfraPlaneOptions{Name: "gpu-a", Domain: domain, Server: server, BearerToken: token}
	if _, err := client.ApplyInfraPlane(ctx, opts); err != nil {
		t.Fatal(err)
	}

	for _, rejected := range []string{"gpu-b.example.com", "Not_A_Domain"} {
		opts.Domain, opts.BearerToken = rejected, "rotated"
		if _, err := client.ApplyInfraPlane(ctx, opts); !apierrors.IsInvalid(err) {
			t.Errorf("ApplyInfraPlane(domain %q) error = %v, want Invalid", rejected, err)
		}
	}

	if got := clusterConfig(t, credentialsSecret(t, client, "gpu-a"))["bearerToken"]; got != token {
		t.Errorf("bearerToken = %v, want the original %s", got, token)
	}
}

func TestDeleteInfraPlaneRemovesPlaneAndSecret(t *testing.T) {
	ctx := t.Context()
	client := newClient(t)
	if _, err := client.ApplyInfraPlane(ctx, cclclient.InfraPlaneOptions{Name: "gpu-a", Domain: domain}); err != nil {
		t.Fatal(err)
	}

	// The second call finds both gone, which is not an error.
	for range 2 {
		if err := client.DeleteInfraPlane(ctx, "gpu-a"); err != nil {
			t.Fatalf("DeleteInfraPlane() error = %v", err)
		}
	}

	if _, err := client.GetInfraPlane(ctx, "gpu-a"); !apierrors.IsNotFound(err) {
		t.Errorf("GetInfraPlane() error = %v, want NotFound", err)
	}
	if _, err := kubeClient.CoreV1().Secrets(client.Namespace()).Get(ctx,
		"gpu-a-credentials", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("credentials Secret: error = %v, want NotFound", err)
	}
}

// The API server answers a delete in a namespace that does not exist with the same 404 as
// a missing object.
func TestDeletesInAMissingNamespaceSucceed(t *testing.T) {
	ctx := t.Context()
	client := cclclient.New(uniqueName(t, "missing"), kubeClient, clientset)

	if err := client.DeleteInfraPlane(ctx, "gpu-a"); err != nil {
		t.Errorf("DeleteInfraPlane() error = %v", err)
	}
	if err := client.DeleteComponentSet(ctx, "base"); err != nil {
		t.Errorf("DeleteComponentSet() error = %v", err)
	}
}

func TestApplyComponentSetReplacesTheComponents(t *testing.T) {
	ctx := t.Context()
	client := newClient(t)
	values := &apiextensionsv1.JSON{Raw: []byte(`{"keda":{"operator":{"replicaCount":1}}}`)}
	keda := component("keda", "catalogs/cluster_base/keda", values)
	certManager := component("cert-manager", "catalogs/cluster_base/cert_manager", nil)

	applied, err := client.ApplyComponentSet(ctx, "base",
		[]cclusternvidiacomv1alpha1.ComponentSpec{keda, certManager})
	if err != nil {
		t.Fatalf("ApplyComponentSet() error = %v", err)
	}
	if applied.Namespace != client.Namespace() || applied.Generation != 1 {
		t.Errorf("ApplyComponentSet() returned %s/%s generation %d, want the stored object",
			applied.Namespace, applied.Name, applied.Generation)
	}
	if !reflect.DeepEqual(applied.Spec.Components, []cclusternvidiacomv1alpha1.ComponentSpec{keda, certManager}) {
		t.Errorf("components = %+v, want keda and cert-manager as given", applied.Spec.Components)
	}

	// A shorter list replaces the stored one: the field manager owns the whole list.
	if _, err := client.ApplyComponentSet(ctx, "base",
		[]cclusternvidiacomv1alpha1.ComponentSpec{certManager}); err != nil {
		t.Fatalf("second ApplyComponentSet() error = %v", err)
	}
	got, err := client.GetComponentSet(ctx, "base")
	if err != nil {
		t.Fatalf("GetComponentSet() error = %v", err)
	}
	if !reflect.DeepEqual(got.Spec.Components, []cclusternvidiacomv1alpha1.ComponentSpec{certManager}) {
		t.Errorf("components = %+v, want only cert-manager", got.Spec.Components)
	}

	if err := client.DeleteComponentSet(ctx, "base"); err != nil {
		t.Fatalf("DeleteComponentSet() error = %v", err)
	}
	if _, err := client.GetComponentSet(ctx, "base"); !apierrors.IsNotFound(err) {
		t.Errorf("GetComponentSet() after delete: error = %v, want NotFound", err)
	}
}

func TestIsComponentSetFinished(t *testing.T) {
	finished := map[string]metav1.ConditionStatus{
		cclusternvidiacomv1alpha1.CCLComponentSetConditionValidated:         metav1.ConditionTrue,
		cclusternvidiacomv1alpha1.CCLComponentSetConditionComponentsApplied: metav1.ConditionTrue,
		cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing:       metav1.ConditionFalse,
	}
	tests := []struct {
		name string
		// conditions are written with the set's current generation, except the types
		// in stale, which carry the one before it.
		conditions map[string]metav1.ConditionStatus
		stale      []string
		deleting   bool
		want       bool
	}{
		{name: "no status yet", want: false},
		{name: "converged", conditions: finished, want: true},
		{
			name: "still rolling out",
			conditions: map[string]metav1.ConditionStatus{
				cclusternvidiacomv1alpha1.CCLComponentSetConditionValidated:         metav1.ConditionTrue,
				cclusternvidiacomv1alpha1.CCLComponentSetConditionComponentsApplied: metav1.ConditionTrue,
				cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing:       metav1.ConditionTrue,
			},
			want: false,
		},
		{
			name: "validation failed",
			conditions: map[string]metav1.ConditionStatus{
				cclusternvidiacomv1alpha1.CCLComponentSetConditionValidated: metav1.ConditionFalse,
			},
			want: false,
		},
		{
			// After a failed validation the other types keep the previous generation's
			// verdict, which says nothing about the spec as it now stands.
			name:       "converged only for the previous generation",
			conditions: finished,
			stale: []string{
				cclusternvidiacomv1alpha1.CCLComponentSetConditionComponentsApplied,
				cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing,
			},
			want: false,
		},
		{name: "being deleted", conditions: finished, deleting: true, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			client := newClient(t)
			set := applyComponentSetWithStatus(t, client, test.conditions, test.stale)
			if test.deleting {
				// A finalizer nobody removes keeps the set in Terminating.
				set.Finalizers = []string{"ccluster.nvidia.com/test"}
				if _, err := clientset.CclusterV1alpha1().CCLComponentSets(client.Namespace()).
					Update(ctx, set, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
				if err := client.DeleteComponentSet(ctx, set.Name); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { removeFinalizers(t, client, set.Name) })
			}

			got, err := client.IsComponentSetFinished(ctx, set.Name)
			if err != nil {
				t.Fatalf("IsComponentSetFinished() error = %v", err)
			}
			if got != test.want {
				t.Errorf("IsComponentSetFinished() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestIsComponentSetFinishedReportsAMissingSet(t *testing.T) {
	client := newClient(t)
	if _, err := client.IsComponentSetFinished(t.Context(), "missing"); !apierrors.IsNotFound(err) {
		t.Errorf("IsComponentSetFinished() error = %v, want NotFound", err)
	}
}

func TestComponentSetCurrentConditionsDropsStaleTypes(t *testing.T) {
	set := &cclusternvidiacomv1alpha1.CCLComponentSet{
		ObjectMeta: metav1.ObjectMeta{Generation: 3},
		Status: cclusternvidiacomv1alpha1.CCLComponentSetStatus{Conditions: []metav1.Condition{
			{Type: cclusternvidiacomv1alpha1.CCLComponentSetConditionValidated, Status: metav1.ConditionFalse,
				Reason: cclusternvidiacomv1alpha1.CCLComponentSetReasonInfraPlaneNotFound, ObservedGeneration: 3},
			{Type: cclusternvidiacomv1alpha1.CCLComponentSetConditionComponentsApplied, Status: metav1.ConditionTrue,
				Reason: cclusternvidiacomv1alpha1.CCLComponentSetReasonApplicationsCreated, ObservedGeneration: 2},
		}},
	}

	got := cclclient.ComponentSetCurrentConditions(set)
	validated := got[cclusternvidiacomv1alpha1.CCLComponentSetConditionValidated]
	if len(got) != 1 || validated.Reason != cclusternvidiacomv1alpha1.CCLComponentSetReasonInfraPlaneNotFound {
		t.Errorf("ComponentSetCurrentConditions() = %v, want only Validated", got)
	}
}

// newClient returns a client for a new namespace of its own.
func newClient(t *testing.T) *cclclient.Client {
	t.Helper()

	client := cclclient.New(uniqueName(t, "tenant"), kubeClient, clientset)
	if err := client.EnsureNamespace(t.Context()); err != nil {
		t.Fatal(err)
	}
	return client
}

// uniqueName returns a namespace name of prefix's no other test uses: envtest runs no
// namespace controller, so a namespace is never really deleted and each test needs its own.
func uniqueName(t *testing.T, prefix string) string {
	t.Helper()

	return prefix + "-" + rand.String(8)
}

func component(
	name, chart string,
	values *apiextensionsv1.JSON,
) cclusternvidiacomv1alpha1.ComponentSpec {
	return cclusternvidiacomv1alpha1.ComponentSpec{
		Name:          name,
		InfraPlaneRef: cclusternvidiacomv1alpha1.LocalObjectReference{Name: "gpu-a"},
		ComponentEntry: cclusternvidiacomv1alpha1.ComponentEntry{
			Chart: chart, Namespace: name, Values: values,
		},
	}
}

// applyComponentSetWithStatus applies a one-component set and writes conditions to its
// status as the operator would, under a field manager of its own.
func applyComponentSetWithStatus(
	t *testing.T,
	client *cclclient.Client,
	conditions map[string]metav1.ConditionStatus,
	stale []string,
) *cclusternvidiacomv1alpha1.CCLComponentSet {
	t.Helper()
	ctx := t.Context()

	set, err := client.ApplyComponentSet(ctx, "base", []cclusternvidiacomv1alpha1.ComponentSpec{
		component("keda", "catalogs/cluster_base/keda", nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(conditions) == 0 {
		return set
	}

	status := cclusterac.CCLComponentSetStatus()
	for conditionType, conditionStatus := range conditions {
		generation := set.Generation
		for _, s := range stale {
			if s == conditionType {
				generation--
			}
		}
		status.WithConditions(metav1ac.Condition().
			WithType(conditionType).WithStatus(conditionStatus).WithReason("Test").
			WithMessage("").WithLastTransitionTime(metav1.Now()).WithObservedGeneration(generation))
	}
	set, err = clientset.CclusterV1alpha1().CCLComponentSets(client.Namespace()).ApplyStatus(ctx,
		cclusterac.CCLComponentSet(set.Name, client.Namespace()).WithStatus(status),
		metav1.ApplyOptions{FieldManager: "operator-test", Force: true})
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func removeFinalizers(t *testing.T, client *cclclient.Client, name string) {
	t.Helper()

	sets := clientset.CclusterV1alpha1().CCLComponentSets(client.Namespace())
	// t.Context is already cancelled when cleanups run.
	ctx := context.Background()
	set, err := sets.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	set.Finalizers = nil
	if _, err := sets.Update(ctx, set, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func credentialsSecret(t *testing.T, client *cclclient.Client, plane string) *corev1.Secret {
	t.Helper()

	secret, err := kubeClient.CoreV1().Secrets(client.Namespace()).Get(t.Context(),
		cclclient.CredentialsSecretName(plane), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

func clusterConfig(t *testing.T, secret *corev1.Secret) map[string]any {
	t.Helper()

	var config map[string]any
	if err := json.Unmarshal(secret.Data["config"], &config); err != nil {
		t.Fatalf("config %q: %v", secret.Data["config"], err)
	}
	return config
}
