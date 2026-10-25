/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package remote

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// SyncTimeout bounds how long a new watch may take to finish its first list before it
// is given up on, and restarted by the next Ensure.
const SyncTimeout = 30 * time.Second

// activeWatches counts the infra-plane clusters currently watched.
var activeWatches = prometheus.NewGauge(prometheus.GaugeOpts{
	Name: "ccluster_remote_watches",
	Help: "Number of infra-plane clusters the control-plane operator is watching.",
})

func init() {
	metrics.Registry.MustRegister(activeWatches)
}

// Watcher watches one named, cluster-scoped object on each infra-plane cluster and
// enqueues the CCLInfraPlane that registered the cluster whenever it changes.
//
// A watch is only an event source. It never blocks or fails a reconcile: what the
// operator reports about a cluster comes from reading and writing it directly.
type Watcher interface {
	// Ensure starts watching the cluster secret describes on behalf of plane, unless
	// it is already watched with the same Secret. A Secret that changed since the
	// watch started replaces it. It returns at once; a watch that cannot sync within
	// SyncTimeout is logged and dropped, and the next Ensure starts it again.
	Ensure(ctx context.Context, plane types.NamespacedName, secret *corev1.Secret)

	// Stop stops watching on behalf of plane. It is a no-op if nothing is watched.
	Stop(plane types.NamespacedName)

	// Source delivers the Watcher's events to a controller's workqueue.
	Source() source.TypedSource[reconcile.Request]
}

// WatchOptions configures a Registry.
type WatchOptions struct {
	// Local is the operator's own rest.Config, for a Secret that registers the
	// operator's own cluster.
	Local *rest.Config

	// Scheme maps Object's type.
	Scheme *runtime.Scheme

	// Object is the type to watch, and Name the one object of it that matters.
	Object client.Object
	Name   string
}

// Registry is the Watcher the operator runs with: one controller-runtime cache per
// infra-plane cluster, holding just the named object.
//
// Its events reach the controller through one source registered at startup, so watches
// come and go without the controller ever adding or removing a watch of its own. It is
// a manager Runnable only so that it stops every cache when the manager stops; watches
// start from Ensure, which only the elected leader's reconciler calls.
//
// Its methods are safe for concurrent use, and a slow or unreachable cluster delays
// nothing but its own watch.
type Registry struct {
	opts WatchOptions

	mu      sync.Mutex
	watches map[types.NamespacedName]*watch
	queue   workqueue.TypedRateLimitingInterface[reconcile.Request]
}

// watch is one infra-plane cluster's cache.
type watch struct {
	// secret identifies the credentials the watch was started with.
	secret secretVersion
	cancel context.CancelFunc
	// wg tracks the goroutines that start the watch and run its cache.
	wg sync.WaitGroup
}

// secretVersion identifies one revision of one credentials Secret.
type secretVersion struct {
	uid             types.UID
	resourceVersion string
}

// NewRegistry returns a Registry with no watches.
func NewRegistry(opts WatchOptions) *Registry {
	return &Registry{opts: opts, watches: map[types.NamespacedName]*watch{}}
}

// Source returns the source that hands the Registry its controller's workqueue.
func (r *Registry) Source() source.TypedSource[reconcile.Request] {
	return source.TypedFunc[reconcile.Request](func(_ context.Context, queue workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.queue = queue
		return nil
	})
}

// enqueue adds plane to the controller's workqueue. Before the controller has started
// there is no queue, and nothing is lost: starting it reconciles every CCLInfraPlane.
func (r *Registry) enqueue(plane types.NamespacedName) {
	r.mu.Lock()
	queue := r.queue
	r.mu.Unlock()
	if queue != nil {
		queue.Add(reconcile.Request{NamespacedName: plane})
	}
}

// Ensure implements Watcher.
func (r *Registry) Ensure(ctx context.Context, plane types.NamespacedName, secret *corev1.Secret) {
	version := secretVersion{uid: secret.UID, resourceVersion: secret.ResourceVersion}
	log := logf.FromContext(ctx).WithValues("secret", secret.Name)

	r.mu.Lock()
	replaced, hadOne := r.watches[plane]
	if hadOne && replaced.secret == version {
		r.mu.Unlock()
		return
	}
	started := &watch{secret: version}
	watchCtx, cancel := context.WithCancel(context.Background())
	started.cancel = cancel
	// Counted before the watch is visible, so a Stop that finds it waits for it.
	started.wg.Add(1)
	r.watches[plane] = started
	r.mu.Unlock()

	if hadOne {
		replaced.stop()
		log.Info("Replaced watch on infra-plane cluster")
	} else {
		activeWatches.Inc()
		log.Info("Started watch on infra-plane cluster")
	}

	secret = secret.DeepCopy()
	go func() {
		defer started.wg.Done()
		err := r.run(watchCtx, plane, secret, started)
		if err == nil {
			return
		}
		// Checked before cancelling: a cancelled context means Stop or a replacement
		// ended the watch, which is not a failure.
		stopped := watchCtx.Err() != nil
		cancel()
		if !stopped {
			log.Info("Failed to start watch on infra-plane cluster, dropped it", "reason", err.Error())
			r.drop(plane, started)
		}
	}()
}

// run builds the cache for the cluster secret describes, starts it, and waits for its
// first list, then enqueues plane on every event on the watched object. It returns an
// error if the watch never synced; the caller then cancels ctx, which stops the cache.
func (r *Registry) run(
	ctx context.Context,
	plane types.NamespacedName,
	secret *corev1.Secret,
	w *watch,
) error {
	restConfig, err := RESTConfig(secret, r.opts.Local)
	if err != nil {
		return err
	}

	// Discovery is bounded by RequestTimeout; the watch itself is not, or every watch
	// connection would be cut after RequestTimeout.
	httpClient, err := rest.HTTPClientFor(restConfig)
	if err != nil {
		return err
	}
	mapper, err := apiutil.NewDynamicRESTMapper(restConfig, httpClient)
	if err != nil {
		return err
	}
	watchConfig := rest.CopyConfig(restConfig)
	watchConfig.Timeout = 0

	log := logf.Log.WithName("remote").WithValues("CCLInfraPlane", plane.String())
	remoteCache, err := cache.New(watchConfig, cache.Options{
		Scheme: r.opts.Scheme,
		Mapper: mapper,
		ByObject: map[client.Object]cache.ByObject{
			r.opts.Object: {Field: fields.OneTermEqualSelector("metadata.name", r.opts.Name)},
		},
		// The informer backs off and retries by itself. Reconciling the plane is what
		// turns a lost connection into a condition: its write to the cluster fails.
		DefaultWatchErrorHandler: func(_ context.Context, _ *toolscache.Reflector, err error) {
			log.Info("Lost watch on infra-plane cluster, retrying", "reason", err.Error())
			r.enqueue(plane)
		},
	})
	if err != nil {
		return err
	}

	w.wg.Go(func() {
		if err := remoteCache.Start(ctx); err != nil {
			log.Error(err, "Failed to run watch on infra-plane cluster")
		}
	})

	syncCtx, cancelSync := context.WithTimeout(ctx, SyncTimeout)
	defer cancelSync()
	informer, err := remoteCache.GetInformer(syncCtx, r.opts.Object)
	if err != nil {
		return err
	}
	// GetInformer waits for the sync only once the cache has started, and Start runs in
	// the goroutine above, so it can return before the first list. Waiting here makes a
	// cluster that answers discovery but not the list - no list or watch permission, or
	// a list that hangs - fail within SyncTimeout instead of retrying forever.
	if !remoteCache.WaitForCacheSync(syncCtx) {
		return errors.New("watch did not sync within " + SyncTimeout.String())
	}
	_, err = informer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { r.enqueue(plane) },
		UpdateFunc: func(any, any) { r.enqueue(plane) },
		DeleteFunc: func(any) { r.enqueue(plane) },
	})
	return err
}

// drop forgets w, if it is still plane's watch, so the next Ensure starts a new one.
func (r *Registry) drop(plane types.NamespacedName, w *watch) {
	r.mu.Lock()
	current, ok := r.watches[plane]
	if ok && current == w {
		delete(r.watches, plane)
		activeWatches.Dec()
	}
	r.mu.Unlock()
}

// stop cancels the watch and waits for its goroutines to finish, so no event from it
// arrives afterwards and nothing outlives it.
func (w *watch) stop() {
	w.cancel()
	w.wg.Wait()
}

// Stop implements Watcher.
func (r *Registry) Stop(plane types.NamespacedName) {
	r.mu.Lock()
	existing, ok := r.watches[plane]
	delete(r.watches, plane)
	r.mu.Unlock()

	if ok {
		existing.stop()
		activeWatches.Dec()
		logf.Log.WithName("remote").Info("Stopped watch on infra-plane cluster",
			"CCLInfraPlane", plane.String())
	}
}

// Watched returns the planes currently watched, for tests and diagnostics.
func (r *Registry) Watched() sets.Set[types.NamespacedName] {
	r.mu.Lock()
	defer r.mu.Unlock()
	planes := sets.New[types.NamespacedName]()
	for plane := range r.watches {
		planes.Insert(plane)
	}
	return planes
}

// Start implements manager.Runnable: it waits for the manager to stop, then stops every
// watch.
func (r *Registry) Start(ctx context.Context) error {
	<-ctx.Done()
	for plane := range r.Watched() {
		r.Stop(plane)
	}
	return nil
}
