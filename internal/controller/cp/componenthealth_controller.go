/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

import (
	"context"
	"maps"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	argocdv1alpha1 "github.com/NVIDIA/ccluster/internal/api/argocd/v1alpha1"
	healthregistry "github.com/NVIDIA/ccluster/internal/registry/health_registry"
)

const (
	// healthPollSettling and healthPollSettled are how often a CCLComponentSet's
	// components are judged again: sooner while any of them is still on its way to a
	// settled health, so a change is seen promptly, and less often once all have
	// settled.
	healthPollSettling = 30 * time.Second
	healthPollSettled  = time.Minute
)

// ComponentHealthReconciler reports each released component's health in
// status.componentHealthStatuses, and that of each component removed from the spec until
// its Application is gone. It is the only writer of that field.
//
// It is separate from CCLComponentSetReconciler because it polls: a Pod failing changes
// no Application field, so no watch would see it. The rollout controller stays watch
// driven, and judges health again for itself when it needs to (see finished). A write
// here is a CCLComponentSet update, which brings the rollout controller back to do so.
type ComponentHealthReconciler struct {
	client.Client

	// ArgoCD and Health are as on CCLComponentSetReconciler, and must be the same values,
	// so that the health reported here is the health the rollout gates on.
	ArgoCD ArgoCDServer
	Health *healthregistry.Registry

	// MaxConcurrentReconciles is how many CCLComponentSets are judged at once. Zero falls
	// back to the manager's controller default, as on CCLComponentSetReconciler.
	MaxConcurrentReconciles int

	// now is the clock; nil means time.Now. Tests replace it.
	now func() time.Time
}

// +kubebuilder:rbac:groups=ccluster.nvidia.com,resources=cclcomponentsets,verbs=get;list;watch
// +kubebuilder:rbac:groups=ccluster.nvidia.com,resources=cclcomponentsets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=argoproj.io,resources=applications,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile judges every released component of a CCLComponentSet. It writes their health
// only when it has changed, so polling alone writes nothing.
func (r *ComponentHealthReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var componentSet cclusternvidiacomv1alpha1.CCLComponentSet
	if err := r.Get(ctx, req.NamespacedName, &componentSet); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !componentSet.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	all, err := listApplications(ctx, r.Client, &componentSet)
	if err != nil {
		return ctrl.Result{}, err
	}
	// A component without an Application is not released: nothing runs to judge. A
	// removed component's objects run until Argo CD has uninstalled them, so it is judged
	// while its Application is terminating too.
	applications := ownedApplications(&componentSet, liveApplications(all))
	prunable := prunableApplications(&componentSet, all)
	maps.Copy(applications, prunable)
	healths := judgeAll(ctx, r.ArgoCD, r.Health, &componentSet, applications)

	statuses := r.healthStatuses(&componentSet, healths, prunable)
	if !sameHealthStatuses(statuses, componentSet.Status.ComponentHealthStatuses) {
		base := componentSet.DeepCopy()
		componentSet.Status.ComponentHealthStatuses = statuses
		if err := r.Status().Patch(ctx, &componentSet, client.MergeFrom(base)); err != nil {
			// A missing object has no status left to record.
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	}

	return ctrl.Result{RequeueAfter: pollInterval(statuses)}, nil
}

// healthStatuses builds status.componentHealthStatuses from this pass's healths, in the
// order of componentSyncStatuses: the components in the spec in spec order, then the
// removed ones in pruneOrder. A component whose health is unchanged keeps its
// lastTransitionTime.
func (r *ComponentHealthReconciler) healthStatuses(
	componentSet *cclusternvidiacomv1alpha1.CCLComponentSet,
	healths componentHealths,
	prunable map[string]*argocdv1alpha1.Application,
) []cclusternvidiacomv1alpha1.ComponentHealthStatus {
	previous := make(map[string]cclusternvidiacomv1alpha1.ComponentHealthStatus,
		len(componentSet.Status.ComponentHealthStatuses))
	for _, status := range componentSet.Status.ComponentHealthStatuses {
		previous[status.Name] = status
	}

	names := make([]string, 0, len(componentSet.Spec.Components)+len(prunable))
	for _, component := range componentSet.Spec.Components {
		names = append(names, component.Name)
	}
	for _, name := range pruneOrder(prunable) {
		names = append(names, componentName(componentSet.Name, name))
	}

	now := metav1.NewTime(r.clock())
	var statuses []cclusternvidiacomv1alpha1.ComponentHealthStatus
	for _, name := range names {
		health, judged := healths[name]
		if !judged {
			continue
		}
		status := cclusternvidiacomv1alpha1.ComponentHealthStatus{
			Name:               name,
			Health:             health.Status,
			Reason:             health.Reason,
			Message:            health.Message,
			LastTransitionTime: now,
		}
		if before, ok := previous[name]; ok && before.Health == health.Status {
			status.LastTransitionTime = before.LastTransitionTime
		}
		statuses = append(statuses, status)
	}
	return statuses
}

// sameHealthStatuses reports whether writing a over b would change nothing. An empty
// list and a missing one are the same.
func sameHealthStatuses(a, b []cclusternvidiacomv1alpha1.ComponentHealthStatus) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return equality.Semantic.DeepEqual(a, b)
}

// pollInterval is how long until the components are judged again: soon while any has a
// health expected to change, less often once all have settled.
func pollInterval(statuses []cclusternvidiacomv1alpha1.ComponentHealthStatus) time.Duration {
	for _, s := range statuses {
		if !settled(s.Health) {
			return healthPollSettling
		}
	}
	return healthPollSettled
}

func (r *ComponentHealthReconciler) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// SetupWithManager sets up the controller with the Manager.
//
// Only spec changes trigger it among CCLComponentSet events: its own status writes, and
// the rollout controller's, would otherwise judge every component again on each. An
// Application event does when it changes a field the operator decodes (applicationChanged),
// so a component is judged as soon as Argo CD reports a change on it.
func (r *ComponentHealthReconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr).
		For(&cclusternvidiacomv1alpha1.CCLComponentSet{},
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		WithOptions(controller.Options{MaxConcurrentReconciles: r.MaxConcurrentReconciles})

	// Registered only when the CRD is installed, for the reason given in
	// CCLComponentSetReconciler.SetupWithManager.
	if argoCDKindInstalled(mgr.GetRESTMapper(), mgr.GetLogger().WithName("componenthealth"), "Application") {
		b = b.Watches(&argocdv1alpha1.Application{},
			handler.EnqueueRequestsFromMapFunc(componentSetForApplication),
			builder.WithPredicates(applicationChanged))
	}

	return b.Named("componenthealth").Complete(r)
}
