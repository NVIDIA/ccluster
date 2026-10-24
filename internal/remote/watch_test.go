/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package remote

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/goleak"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
)

// The registry is tested against a real API server: envtest plays the infra-plane
// cluster, reached through a credentials Secret with no server, which means the
// operator's own cluster.
var (
	testConfig *rest.Config
	testScheme = runtime.NewScheme()
	testClient client.Client
)

func TestMain(m *testing.M) {
	utilruntime.Must(clientgoscheme.AddToScheme(testScheme))
	utilruntime.Must(cclusternvidiacomv1alpha1.AddToScheme(testScheme))

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

	var err error
	if testConfig, err = env.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "starting envtest:", err)
		os.Exit(1)
	}
	if testClient, err = client.New(testConfig, client.Options{Scheme: testScheme}); err != nil {
		fmt.Fprintln(os.Stderr, "creating client:", err)
		os.Exit(1)
	}

	code := m.Run()
	if err := env.Stop(); err != nil {
		fmt.Fprintln(os.Stderr, "stopping envtest:", err)
	}
	os.Exit(code)
}

type testQueue = workqueue.TypedRateLimitingInterface[reconcile.Request]

// newRegistry returns a Registry for CCLClusterConfig/cluster on envtest, connected to a
// fresh queue, and stops its watches when the test ends.
func newRegistry(t *testing.T) (*Registry, testQueue) {
	t.Helper()

	registry := NewRegistry(WatchOptions{
		Local:  testConfig,
		Scheme: testScheme,
		Object: &cclusternvidiacomv1alpha1.CCLClusterConfig{},
		Name:   cclusternvidiacomv1alpha1.CCLClusterConfigName,
	})
	queue := workqueue.NewTypedRateLimitingQueue(
		workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
	if err := registry.Source().Start(context.Background(), queue); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for plane := range registry.Watched() {
			registry.Stop(plane)
		}
		queue.ShutDown()
	})
	return registry, queue
}

// localSecret is a credentials Secret for the operator's own cluster: envtest.
func localSecret(resourceVersion string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Namespace: "team-a", Name: "gpu-a-credentials", UID: "uid-1", ResourceVersion: resourceVersion,
	}}
}

// expectEnqueued waits for plane to be the next request on queue.
func expectEnqueued(t *testing.T, queue testQueue, plane types.NamespacedName) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for queue.Len() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%s was not enqueued", plane)
		}
		time.Sleep(10 * time.Millisecond)
	}
	request, _ := queue.Get()
	queue.Done(request)
	queue.Forget(request)
	if request.NamespacedName != plane {
		t.Fatalf("enqueued %s, want %s", request.NamespacedName, plane)
	}
}

// drain empties queue of whatever arrived within a short settling time.
func drain(queue testQueue) {
	time.Sleep(200 * time.Millisecond)
	for queue.Len() > 0 {
		request, _ := queue.Get()
		queue.Done(request)
		queue.Forget(request)
	}
}

// waitFor polls condition until it holds, failing the test after ten seconds.
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func deleteClusterConfig(t *testing.T) {
	t.Helper()

	err := testClient.Delete(context.Background(), &cclusternvidiacomv1alpha1.CCLClusterConfig{
		ObjectMeta: metav1.ObjectMeta{Name: cclusternvidiacomv1alpha1.CCLClusterConfigName},
	})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
}

func TestRegistryEnqueuesThePlaneOnEveryChange(t *testing.T) {
	ctx := context.Background()
	registry, queue := newRegistry(t)
	plane := types.NamespacedName{Namespace: "team-a", Name: "gpu-a"}
	t.Cleanup(func() { deleteClusterConfig(t) })

	registry.Ensure(ctx, plane, localSecret("1"))

	config := &cclusternvidiacomv1alpha1.CCLClusterConfig{
		ObjectMeta: metav1.ObjectMeta{Name: cclusternvidiacomv1alpha1.CCLClusterConfigName},
		Spec:       cclusternvidiacomv1alpha1.CCLClusterConfigSpec{Domain: "gpu-a.example.com"},
	}
	if err := testClient.Create(ctx, config); err != nil {
		t.Fatal(err)
	}
	expectEnqueued(t, queue, plane)

	t.Run("status written by the infra-plane operator", func(t *testing.T) {
		drain(queue)
		apimeta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
			Type: "Verified", Status: metav1.ConditionTrue, Reason: "ComponentsVerified",
		})
		if err := testClient.Status().Update(ctx, config); err != nil {
			t.Fatal(err)
		}
		expectEnqueued(t, queue, plane)
	})

	t.Run("an object with another name is ignored", func(t *testing.T) {
		drain(queue)
		other := &cclusternvidiacomv1alpha1.CCLClusterConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "not-the-singleton"},
			Spec:       cclusternvidiacomv1alpha1.CCLClusterConfigSpec{Domain: "x.example.com"},
		}
		// The CRD admits only "cluster", so the write fails; either way nothing may
		// arrive.
		_ = testClient.Create(ctx, other)
		time.Sleep(500 * time.Millisecond)
		if queue.Len() != 0 {
			t.Fatalf("enqueued %d requests for an object the watch should not see", queue.Len())
		}
	})

	t.Run("deletion", func(t *testing.T) {
		drain(queue)
		deleteClusterConfig(t)
		expectEnqueued(t, queue, plane)
	})
}

func TestRegistryEnsureIsIdempotentAndFollowsTheSecret(t *testing.T) {
	ctx := context.Background()
	registry, _ := newRegistry(t)
	plane := types.NamespacedName{Namespace: "team-a", Name: "gpu-a"}
	before := testutil.ToFloat64(activeWatches)

	registry.Ensure(ctx, plane, localSecret("1"))
	first := registry.watches[plane]
	registry.Ensure(ctx, plane, localSecret("1"))
	if registry.watches[plane] != first {
		t.Fatal("Ensure with the same Secret replaced the watch")
	}

	registry.Ensure(ctx, plane, localSecret("2"))
	second := registry.watches[plane]
	if second == first {
		t.Fatal("Ensure with a changed Secret kept the old watch")
	}
	// Ensure waits for the replaced watch to stop before returning.
	select {
	case <-waitDone(first):
	case <-time.After(time.Second):
		t.Fatal("the replaced watch is still running")
	}

	if got := testutil.ToFloat64(activeWatches) - before; got != 1 {
		t.Fatalf("active watches grew by %v, want 1", got)
	}

	registry.Stop(plane)
	if registry.Watched().Has(plane) {
		t.Fatal("Stop left the watch registered")
	}
	if got := testutil.ToFloat64(activeWatches) - before; got != 0 {
		t.Fatalf("active watches grew by %v after Stop, want 0", got)
	}
	registry.Stop(plane) // a no-op the second time
}

// waitDone returns a channel closed once w's goroutines have finished.
func waitDone(w *watch) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()
	return done
}

func TestRegistryDropsAWatchThatCannotStart(t *testing.T) {
	ctx := context.Background()
	registry, _ := newRegistry(t)
	healthy := types.NamespacedName{Namespace: "team-a", Name: "healthy"}
	unreachable := types.NamespacedName{Namespace: "team-a", Name: "unreachable"}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "unreachable-credentials", UID: "uid-2", ResourceVersion: "1"},
		Data: map[string][]byte{
			"server": []byte("https://127.0.0.1:1"),
			"config": []byte(`{"bearerToken":"t"}`),
		},
	}
	registry.Ensure(ctx, unreachable, secret)
	registry.Ensure(ctx, healthy, localSecret("1"))

	waitFor(t, "the unreachable watch to be dropped", func() bool {
		return !registry.Watched().Has(unreachable)
	})
	if !registry.Watched().Has(healthy) {
		t.Fatal("a failing cluster took down another plane's watch")
	}

	// The next Ensure, from the plane's next reconcile, starts it again.
	registry.Ensure(ctx, unreachable, secret)
	if !registry.Watched().Has(unreachable) {
		t.Fatal("Ensure did not restart a dropped watch")
	}
}

func TestRegistryStartStopsEveryWatchWhenTheManagerStops(t *testing.T) {
	registry, _ := newRegistry(t)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error)
	go func() { stopped <- registry.Start(ctx) }()

	for _, name := range []string{"a", "b"} {
		registry.Ensure(context.Background(), types.NamespacedName{Namespace: "team-a", Name: name}, localSecret("1"))
	}
	cancel()
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if n := registry.Watched().Len(); n != 0 {
		t.Fatalf("%d watches survived the manager stopping", n)
	}
}

// Watches come and go with CCLInfraPlanes for the life of the operator, so neither
// starting nor stopping one may leave a goroutine behind.
func TestRegistryLeaksNoGoroutines(t *testing.T) {
	ctx := context.Background()
	ignore := goleak.IgnoreCurrent()
	registry, queue := newRegistry(t)
	plane := types.NamespacedName{Namespace: "team-a", Name: "gpu-a"}
	t.Cleanup(func() { deleteClusterConfig(t) })

	if err := testClient.Create(ctx, &cclusternvidiacomv1alpha1.CCLClusterConfig{
		ObjectMeta: metav1.ObjectMeta{Name: cclusternvidiacomv1alpha1.CCLClusterConfigName},
		Spec:       cclusternvidiacomv1alpha1.CCLClusterConfigSpec{Domain: "gpu-a.example.com"},
	}); err != nil {
		t.Fatal(err)
	}

	for i := range 5 {
		registry.Ensure(ctx, plane, localSecret(fmt.Sprint(i)))
		// The add event for the existing object proves the watch synced.
		expectEnqueued(t, queue, plane)
		if i%2 == 1 {
			registry.Stop(plane)
		}
	}
	registry.Stop(plane)

	goleak.VerifyNone(t, ignore,
		// Pooled keep-alive connections to envtest, closed by the transport on its own
		// idle timeout; client-go shares transports between configs.
		goleak.IgnoreTopFunction("net/http.(*persistConn).readLoop"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).writeLoop"),
		goleak.IgnoreAnyFunction("k8s.io/client-go/util/workqueue.(*Typed[...]).updateUnfinishedWorkLoop"),
		goleak.IgnoreAnyFunction("k8s.io/client-go/util/workqueue.(*delayingType[...]).waitingLoop"),
	)
}
