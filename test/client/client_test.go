/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package client_test runs the generated client in pkg/client against a real API server
// serving the ccluster CRDs. The generated code itself is not under test; what is, is what
// it is generated from: the group name, the scope and status subresource of each kind, and
// the SchemeGroupVersion and Resource it takes from the API package.
package client_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	cclusterac "github.com/NVIDIA/ccluster/pkg/client/applyconfiguration/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/pkg/client/clientset/versioned"
	"github.com/NVIDIA/ccluster/pkg/client/clientset/versioned/fake"
	"github.com/NVIDIA/ccluster/pkg/client/informers/externalversions"
)

var (
	clientset  versioned.Interface
	kubeClient kubernetes.Interface
)

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

// CCLClusterConfig is cluster-scoped and has a status subresource: the client reaches it
// without a namespace, and applies its spec and its status separately.
func TestClusterScopedKindAppliesSpecAndStatus(t *testing.T) {
	ctx := t.Context()
	configs := clientset.CclusterV1alpha1().CCLClusterConfigs()
	apply := metav1.ApplyOptions{FieldManager: "client-test", Force: true}
	name := cclusternvidiacomv1alpha1.CCLClusterConfigName
	t.Cleanup(func() { deleteClusterConfig(t) })

	if _, err := configs.Apply(ctx, cclusterac.CCLClusterConfig(name).
		WithSpec(cclusterac.CCLClusterConfigSpec().WithDomain("gpu-a.example.com")), apply); err != nil {
		t.Fatal(err)
	}
	if _, err := configs.ApplyStatus(ctx, cclusterac.CCLClusterConfig(name).
		WithStatus(cclusterac.CCLClusterConfigStatus().WithConditions(metav1ac.Condition().
			WithType("Verified").WithStatus(metav1.ConditionTrue).WithReason("ComponentsVerified").
			WithMessage("").WithLastTransitionTime(metav1.Now()))), apply); err != nil {
		t.Fatal(err)
	}

	got, err := configs.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.Domain != "gpu-a.example.com" {
		t.Errorf("spec.domain = %q, want gpu-a.example.com", got.Spec.Domain)
	}
	if !apimeta.IsStatusConditionTrue(got.Status.Conditions, "Verified") {
		t.Errorf("status.conditions = %v, want Verified=True", got.Status.Conditions)
	}

	// Extract reads the server's managed fields with the schema built from the CRDs, so it
	// returns what the field manager applied.
	owned, err := cclusterac.ExtractCCLClusterConfig(got, apply.FieldManager)
	if err != nil {
		t.Fatal(err)
	}
	if owned.Spec == nil || owned.Spec.Domain == nil || *owned.Spec.Domain != "gpu-a.example.com" {
		t.Errorf("extracted spec = %+v, want domain gpu-a.example.com", owned.Spec)
	}
}

// CCLInfraPlane and CCLComponentSet are namespaced: what the client writes in one
// namespace it lists there and nowhere else.
func TestNamespacedKindsStayInTheirNamespace(t *testing.T) {
	ctx := t.Context()
	client := clientset.CclusterV1alpha1()
	team, other := createNamespace(t, "team-a"), createNamespace(t, "team-b")

	if _, err := client.CCLInfraPlanes(team).Create(ctx, &cclusternvidiacomv1alpha1.CCLInfraPlane{
		ObjectMeta: metav1.ObjectMeta{Name: "gpu-a"},
		Spec: cclusternvidiacomv1alpha1.CCLInfraPlaneSpec{
			ClusterCredentialsRef: cclusternvidiacomv1alpha1.LocalObjectReference{Name: "gpu-a-credentials"},
		},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CCLComponentSets(team).Create(ctx, &cclusternvidiacomv1alpha1.CCLComponentSet{
		ObjectMeta: metav1.ObjectMeta{Name: "base"},
		Spec: cclusternvidiacomv1alpha1.CCLComponentSetSpec{
			Components: []cclusternvidiacomv1alpha1.ComponentSpec{{
				Name:          "cert-manager",
				InfraPlaneRef: cclusternvidiacomv1alpha1.LocalObjectReference{Name: "gpu-a"},
				ComponentEntry: cclusternvidiacomv1alpha1.ComponentEntry{
					Chart: "catalogs/cluster_base/cert_manager", Namespace: "cert-manager",
				},
			}},
		},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	for namespace, want := range map[string]int{team: 1, other: 0} {
		planes, err := client.CCLInfraPlanes(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		sets, err := client.CCLComponentSets(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if len(planes.Items) != want || len(sets.Items) != want {
			t.Errorf("namespace %s holds %d CCLInfraPlanes and %d CCLComponentSets, want %d of each",
				namespace, len(planes.Items), len(sets.Items), want)
		}
	}
}

// The informer factory, narrowed to one object by a field selector the way an operator
// watches a singleton, delivers changes to its handlers and serves reads from its lister.
func TestInformerFactoryWatchesAndListsASingleton(t *testing.T) {
	ctx := t.Context()
	name := cclusternvidiacomv1alpha1.CCLClusterConfigName

	factory := externalversions.NewSharedInformerFactoryWithOptions(clientset, 0,
		externalversions.WithTweakListOptions(func(options *metav1.ListOptions) {
			options.FieldSelector = fields.OneTermEqualSelector("metadata.name", name).String()
		}))
	informer := factory.Ccluster().V1alpha1().CCLClusterConfigs()
	events := make(chan string, 16)
	if _, err := informer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { events <- "add" },
		UpdateFunc: func(any, any) { events <- "update" },
		DeleteFunc: func(any) { events <- "delete" },
	}); err != nil {
		t.Fatal(err)
	}
	factory.Start(ctx.Done())
	// Runs after t.Context is cancelled, which stops the informers; Shutdown waits for them.
	t.Cleanup(factory.Shutdown)
	for informerType, synced := range factory.WaitForCacheSync(ctx.Done()) {
		if !synced {
			t.Fatalf("%v did not sync", informerType)
		}
	}

	// A missing object is NotFound in the ccluster.nvidia.com group.
	_, err := informer.Lister().Get(name)
	var status apierrors.APIStatus
	if !apierrors.IsNotFound(err) || !errors.As(err, &status) ||
		status.Status().Details.Group != cclusternvidiacomv1alpha1.GroupVersion.Group {
		t.Fatalf("Get of a missing CCLClusterConfig returned %v, want NotFound in group %s",
			err, cclusternvidiacomv1alpha1.GroupVersion.Group)
	}

	// Its creation is delivered, and the lister serves it.
	t.Cleanup(func() { deleteClusterConfig(t) })
	configs := clientset.CclusterV1alpha1().CCLClusterConfigs()
	if _, err := configs.Create(ctx, &cclusternvidiacomv1alpha1.CCLClusterConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       cclusternvidiacomv1alpha1.CCLClusterConfigSpec{Domain: "gpu-a.example.com"},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	expectEvent(t, events, "add")
	cached, err := informer.Lister().Get(name)
	if err != nil {
		t.Fatal(err)
	}
	if cached.Spec.Domain != "gpu-a.example.com" {
		t.Errorf("cached spec.domain = %q, want gpu-a.example.com", cached.Spec.Domain)
	}

	// Its deletion is delivered.
	deleteClusterConfig(t)
	expectEvent(t, events, "delete")
}

// The fake clientset tracks field ownership for the API types, so programs can create,
// update and apply them in tests without a server. Each write goes through the schema in
// pkg/client; a kind missing from it fails every write.
func TestFakeClientsetWritesTheAPITypes(t *testing.T) {
	ctx := t.Context()
	client := fake.NewClientset(&cclusternvidiacomv1alpha1.CCLInfraPlane{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "gpu-a"},
	}).CclusterV1alpha1()

	plane, err := client.CCLInfraPlanes("team-a").Get(ctx, "gpu-a", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	plane.Status.ObservedGeneration = 1
	if _, err := client.CCLInfraPlanes("team-a").UpdateStatus(ctx, plane, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}

	if _, err := client.CCLClusterConfigs().Create(ctx, &cclusternvidiacomv1alpha1.CCLClusterConfig{
		ObjectMeta: metav1.ObjectMeta{Name: cclusternvidiacomv1alpha1.CCLClusterConfigName},
		Spec:       cclusternvidiacomv1alpha1.CCLClusterConfigSpec{Domain: "gpu-a.example.com"},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	// values has no schema of its own, so this also covers a field the schema leaves untyped.
	set, err := client.CCLComponentSets("team-a").Apply(ctx, cclusterac.CCLComponentSet("base", "team-a").
		WithSpec(cclusterac.CCLComponentSetSpec().WithComponents(cclusterac.ComponentSpec().
			WithName("cert-manager").
			WithInfraPlaneRef(cclusterac.LocalObjectReference().WithName("gpu-a")).
			WithChart("catalogs/cluster_base/cert_manager").WithNamespace("cert-manager").
			WithValues(apiextensionsv1.JSON{Raw: []byte(`{"replicaCount":2}`)}))),
		metav1.ApplyOptions{FieldManager: "client-test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Spec.Components) != 1 || set.Spec.Components[0].Values == nil ||
		string(set.Spec.Components[0].Values.Raw) != `{"replicaCount":2}` {
		t.Errorf("applied spec.components = %+v, want cert-manager with replicaCount 2", set.Spec.Components)
	}
}

// createNamespace creates a namespace named after prefix and returns the name the server
// gave it. The name is unique because envtest runs no namespace controller, so a
// namespace is never deleted and a fixed name would fail the next run in the same process.
func createNamespace(t *testing.T, prefix string) string {
	t.Helper()

	namespace, err := kubeClient.CoreV1().Namespaces().Create(t.Context(),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: prefix + "-"}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return namespace.Name
}

// deleteClusterConfig deletes the CCLClusterConfig singleton, if there is one.
func deleteClusterConfig(t *testing.T) {
	t.Helper()

	err := clientset.CclusterV1alpha1().CCLClusterConfigs().Delete(context.Background(),
		cclusternvidiacomv1alpha1.CCLClusterConfigName, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
}

// expectEvent waits for the next event a handler recorded and fails unless it is want.
func expectEvent(t *testing.T, events <-chan string, want string) {
	t.Helper()

	select {
	case got := <-events:
		if got != want {
			t.Fatalf("got a %s event, want %s", got, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("no %s event within 10s", want)
	}
}
