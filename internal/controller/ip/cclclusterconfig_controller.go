/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package ip

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	ingressnginx "github.com/NVIDIA/ccluster/catalogs/cluster_base/ingress_nginx"
	verifierregistry "github.com/NVIDIA/ccluster/internal/registry/verifier_registry"
)

// Condition types and reasons reported on CCLClusterConfig.status.
const (
	// conditionTypeVerified reports whether every cluster_base component passed its
	// Verifier against the spec.
	conditionTypeVerified = "Verified"

	reasonComponentsVerified    = "ComponentsVerified"
	reasonComponentsNotVerified = "ComponentsNotVerified"

	// conditionTypeLoadBalancerReady reports the cluster's ingress LoadBalancer: the
	// Service of type LoadBalancer labelled ingressnginx.ServiceLabel=true. When Ready,
	// its message names the Service and its addresses, which the control plane
	// publishes the cluster's DNS from.
	conditionTypeLoadBalancerReady = "LoadBalancerReady"

	reasonLoadBalancerReady       = "Ready"
	reasonLoadBalancerPending     = "Pending"
	reasonLoadBalancerNotDeployed = "NotDeployed"
	reasonLoadBalancerAmbiguous   = "Ambiguous"
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
// records the outcome as its Verified condition, and reports the cluster's ingress
// LoadBalancer as its LoadBalancerReady condition.
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

	total, failing := r.verify(ctx, config.Spec)

	status, reason, message := verifiedCondition(total, failing)
	previous := apimeta.FindStatusCondition(config.Status.Conditions, conditionTypeVerified)
	if previous == nil || previous.Status != status || previous.Message != message {
		log.Info("Verified cluster_base components", "verified", total-len(failing),
			"total", total, "failing", describe(failing))
	}

	loadBalancer, err := r.observeLoadBalancer(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	previous = apimeta.FindStatusCondition(config.Status.Conditions, conditionTypeLoadBalancerReady)
	if previous == nil || previous.Status != loadBalancer.Status || previous.Message != loadBalancer.Message {
		log.Info("Observed ingress LoadBalancer", "reason", loadBalancer.Reason,
			"message", loadBalancer.Message)
	}

	verified := metav1.Condition{
		Type:    conditionTypeVerified,
		Status:  status,
		Reason:  reason,
		Message: message,
	}
	if err := r.setStatus(ctx, &config, verified, loadBalancer); err != nil {
		return ctrl.Result{}, err
	}

	if len(failing) > 0 {
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

// verifiedCondition turns the verification outcome into the Verified condition.
func verifiedCondition(total int, failing []failure) (metav1.ConditionStatus, string, string) {
	if len(failing) == 0 {
		return metav1.ConditionTrue, reasonComponentsVerified, fmt.Sprintf(
			"All %d cluster_base components are installed and configured", total)
	}
	return metav1.ConditionFalse, reasonComponentsNotVerified, fmt.Sprintf(
		"%d of %d cluster_base components verified; failing: %s",
		total-len(failing), total, strings.Join(describe(failing), "; "))
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

// observeLoadBalancer reports the cluster's ingress LoadBalancer as the LoadBalancerReady
// condition.
//
// The ingress is found by ingressnginx.ServiceLabel, which the catalog's ingress_nginx
// chart stamps on its controller Service, rather than by name or by the upstream chart's
// labels: the Service's name follows the Helm release, which differs between a
// CCLComponentSet and a helm install, and the upstream labels match any ingress-nginx.
// Like a Verifier, it reads the live Service and never Argo CD or Helm release state.
// Only Services of type LoadBalancer count. A failed list is returned, so the pass is
// retried, rather than reported as no LoadBalancer.
func (r *CCLClusterConfigReconciler) observeLoadBalancer(ctx context.Context) (metav1.Condition, error) {
	var services corev1.ServiceList
	if err := r.List(ctx, &services, client.MatchingLabels{ingressnginx.ServiceLabel: "true"}); err != nil {
		return metav1.Condition{}, fmt.Errorf("listing ingress Services: %w", err)
	}
	var loadBalancers []corev1.Service
	for _, service := range services.Items {
		if service.Spec.Type == corev1.ServiceTypeLoadBalancer {
			loadBalancers = append(loadBalancers, service)
		}
	}
	return loadBalancerCondition(loadBalancers), nil
}

// loadBalancerCondition turns the LoadBalancer Services labelled as the cluster's ingress
// into the LoadBalancerReady condition. Its reason and message are what the control plane
// reads, so they are built here and nowhere else.
func loadBalancerCondition(loadBalancers []corev1.Service) metav1.Condition {
	condition := metav1.Condition{Type: conditionTypeLoadBalancerReady, Status: metav1.ConditionFalse}
	switch len(loadBalancers) {
	case 0:
		condition.Reason = reasonLoadBalancerNotDeployed
		condition.Message = fmt.Sprintf("No Service of type LoadBalancer is labelled %s=true",
			ingressnginx.ServiceLabel)
	case 1:
		service := loadBalancers[0]
		name := types.NamespacedName{Namespace: service.Namespace, Name: service.Name}
		var addresses []string
		for _, ingress := range service.Status.LoadBalancer.Ingress {
			if ingress.IP != "" {
				addresses = append(addresses, ingress.IP)
			}
			if ingress.Hostname != "" {
				addresses = append(addresses, ingress.Hostname)
			}
		}
		if len(addresses) == 0 {
			condition.Reason = reasonLoadBalancerPending
			condition.Message = fmt.Sprintf("Service %s has no address yet", name)
			break
		}
		condition.Status = metav1.ConditionTrue
		condition.Reason = reasonLoadBalancerReady
		condition.Message = fmt.Sprintf("Service %s has addresses %s", name, strings.Join(addresses, ", "))
	default:
		names := make([]string, len(loadBalancers))
		for i, service := range loadBalancers {
			names[i] = types.NamespacedName{Namespace: service.Namespace, Name: service.Name}.String()
		}
		// A cached list has no order of its own; sorting keeps the message, and with it the
		// status, unchanged from one pass to the next.
		slices.Sort(names)
		condition.Reason = reasonLoadBalancerAmbiguous
		condition.Message = fmt.Sprintf("%d Services of type LoadBalancer are labelled %s=true: %s",
			len(loadBalancers), ingressnginx.ServiceLabel, strings.Join(names, ", "))
	}
	return condition
}

// setStatus sets the given conditions and the observed generation, then writes the status
// subresource.
//
// A plain merge patch, without an optimistic lock: this controller is the only writer of
// this status, so a racing write has nothing to clobber, and a result computed for an
// older spec carries that spec's observedGeneration, so it reads as stale until the next
// pass replaces it. NotFound is ignored: the object was deleted while it was being
// verified.
func (r *CCLClusterConfigReconciler) setStatus(
	ctx context.Context,
	config *cclusternvidiacomv1alpha1.CCLClusterConfig,
	conditions ...metav1.Condition,
) error {
	base := config.DeepCopy()

	for _, condition := range conditions {
		condition.ObservedGeneration = config.Generation
		apimeta.SetStatusCondition(&config.Status.Conditions, condition)
	}
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
// Services are the exception: every API server serves them, so the ingress LoadBalancer
// is watched, and its address reaches LoadBalancerReady as soon as the cloud provider
// assigns it. Only Services labelled ingressnginx.ServiceLabel=true are passed on, all to
// the singleton, and with no predicate on updates, since the address arrives as a status
// update. ServiceCacheSelector keeps the operator's cache to those Services.
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
			builder.WithPredicates(predicate.NewPredicateFuncs(isIngressService))).
		Named("cclclusterconfig").
		Complete(r)
}

// ServiceCacheSelector is the only Services the operator's cache holds: the ones labelled
// as the cluster's ingress. cmd/ccluster-ip-operator sets it as the manager's cache
// selector for Services.
var ServiceCacheSelector = labels.SelectorFromSet(labels.Set{ingressnginx.ServiceLabel: "true"})

// isIngressService is whether a Service is labelled as the cluster's ingress.
func isIngressService(object client.Object) bool {
	return ServiceCacheSelector.Matches(labels.Set(object.GetLabels()))
}

// singletonForService maps an ingress Service event to the CCLClusterConfig singleton.
func singletonForService(context.Context, client.Object) []reconcile.Request {
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{Name: cclusternvidiacomv1alpha1.CCLClusterConfigName},
	}}
}
