/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/internal/argocd/apiclient"
	argocdv1alpha1 "github.com/NVIDIA/ccluster/internal/argocd/v1alpha1"
	driftregistry "github.com/NVIDIA/ccluster/internal/registry/drift_registry"
)

// Metadata written onto the generated Applications.
const (
	// Back-references to the CCLComponentSet an Application was created for. An
	// Application lives in its CCLComponentSet's own namespace, so an owner reference
	// would be legal here - and is deliberately not used. Garbage collection would
	// delete every Application at once when the CCLComponentSet goes, and teardownNext
	// exists precisely to take them down one at a time in reverse order.
	annotationComponentSetName      = "ccluster.nvidia.com/componentset-name"
	annotationComponentSetNamespace = "ccluster.nvidia.com/componentset-namespace"

	// The name again as a label, because a label can be selected on and an annotation
	// cannot: listing a CCLComponentSet's Applications is how the operator knows what it
	// has already released. The namespace needs no label - it is the Application's own.
	// This is why a CCLComponentSet's name is capped at a label value's 63 characters.
	labelComponentSetName = "ccluster.nvidia.com/componentset-name"

	// labelWave carries a component's position in the rollout. Nothing acts on it; it
	// exists so `kubectl get applications -L ccluster.nvidia.com/wave` shows the order.
	labelWave = "ccluster.nvidia.com/wave"

	// componentSetFinalizerName guards the generated Applications, which nothing else
	// would clean up. It is distinct from finalizerName so the two controllers never
	// remove each other's.
	componentSetFinalizerName = "ccluster.nvidia.com/argocd-applications"

	// teardownRetryInterval backstops the Application watch during teardown. Progress
	// is event-driven - an Application going away brings the CCLComponentSet back - so
	// this only covers a missed event, and keeps a component whose cluster is
	// unreachable from wedging the delete silently.
	teardownRetryInterval = 15 * time.Second

	// statusConflictRetryInterval backstops the watch after a status write conflicts.
	// The conflict means the server holds a newer CCLComponentSet, and that version's
	// watch event brings it back. The queue keeps one entry per object at its earliest
	// time, so the event and this requeue make one pass unless the event never comes.
	statusConflictRetryInterval = 500 * time.Millisecond
)

// CCLComponentSetReconciler reconciles a CCLComponentSet object
type CCLComponentSetReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// CatalogRepoURL and CatalogRevision locate the chart catalog components are
	// deployed from. Component paths in the spec are relative to that repository's
	// root; the repository and revision themselves are not settable per component.
	CatalogRepoURL  string
	CatalogRevision string

	// Drift is the registry of charts' drift rules, written as Argo CD ignoreDifferences,
	// normally driftregistry.Get(). Nil diffs every chart in full.
	Drift *driftregistry.Registry

	// ArgoCDServer reads what an Application deployed through argocd-server. Nothing
	// reads through it yet.
	ArgoCDServer *apiclient.Client

	// MaxConcurrentReconciles is how many CCLComponentSets are reconciled at once. Zero
	// falls back to the manager's controller default, which is one unless set. Different
	// CCLComponentSets write disjoint objects; the one overlap, two that map to one
	// Application name, ends in ApplicationConflict for one of them in parallel as it
	// does serially.
	MaxConcurrentReconciles int
}

// +kubebuilder:rbac:groups=ccluster.nvidia.com,resources=cclcomponentsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ccluster.nvidia.com,resources=cclcomponentsets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ccluster.nvidia.com,resources=cclcomponentsets/finalizers,verbs=update
// +kubebuilder:rbac:groups=argoproj.io,resources=applications,verbs=get;list;watch;create;update;patch;delete

// Reconcile projects a CCLComponentSet onto a set of Argo CD Applications:
//
//  1. Every component's infraPlaneRef is resolved. The CCLInfraPlane must exist and be
//     Available, because the generated Applications address their destination cluster by
//     name, and that name only exists once its cluster Secret has been mirrored. An
//     Available CCLInfraPlane also guarantees its namespace's AppProject exists, so the
//     project is never checked here. Reported as the Validated condition.
//  2. One Argo CD Application is written per released component, into the
//     CCLComponentSet's own namespace under that project. Reported as the
//     ComponentsApplied condition.
//  3. Those Applications are read back, both to decide whether the next component may be
//     released and to report where the rollout has got to. Reported as the Progressing
//     condition and status.componentSyncs, on every pass, including one that stopped at
//     step 1 or 2.
//
// Ordering is enforced by withholding a component's Application until everything before
// it has finished - see releaseCount - rather than by an Argo CD sync strategy, which would
// mean switching off the automated sync these Applications depend on.
//
// The operator installs nothing itself; Argo CD does all of the work.
func (r *CCLComponentSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var componentSet cclusternvidiacomv1alpha1.CCLComponentSet
	if err := r.Get(ctx, req.NamespacedName, &componentSet); err != nil {
		if apierrors.IsNotFound(err) {
			// The controller's logger already carries the request's name and namespace.
			log.Info("Ignored missing CCLComponentSet")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !componentSet.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &componentSet)
	}

	base := componentSet.DeepCopy()
	if controllerutil.AddFinalizer(&componentSet, componentSetFinalizerName) {
		// Return after the write: it bumps the resource version, and the next
		// reconcile works from a settled object.
		return ctrl.Result{}, client.IgnoreNotFound(
			r.Patch(ctx, &componentSet, client.MergeFrom(base)))
	}

	reason, message, err := r.resolveInfraPlanes(ctx, &componentSet)
	if err != nil {
		return ctrl.Result{}, err
	}

	// The Applications already generated are both the record of what has been
	// released and the evidence for whether the next component may follow. They are
	// read before validation is acted on, because the status reports them either way.
	all, err := r.listApplications(ctx, &componentSet)
	if err != nil {
		return ctrl.Result{}, err
	}
	applications := liveApplications(all)
	statusBase := componentSet.DeepCopy()

	var conditions []metav1.Condition
	var retryErr error
	if reason != "" {
		// No requeue: only an edit to the CCLComponentSet or its CCLInfraPlanes can
		// resolve this, and both are watched.
		log.Info("Did not apply CCLComponentSet", "reason", reason, "message", message)
		conditions = append(conditions,
			condition(cclusternvidiacomv1alpha1.CCLComponentSetConditionValidated, metav1.ConditionFalse, reason, message))
	} else {
		conditions = append(conditions, condition(cclusternvidiacomv1alpha1.CCLComponentSetConditionValidated, metav1.ConditionTrue,
			cclusternvidiacomv1alpha1.CCLComponentSetReasonComponentsAccepted, fmt.Sprintf(
				"All %d components reference an available CCLInfraPlane",
				len(componentSet.Spec.Components))))

		var applied metav1.Condition
		applied, retryErr = r.applyComponents(ctx, &componentSet, applications)
		conditions = append(conditions, applied)

		// Re-listed because applyComponents may have written some, even on a pass that
		// then failed on a later component.
		if all, err = r.listApplications(ctx, &componentSet); err != nil {
			return ctrl.Result{}, err
		}
		applications = liveApplications(all)
	}

	// Rebuilt on every pass, validated or not, from one list of Applications.
	// componentSyncs has no generation of its own and status.observedGeneration vouches
	// for it, so neither it nor Progressing may outlive the generation this write records.
	// An Application claimed by another CCLComponentSet is left out of both.
	reported := ownedApplications(&componentSet, applications)
	componentSet.Status.ComponentSyncs = r.componentSyncStatuses(&componentSet, reported)
	progressStatus, progressReason, progressMessage :=
		r.rolloutStatus(&componentSet, reported, reason, message)

	conditions = append(conditions,
		condition(cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing, progressStatus, progressReason, progressMessage))

	if err := r.setConditions(ctx, &componentSet, statusBase, conditions...); err != nil {
		if !apierrors.IsConflict(err) {
			return ctrl.Result{}, err
		}
		// Expected rather than a failure: the report was computed from a read that a
		// lagging cache or another writer has since left behind. It is recomputed from a
		// fresh read, not written again, because it belongs to the generation it was read
		// at. A pending apply error keeps its rate-limited retry.
		log.V(1).Info("Requeued CCLComponentSet after its status write conflicted")
		if retryErr == nil {
			return ctrl.Result{RequeueAfter: statusConflictRetryInterval}, nil
		}
	}
	return ctrl.Result{}, retryErr
}

// applyComponents writes the Applications of the components released so far and returns
// the ComponentsApplied condition. A returned error is retried after the status is
// written; a missing CRD or a name conflict is reported in the condition instead.
func (r *CCLComponentSetReconciler) applyComponents(
	ctx context.Context,
	componentSet *cclusternvidiacomv1alpha1.CCLComponentSet,
	applications map[string]*argocdv1alpha1.Application,
) (metav1.Condition, error) {
	log := logf.FromContext(ctx)

	released := r.releaseCount(componentSet, applications)
	changed, applyErr := r.reconcileApplications(ctx, componentSet, released, applications)
	status, reason, message, retryErr := r.applicationsCondition(componentSet, released, applyErr)

	if applyErr == nil && changed {
		log.Info("Applied Argo CD Applications", "namespace", componentSet.Namespace,
			"released", released, "components", len(componentSet.Spec.Components))
	}
	if status != metav1.ConditionTrue {
		log.Info("Did not apply Argo CD Applications", "cclcomponentset", componentSet.Name,
			"reason", reason, "message", message)
	}
	return condition(cclusternvidiacomv1alpha1.CCLComponentSetConditionComponentsApplied, status, reason, message), retryErr
}

// resolveInfraPlanes checks that every component names a CCLInfraPlane that exists in the
// CCLComponentSet's namespace and is Available for its current generation.
//
// An empty reason means every component resolved. A non-empty reason is a condition to
// report, not an error to retry; a returned error is a genuine API failure.
func (r *CCLComponentSetReconciler) resolveInfraPlanes(
	ctx context.Context,
	componentSet *cclusternvidiacomv1alpha1.CCLComponentSet,
) (reason, message string, err error) {
	// Components frequently share an infra plane; each distinct one is only looked up
	// once. The cache makes the Get cheap, but not free.
	checked := make(map[string]struct{}, len(componentSet.Spec.Components))

	for _, component := range componentSet.Spec.Components {
		name := component.InfraPlaneRef.Name
		if _, done := checked[name]; done {
			continue
		}
		checked[name] = struct{}{}

		var infraPlane cclusternvidiacomv1alpha1.CCLInfraPlane
		key := client.ObjectKey{Namespace: componentSet.Namespace, Name: name}
		switch err := r.Get(ctx, key, &infraPlane); {
		case apierrors.IsNotFound(err):
			return cclusternvidiacomv1alpha1.CCLComponentSetReasonInfraPlaneNotFound, fmt.Sprintf(
				"Component %q references CCLInfraPlane %q, which does not exist",
				component.Name, name), nil
		case err != nil:
			return "", "", err
		}

		// Neither case is an error: the CCLInfraPlane may simply not have been
		// reconciled yet, and its watch brings us back when it is.
		//
		// They are reported apart because a plane whose status is behind its spec
		// still prints Available=True - the printer column shows the condition as it
		// stands - so calling it "not yet Available" sends whoever reads the status
		// after a problem that is not there.
		if infraPlane.Status.ObservedGeneration < infraPlane.Generation {
			return cclusternvidiacomv1alpha1.CCLComponentSetReasonInfraPlaneNotReady, fmt.Sprintf(
				"Component %q references CCLInfraPlane %q, whose status has not caught up "+
					"with its spec", component.Name, name), nil
		}
		if !apimeta.IsStatusConditionTrue(infraPlane.Status.Conditions, cclusternvidiacomv1alpha1.CCLInfraPlaneConditionAvailable) {
			return cclusternvidiacomv1alpha1.CCLComponentSetReasonInfraPlaneNotReady, fmt.Sprintf(
				"Component %q references CCLInfraPlane %q, which is not yet Available",
				component.Name, name), nil
		}
	}

	return "", "", nil
}

// finalize tears the components down in reverse, then drops the finalizer so the
// CCLComponentSet can go away.
//
// It removes one Application per call and returns to be re-invoked, rather than deleting
// them all in one pass: the components were deployed in order because a later one may
// depend on an earlier one, so an earlier one must not be removed while a later one is
// still using it. The CCLComponentSet therefore survives until the last chart is gone,
// which is what makes the ordering observable at all.
func (r *CCLComponentSetReconciler) finalize(
	ctx context.Context,
	componentSet *cclusternvidiacomv1alpha1.CCLComponentSet,
) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(componentSet, componentSetFinalizerName) {
		return ctrl.Result{}, nil
	}

	remaining, err := r.teardownNext(ctx, componentSet)
	if err != nil {
		return ctrl.Result{}, err
	}
	if remaining > 0 {
		// The Application watch brings us back as soon as one is really gone; the
		// interval is only a backstop.
		return ctrl.Result{RequeueAfter: teardownRetryInterval}, nil
	}

	// Ignore NotFound if deletion completed before this finalizer patch.
	base := componentSet.DeepCopy()
	controllerutil.RemoveFinalizer(componentSet, componentSetFinalizerName)
	return ctrl.Result{}, client.IgnoreNotFound(r.Patch(ctx, componentSet, client.MergeFrom(base)))
}

// condition builds a metav1.Condition. ObservedGeneration is filled in by setConditions,
// which is the only thing that knows the object it is about to write.
func condition(conditionType string, status metav1.ConditionStatus, reason, message string) metav1.Condition {
	return metav1.Condition{
		Type:    conditionType,
		Status:  status,
		Reason:  reason,
		Message: message,
	}
}

// setConditions sets the given conditions and the observed generation, then writes the
// status subresource in one patch, unless the status is the one base already holds.
//
// Carry the original resource version so a concurrent spec edit cannot receive a
// completion computed for older input. A conflict is returned for Reconcile to requeue; a
// missing object has no status left to record.
//
// Skipping an unchanged status matters because every Application status write, Argo CD's
// periodic refresh included, brings the CCLComponentSet back. A skipped write is safe
// against a stale read: a newer CCLComponentSet has its own watch event, and that pass
// compares against the newer status.
func (r *CCLComponentSetReconciler) setConditions(
	ctx context.Context,
	componentSet *cclusternvidiacomv1alpha1.CCLComponentSet,
	base *cclusternvidiacomv1alpha1.CCLComponentSet,
	conditions ...metav1.Condition,
) error {
	for _, c := range conditions {
		c.ObservedGeneration = componentSet.Generation
		apimeta.SetStatusCondition(&componentSet.Status.Conditions, c)
	}
	componentSet.Status.ObservedGeneration = componentSet.Generation

	if equality.Semantic.DeepEqual(componentSet.Status, base.Status) {
		return nil
	}
	return client.IgnoreNotFound(r.Status().Patch(ctx, componentSet,
		client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})))
}

// componentSetsForInfraPlane maps a CCLInfraPlane to the CCLComponentSets that reference it,
// so that a plane becoming Available unblocks them.
func (r *CCLComponentSetReconciler) componentSetsForInfraPlane(ctx context.Context, obj client.Object) []reconcile.Request {
	var list cclusternvidiacomv1alpha1.CCLComponentSetList
	if err := r.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list CCLComponentSets for CCLInfraPlane",
			"name", obj.GetName(), "namespace", obj.GetNamespace())
		return nil
	}

	var requests []reconcile.Request
	for i := range list.Items {
		componentSet := &list.Items[i]
		for _, component := range componentSet.Spec.Components {
			if component.InfraPlaneRef.Name != obj.GetName() {
				continue
			}
			requests = append(requests, reconcile.Request{
				NamespacedName: client.ObjectKeyFromObject(componentSet),
			})
			break
		}
	}
	return requests
}

// componentSetForApplication maps an Application back to the CCLComponentSet it was created
// for: the one named by its label, in the Application's own namespace. This is what lets
// the next component be released the moment the previous one finishes, rather than on
// a poll.
func (r *CCLComponentSetReconciler) componentSetForApplication(_ context.Context, obj client.Object) []reconcile.Request {
	name, hasName := obj.GetLabels()[labelComponentSetName]
	if !hasName {
		// Not one of ours - Argo CD Applications created by anything else.
		return nil
	}

	return []reconcile.Request{{
		NamespacedName: client.ObjectKey{Namespace: obj.GetNamespace(), Name: name},
	}}
}

// SetupWithManager sets up the controller with the Manager.
func (r *CCLComponentSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	builder := ctrl.NewControllerManagedBy(mgr).
		For(&cclusternvidiacomv1alpha1.CCLComponentSet{}).
		Watches(&cclusternvidiacomv1alpha1.CCLInfraPlane{},
			handler.EnqueueRequestsFromMapFunc(r.componentSetsForInfraPlane)).
		WithOptions(controller.Options{MaxConcurrentReconciles: r.MaxConcurrentReconciles})

	// The Application watch is registered only when Argo CD's CRD is already
	// installed. controller-runtime blocks a controller from starting until every
	// watch source syncs, and a source for a kind the API server does not serve never
	// does - so an unconditional watch means that with `make deploy-cp ARGOCD=false`,
	// or simply with the operator started before Argo CD, the CCLComponentSet controller
	// never runs at all. It logs "if kind is a CRD, it should be installed before
	// calling Start" every ten seconds and reconciles nothing, which also makes the
	// ApplicationCRDMissing branch below unreachable in a real cluster.
	//
	// Skipping the watch keeps the controller alive: it still reconciles on
	// CCLComponentSet and CCLInfraPlane events, listApplications tolerates the missing
	// kind, and the status reports ApplicationCRDMissing. What is lost is reacting to
	// Application health changes, so the operator must be restarted after Argo CD is
	// installed for a rollout to progress past its first wave. That is the same
	// restart the chart performs when it installs both together.
	if argoCDKindInstalled(mgr.GetRESTMapper(), mgr.GetLogger().WithName("cclcomponentset"), "Application") {
		builder = builder.Watches(&argocdv1alpha1.Application{},
			handler.EnqueueRequestsFromMapFunc(r.componentSetForApplication))
	}

	return builder.Named("cclcomponentset").Complete(r)
}

// argoCDKindInstalled reports whether the API server currently serves the named Argo CD
// kind.
func argoCDKindInstalled(mapper apimeta.RESTMapper, log logr.Logger, kind string) bool {
	return kindInstalled(mapper, log, argocdv1alpha1.GroupVersion.WithKind(kind))
}

// kindInstalled reports whether the API server currently serves gvk, whose CRD another
// chart installs. It takes the RESTMapper rather than the manager so the three outcomes
// below can be tested without standing one up.
func kindInstalled(mapper apimeta.RESTMapper, log logr.Logger, gvk schema.GroupVersionKind) bool {
	_, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	switch {
	case err == nil:
		return true
	case apimeta.IsNoMatchError(err):
		log.Info("CRD not installed, not watching its kind; "+
			"restart the operator after installing it", "kind", gvk.String())
		return false
	default:
		// Discovery itself failed. Assume the CRD is present rather than silently
		// dropping the watch for the lifetime of the process.
		log.Error(err, "Failed to check for a CRD, registering the watch anyway",
			"kind", gvk.String())
		return true
	}
}
