/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package ip

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	verifierregistry "github.com/NVIDIA/ccluster/internal/registry/verifier_registry"
)

// Re-verification intervals. Verifiers read state this operator does not write, so
// there is no event of its own to wait for; a periodic pass is what notices a component
// arriving or breaking. It is the one place in ccluster that polls.
const (
	// recheckWhileFailing is the interval while any component fails: the cluster is
	// being set up and the operator is what tells the user how far along it is.
	recheckWhileFailing = time.Minute
	// recheckWhenVerified is the interval once everything passes.
	recheckWhenVerified = 5 * time.Minute
)

// verifyTimeout bounds each Verifier call, so one check that hangs cannot hold up the
// pass and with it the status of every other component. It is shorter than
// recheckWhileFailing, so a pass always finishes before the next is due.
const verifyTimeout = 30 * time.Second

// CCLClusterConfigReconciler reconciles a CCLClusterConfig object
type CCLClusterConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Verifiers is the registry of checks the controller runs, normally
	// verifierregistry.Get(). Injectable so tests can drive the controller with a
	// registry of their own.
	Verifiers *verifierregistry.Registry
}

// RBAC is what the controller itself needs. A Verifier that reads a kind adds a marker
// for it here; controller-gen only reads markers in this package, not in the verifier
// registry. Everything stays read-only: the operator writes nothing but
// CCLClusterConfig status.
// +kubebuilder:rbac:groups=ccluster.nvidia.com,resources=cclclusterconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=ccluster.nvidia.com,resources=cclclusterconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch

// Reconcile runs every cluster_base component's Verifier against the CCLClusterConfig and
// records the outcome as its Verified condition, and reports the cluster's labelled
// LoadBalancers in status.loadBalancers.
//
// The operator reads the cluster and writes status; it changes nothing else. Whether
// the components were installed by the control plane through a CCLComponentSet or by a
// user with helm is invisible to it - the Verifiers look at live objects, never at Argo
// CD or Helm release state - so the same check holds in both cases. A Verifier's error
// is not a reconcile error: it is the answer, and it is reported rather than retried.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.1/pkg/reconcile
func (r *CCLClusterConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var config cclusternvidiacomv1alpha1.CCLClusterConfig
	if err := r.Get(ctx, req.NamespacedName, &config); err != nil {
		if apierrors.IsNotFound(err) {
			// Nothing to verify against; the operator is idle until the singleton exists.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	loadBalancers, conflicts, err := r.observeLoadBalancers(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !equality.Semantic.DeepEqual(loadBalancers, config.Status.LoadBalancers) {
		log.Info("Observed labelled LoadBalancer Services", "loadBalancers", loadBalancers)
	}

	total, failing := r.verify(ctx, config.Spec)

	verified := verifiedCondition(total, failing, conflicts)
	previous := apimeta.FindStatusCondition(config.Status.Conditions, cclusternvidiacomv1alpha1.CCLClusterConfigConditionVerified)
	if previous == nil || previous.Status != verified.Status || previous.Message != verified.Message {
		log.Info("Verified cluster_base components", "verified", total-len(failing),
			"total", total, "failing", describe(failing), "conflicts", conflicts)
	}

	if err := r.setStatus(ctx, &config, verified, loadBalancers); err != nil {
		return ctrl.Result{}, err
	}

	if len(failing) > 0 || len(conflicts) > 0 {
		return ctrl.Result{RequeueAfter: recheckWhileFailing}, nil
	}
	return ctrl.Result{RequeueAfter: recheckWhenVerified}, nil
}

// failure is a cluster_base chart whose Verifier did not pass, and why.
type failure struct {
	chart string
	err   error
}

// verify runs every registered Verifier against spec and returns how many there were
// and the charts whose Verifier returned an error, in registration order.
//
// The Verifiers run concurrently. Each is an independent set of reads through the
// manager's cached client, which is safe for concurrent use, so the pass takes as long
// as the slowest check rather than the sum of all of them. Results are collected by
// position in registration order so the failing list, and with it the condition message,
// is deterministic whatever order the goroutines finish in.
//
// Each Verifier gets its own deadline, and a panic in one is recovered: controller-runtime
// recovers panics in Reconcile, but not in goroutines Reconcile starts, so without it one
// faulty Verifier would crash the operator. Either way the chart is reported as failing,
// with the panic or the timeout as its reason.
func (r *CCLClusterConfigReconciler) verify(
	ctx context.Context,
	spec cclusternvidiacomv1alpha1.CCLClusterConfigSpec,
) (total int, failing []failure) {
	log := logf.FromContext(ctx)

	charts := r.Verifiers.Charts()
	errs := make([]error, len(charts))
	var wg sync.WaitGroup
	for i, chart := range charts {
		verify, _ := r.Verifiers.Lookup(chart)
		wg.Go(func() {
			defer func() {
				if p := recover(); p != nil {
					log.Error(fmt.Errorf("%v", p), "Recovered panic in cluster_base Verifier",
						"chart", chart, "stack", string(debug.Stack()))
					errs[i] = fmt.Errorf("verifier panicked: %v", p)
				}
			}()

			verifyCtx, cancel := context.WithTimeout(ctx, verifyTimeout)
			defer cancel()
			errs[i] = verify(verifyCtx, r, spec)
			if errs[i] != nil && errors.Is(verifyCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
				errs[i] = fmt.Errorf("timed out after %s: %w", verifyTimeout, errs[i])
			}
		})
	}
	wg.Wait()

	for i, chart := range charts {
		if errs[i] != nil {
			failing = append(failing, failure{chart: chart, err: errs[i]})
		}
	}
	return len(charts), failing
}

// verifiedCondition turns the verification outcome, and any LoadBalancer label values
// claimed by more than one Service, into the Verified condition.
func verifiedCondition(total int, failing []failure, conflicts []string) metav1.Condition {
	condition := metav1.Condition{Type: cclusternvidiacomv1alpha1.CCLClusterConfigConditionVerified, Status: metav1.ConditionFalse}
	switch {
	case len(failing) > 0:
		condition.Reason = cclusternvidiacomv1alpha1.CCLClusterConfigReasonComponentsNotVerified
		condition.Message = fmt.Sprintf("%d of %d cluster_base components verified; failing: %s",
			total-len(failing), total, strings.Join(describe(failing), "; "))
	case len(conflicts) > 0:
		condition.Reason = cclusternvidiacomv1alpha1.CCLClusterConfigReasonLoadBalancerConflict
		condition.Message = fmt.Sprintf("All %d cluster_base components are installed and configured", total)
	default:
		condition.Status = metav1.ConditionTrue
		condition.Reason = cclusternvidiacomv1alpha1.CCLClusterConfigReasonComponentsVerified
		condition.Message = fmt.Sprintf("All %d cluster_base components are installed and configured", total)
	}
	if len(conflicts) > 0 {
		condition.Message += "; conflicting LoadBalancers: " + strings.Join(conflicts, "; ")
	}
	return condition
}

// describe renders each failure as "chart (reason)". The separator between them is a
// semicolon, since a reason may itself contain commas.
func describe(failing []failure) []string {
	described := make([]string, len(failing))
	for i, f := range failing {
		described[i] = fmt.Sprintf("%s (%v)", f.chart, f.err)
	}
	return described
}

// observeLoadBalancers returns the address of every Service of type LoadBalancer labelled
// LabelLoadBalancer, one per label value and sorted by it, and describes each value more
// than one such Service carries.
//
// A chart opts a LoadBalancer in by labelling its Service, with a value of its choosing
// that the operator only reports: the catalog's ingress_nginx chart uses "ingress". The
// label, rather than the Service's
// name, identifies it: the name follows the Helm release, which differs between a
// CCLComponentSet and a helm install. Like a Verifier, it reads live Services and never
// Argo CD or Helm release state. A value claimed by two Services has no right answer, so
// it is left out and returned as a conflict for the Verified condition. A failed list is
// returned, so the pass is retried rather than reporting no LoadBalancers.
func (r *CCLClusterConfigReconciler) observeLoadBalancers(
	ctx context.Context,
) ([]cclusternvidiacomv1alpha1.LoadBalancerStatus, []string, error) {
	var services corev1.ServiceList
	if err := r.List(ctx, &services, client.HasLabels{cclusternvidiacomv1alpha1.LabelLoadBalancer}); err != nil {
		return nil, nil, fmt.Errorf("listing LoadBalancer Services: %w", err)
	}
	byName := map[string][]corev1.Service{}
	for _, service := range services.Items {
		name := service.Labels[cclusternvidiacomv1alpha1.LabelLoadBalancer]
		if service.Spec.Type == corev1.ServiceTypeLoadBalancer && name != "" {
			byName[name] = append(byName[name], service)
		}
	}

	// A cached list has no order of its own; sorting keeps the status and the Verified
	// message unchanged from one pass to the next.
	var loadBalancers []cclusternvidiacomv1alpha1.LoadBalancerStatus
	var conflicts []string
	for _, name := range slices.Sorted(maps.Keys(byName)) {
		claimants := byName[name]
		if len(claimants) > 1 {
			services := make([]string, len(claimants))
			for i, service := range claimants {
				services[i] = types.NamespacedName{Namespace: service.Namespace, Name: service.Name}.String()
			}
			slices.Sort(services)
			conflicts = append(conflicts, fmt.Sprintf("%q is on %s", name, strings.Join(services, ", ")))
			continue
		}
		loadBalancers = append(loadBalancers, loadBalancerStatus(name, claimants[0].Status.LoadBalancer.Ingress))
	}
	return loadBalancers, conflicts, nil
}

// loadBalancerStatus takes the first IP and the first hostname a LoadBalancer Service
// reports, as it reports them; both are empty until the cloud provider assigns an address.
// The cp operator checks them when it publishes the record and reports one it cannot use,
// so nothing is dropped here without a trace.
func loadBalancerStatus(name string, ingress []corev1.LoadBalancerIngress) cclusternvidiacomv1alpha1.LoadBalancerStatus {
	status := cclusternvidiacomv1alpha1.LoadBalancerStatus{Name: name}
	for _, address := range ingress {
		if status.IP == "" {
			status.IP = address.IP
		}
		if status.Hostname == "" {
			status.Hostname = address.Hostname
		}
	}
	return status
}

// setStatus sets the Verified condition, the LoadBalancers and the observed generation,
// then writes the status subresource.
//
// A plain merge patch, without an optimistic lock: this controller is the only writer of
// this status, so a racing write has nothing to clobber, and a result computed for an
// older spec carries that spec's observedGeneration, so it reads as stale until the next
// pass replaces it. An empty loadBalancers is written as a removal. NotFound is ignored:
// the object was deleted while it was being verified.
func (r *CCLClusterConfigReconciler) setStatus(
	ctx context.Context,
	config *cclusternvidiacomv1alpha1.CCLClusterConfig,
	verified metav1.Condition,
	loadBalancers []cclusternvidiacomv1alpha1.LoadBalancerStatus,
) error {
	base := config.DeepCopy()

	verified.ObservedGeneration = config.Generation
	apimeta.SetStatusCondition(&config.Status.Conditions, verified)
	config.Status.LoadBalancers = loadBalancers
	config.Status.ObservedGeneration = config.Generation

	return client.IgnoreNotFound(r.Status().Patch(ctx, config, client.MergeFrom(base)))
}

// SetupWithManager sets up the controller with the Manager.
//
// Verifiers read state the operator does not write, and the kinds they will read are
// installed by the very components being verified - a watch on a kind the API server
// does not serve keeps a controller from ever starting - so re-verification is periodic
// (Reconcile's RequeueAfter) rather than event-driven.
//
// Services are the exception: every API server serves them, so the labelled LoadBalancer
// Services are watched, and an address reaches status.loadBalancers as soon as the cloud
// provider assigns it. Only Services carrying LabelLoadBalancer are passed on, all to the
// singleton, and with no predicate on what an update changed, since the address arrives
// as a status update. An update passes when either side carries the label, so removing
// it clears the entry at once. The cache itself is not scoped: it holds every Service,
// as it holds every object of each kind read, so a Verifier reading a Service sees them
// all.
//
// CCLClusterConfig updates are filtered to spec changes. The controller's own status
// patch is an update too, and without the filter every patch would rerun every Verifier;
// the periodic pass already covers re-verification.
func (r *CCLClusterConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cclusternvidiacomv1alpha1.CCLClusterConfig{},
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&corev1.Service{},
			handler.EnqueueRequestsFromMapFunc(singletonForService),
			builder.WithPredicates(loadBalancerServices())).
		Named("cclclusterconfig").
		Complete(r)
}

// loadBalancerServices passes on events for Services carrying LabelLoadBalancer. An
// update is checked on both sides: filtering on the new Service alone would drop the
// update that removes the label.
func loadBalancerServices() predicate.Funcs {
	funcs := predicate.NewPredicateFuncs(isLoadBalancerService)
	funcs.UpdateFunc = func(e event.UpdateEvent) bool {
		return isLoadBalancerService(e.ObjectOld) || isLoadBalancerService(e.ObjectNew)
	}
	return funcs
}

// isLoadBalancerService is whether a Service carries LabelLoadBalancer, whatever its value.
func isLoadBalancerService(object client.Object) bool {
	_, ok := object.GetLabels()[cclusternvidiacomv1alpha1.LabelLoadBalancer]
	return ok
}

// singletonForService maps a labelled Service's event to the CCLClusterConfig singleton.
func singletonForService(context.Context, client.Object) []reconcile.Request {
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{Name: cclusternvidiacomv1alpha1.CCLClusterConfigName},
	}}
}
