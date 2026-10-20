/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
	"github.com/NVIDIA/ccluster/internal/argocd/apiclient"
	argocdv1alpha1 "github.com/NVIDIA/ccluster/internal/argocd/v1alpha1"
	statusregistry "github.com/NVIDIA/ccluster/internal/registry/status_registry"
)

// Reasons the controller reports itself, before or instead of a chart's status function.
const (
	reasonNotReleased        = "NotReleased"
	reasonAwaitingComparison = "AwaitingComparison"
	reasonStatusUnavailable  = "StatusUnavailable"
	reasonStatusPanicked     = "StatusPanicked"
	reasonInvalidStatus      = "InvalidStatus"
)

const (
	// statusPollSettling and statusPollSettled are how often a CCLComponentSet's
	// components are observed again: sooner while any of them is still on its way to a
	// settled state, so a change is seen promptly, and rarely once all have settled.
	statusPollSettling = 30 * time.Second
	statusPollSettled  = 5 * time.Minute

	// observeTimeout bounds one component's observation.
	observeTimeout = 30 * time.Second

	// maxStatusMessage caps a message carried from an error into the status.
	maxStatusMessage = 1024
)

// ComponentStatusReconciler reports each component of a CCLComponentSet in its chart's
// own terms, in status.componentStatuses.
//
// It is separate from CCLComponentSetReconciler because its reads go through
// argocd-server, which is slower than the cache and can be unavailable, and neither may
// hold up a rollout. It writes nothing but status.componentStatuses.
type ComponentStatusReconciler struct {
	client.Client

	// Rollout is the CCLComponentSet controller, whose Applications this one reads and
	// whose idea of the desired Application spec it shares.
	Rollout *CCLComponentSetReconciler

	// Statuses is the registry of charts' status functions, normally
	// statusregistry.Get(). Nil reports every component as Unknown.
	Statuses *statusregistry.Registry

	// Observer reads a component's objects, manifests and events.
	Observer ResourceObserver

	// now is the clock; nil means time.Now. Tests replace it.
	now func() time.Time
}

// +kubebuilder:rbac:groups=ccluster.nvidia.com,resources=cclcomponentsets,verbs=get;list;watch
// +kubebuilder:rbac:groups=ccluster.nvidia.com,resources=cclcomponentsets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=argoproj.io,resources=applications,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile observes every component of a CCLComponentSet and writes their states.
//
// A component is Pending until its Application is released, and Starting until Argo CD
// has compared the spec the operator wrote, both judged as the rollout judges them.
// After that its chart's status function decides, from what argocd-server reports
// about the Application's objects. Components are observed concurrently, each with its
// own deadline, and one failing to be observed is reported Unknown without affecting the
// rest.
//
// Its inputs are not all watchable: a Pod failing changes no Application field. So it
// polls, as the infra-plane operator's Verifiers do, sooner while any component is still
// settling.
func (r *ComponentStatusReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var componentSet cclusternvidiacomv1alpha1.CCLComponentSet
	if err := r.Get(ctx, req.NamespacedName, &componentSet); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !componentSet.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	all, err := r.Rollout.listApplications(ctx, &componentSet)
	if err != nil {
		return ctrl.Result{}, err
	}
	applications := liveApplications(all)

	statuses := make([]cclusternvidiacomv1alpha1.ComponentStatus, len(componentSet.Spec.Components))
	observed := metav1.NewTime(r.clock())
	var wg sync.WaitGroup
	for i, component := range componentSet.Spec.Components {
		application := applications[applicationName(componentSet.Name, component.Name)]
		wg.Go(func() {
			status := r.componentStatus(ctx, &componentSet, component, application)
			statuses[i] = cclusternvidiacomv1alpha1.ComponentStatus{
				Name:             component.Name,
				State:            status.State,
				Reason:           status.Reason,
				Message:          status.Message,
				LastObservedTime: observed,
			}
		})
	}
	wg.Wait()

	base := componentSet.DeepCopy()
	componentSet.Status.ComponentStatuses = statuses
	if err := r.Status().Patch(ctx, &componentSet, client.MergeFrom(base)); err != nil {
		// A missing object has no status left to record.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	return ctrl.Result{RequeueAfter: pollInterval(statuses)}, nil
}

// componentStatus works out one component's state. application is nil while the
// component has not been released.
func (r *ComponentStatusReconciler) componentStatus(
	ctx context.Context,
	componentSet *cclusternvidiacomv1alpha1.CCLComponentSet,
	component cclusternvidiacomv1alpha1.ComponentSpec,
	application *argocdv1alpha1.Application,
) catalogs.Status {
	if application == nil {
		return catalogs.Status{
			State:   cclusternvidiacomv1alpha1.ComponentStatePending,
			Reason:  reasonNotReleased,
			Message: "Waiting for the components before it to finish rolling out",
		}
	}
	if !comparedToMatchesSpec(application, r.Rollout.applicationSpec(componentSet, component)) {
		return catalogs.Status{
			State:   cclusternvidiacomv1alpha1.ComponentStateStarting,
			Reason:  reasonAwaitingComparison,
			Message: "Waiting for Argo CD to compare the current spec",
		}
	}

	ctx, cancel := context.WithTimeout(ctx, observeTimeout)
	defer cancel()
	observed, err := r.Observer.Observe(ctx, apiclient.ApplicationRef{
		Namespace: application.Namespace,
		Name:      application.Name,
		Project:   application.Spec.Project,
	})
	if err != nil {
		logf.FromContext(ctx).Info("Did not observe component of CCLComponentSet",
			"component", component.Name, "error", err.Error())
		return catalogs.Status{
			State:   cclusternvidiacomv1alpha1.ComponentStateUnknown,
			Reason:  reasonStatusUnavailable,
			Message: truncate(fmt.Sprintf("Could not observe the component through Argo CD: %v", err)),
		}
	}
	return r.runStatus(component.ComponentEntry, observed)
}

// runStatus runs the chart's status function, standing in for one that is missing,
// panics, or reports a state the CRD would refuse.
func (r *ComponentStatusReconciler) runStatus(
	entry cclusternvidiacomv1alpha1.ComponentEntry,
	observed catalogs.ObservedResources,
) (status catalogs.Status) {
	var statusFunc statusregistry.StatusFunc
	if r.Statuses != nil {
		statusFunc, _ = r.Statuses.Lookup(entry.Chart)
	}
	if statusFunc == nil {
		return catalogs.Status{
			State:   cclusternvidiacomv1alpha1.ComponentStateUnknown,
			Reason:  statusregistry.ReasonStatusNotImplemented,
			Message: fmt.Sprintf("Chart %q has no status function", entry.Chart),
		}
	}

	defer func() {
		if p := recover(); p != nil {
			status = catalogs.Status{
				State:   cclusternvidiacomv1alpha1.ComponentStateUnknown,
				Reason:  reasonStatusPanicked,
				Message: truncate(fmt.Sprintf("The chart's status function panicked: %v", p)),
			}
		}
	}()
	status = statusFunc(entry, observed)
	if !slices.Contains(componentStates, status.State) {
		return catalogs.Status{
			State:   cclusternvidiacomv1alpha1.ComponentStateUnknown,
			Reason:  reasonInvalidStatus,
			Message: fmt.Sprintf("The chart's status function reported state %q", status.State),
		}
	}
	status.Message = truncate(status.Message)
	return status
}

// componentStates are the states the CRD admits.
var componentStates = []cclusternvidiacomv1alpha1.ComponentState{
	cclusternvidiacomv1alpha1.ComponentStatePending,
	cclusternvidiacomv1alpha1.ComponentStateStarting,
	cclusternvidiacomv1alpha1.ComponentStateRunning,
	cclusternvidiacomv1alpha1.ComponentStateFailing,
	cclusternvidiacomv1alpha1.ComponentStateSucceeded,
	cclusternvidiacomv1alpha1.ComponentStateFailed,
	cclusternvidiacomv1alpha1.ComponentStateUnknown,
}

// pollInterval is how long until the components are observed again: soon while any is
// in a state expected to change, rarely once all are Running, Succeeded or Failed. A
// Failed component stays failed until its spec changes, which triggers a pass anyway.
func pollInterval(statuses []cclusternvidiacomv1alpha1.ComponentStatus) time.Duration {
	for _, s := range statuses {
		switch s.State {
		case cclusternvidiacomv1alpha1.ComponentStateRunning,
			cclusternvidiacomv1alpha1.ComponentStateSucceeded,
			cclusternvidiacomv1alpha1.ComponentStateFailed:
		default:
			return statusPollSettling
		}
	}
	return statusPollSettled
}

func truncate(message string) string {
	if len(message) <= maxStatusMessage {
		return message
	}
	return message[:maxStatusMessage-3] + "..."
}

func (r *ComponentStatusReconciler) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// SetupWithManager sets up the controller with the Manager.
//
// Only spec changes trigger it among CCLComponentSet events: its own status writes, and
// the rollout controller's, would otherwise rerun every observation. Application events
// do, so a component is reported Starting or observed as soon as Argo CD has compared it.
func (r *ComponentStatusReconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr).
		For(&cclusternvidiacomv1alpha1.CCLComponentSet{},
			builder.WithPredicates(predicate.GenerationChangedPredicate{}))

	// Registered only when the CRD is installed, for the reason given in
	// CCLComponentSetReconciler.SetupWithManager.
	if argoCDKindInstalled(mgr.GetRESTMapper(), mgr.GetLogger().WithName("componentstatus"), "Application") {
		b = b.Watches(&argocdv1alpha1.Application{},
			handler.EnqueueRequestsFromMapFunc(r.Rollout.componentSetForApplication))
	}

	return b.Named("componentstatus").Complete(r)
}
