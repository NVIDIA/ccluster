/*
Copyright 2026 NVIDIA CORPORATION & AFFILIATES.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cp

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
	argocdv1alpha1 "github.com/NVIDIA/ccluster/internal/argocd/v1alpha1"
)

// Condition types and reasons reported on CCLComponentSet.status.
const (
	// conditionTypeValidated reports whether every component's infraPlaneRef resolves
	// to a CCLInfraPlane that Argo CD can already reach.
	conditionTypeValidated = "Validated"
	// conditionTypeProgressing reports on the rollout: false once every component is
	// healthy and synced.
	conditionTypeProgressing = "Progressing"
	// conditionTypeComponentsApplied reports on the Argo CD Applications that deploy
	// the components onto their infra-plane clusters.
	conditionTypeComponentsApplied = "ComponentsApplied"

	reasonComponentsAccepted = "ComponentsAccepted"
	reasonInfraPlaneNotFound = "InfraPlaneNotFound"
	reasonInfraPlaneNotReady = "InfraPlaneNotReady"

	reasonWaveSyncing     = "WaveSyncing"
	reasonRolloutComplete = "RolloutComplete"

	reasonApplicationsCreated   = "ApplicationsCreated"
	reasonApplicationCRDMissing = "ApplicationCRDMissing"
	reasonApplicationConflict   = "ApplicationConflict"
	reasonApplicationFailed     = "ApplicationFailed"
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
//     condition.
//
// Ordering is enforced by withholding a component's Application until everything before
// it is healthy - see releaseCount - rather than by an Argo CD sync strategy, which would
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
	if reason != "" {
		// No requeue: only an edit to the CCLComponentSet or its CCLInfraPlanes can
		// resolve this, and both are watched.
		log.Info("Did not apply CCLComponentSet", "reason", reason, "message", message)
		return ctrl.Result{}, r.setConditions(ctx, &componentSet,
			condition(conditionTypeValidated, metav1.ConditionFalse, reason, message))
	}

	// The Applications already generated are both the record of what has been
	// released and the evidence for whether the next component may follow.
	all, err := r.listApplications(ctx, &componentSet)
	if err != nil {
		return ctrl.Result{}, err
	}
	applications := liveApplications(all)
	released := releaseCount(&componentSet, applications)

	changed, applyErr := r.reconcileApplications(ctx, &componentSet, released, applications)
	appliedStatus, appliedReason, appliedMessage, retryErr :=
		r.applicationsCondition(&componentSet, released, applyErr)

	if applyErr == nil && changed {
		log.Info("Applied Argo CD Applications", "namespace", componentSet.Namespace,
			"released", released, "components", len(componentSet.Spec.Components))
	}
	if appliedStatus != metav1.ConditionTrue {
		log.Info("Did not apply Argo CD Applications", "cclcomponentset", componentSet.Name,
			"reason", appliedReason, "message", appliedMessage)
	}

	conditions := []metav1.Condition{
		condition(conditionTypeValidated, metav1.ConditionTrue,
			reasonComponentsAccepted, fmt.Sprintf(
				"All %d components reference an available CCLInfraPlane",
				len(componentSet.Spec.Components))),
		condition(conditionTypeComponentsApplied, appliedStatus, appliedReason, appliedMessage),
	}

	// Only meaningful once the Applications exist; before that there is nothing to read
	// back and the previous Progressing condition, if any, still stands. They are
	// re-listed because reconcileApplications has just created some.
	if appliedStatus == metav1.ConditionTrue {
		all, err = r.listApplications(ctx, &componentSet)
		if err != nil {
			return ctrl.Result{}, err
		}
		applications = liveApplications(all)
		progressStatus, progressReason, progressMessage := r.rolloutStatus(&componentSet, applications)
		conditions = append(conditions, condition(
			conditionTypeProgressing, progressStatus, progressReason, progressMessage))
	}

	if err := r.setConditions(ctx, &componentSet, conditions...); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, retryErr
}

// resolveInfraPlanes checks that every component names a CCLInfraPlane that exists in the
// CCLComponentSet's namespace and is Available.
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
			return reasonInfraPlaneNotFound, fmt.Sprintf(
				"Component %q references CCLInfraPlane %q, which does not exist",
				component.Name, name), nil
		case err != nil:
			return "", "", err
		}

		if !apimeta.IsStatusConditionTrue(infraPlane.Status.Conditions, conditionTypeAvailable) {
			// Not an error: the CCLInfraPlane may simply not have been reconciled
			// yet. Its watch brings us back when it is.
			return reasonInfraPlaneNotReady, fmt.Sprintf(
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

	// Patched rather than updated, and NotFound ignored, for the reasons in
	// setConditions: the deletion that brought us here may not have reached the cache.
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
// status subresource in one patch.
//
// This is a merge patch, not an update, because r.Get reads through the informer cache:
// on a CCLComponentSet that was just written the cached copy can carry a stale resource
// version, and an update would fail the optimistic-concurrency check even though the
// status it computed is correct. A patch carries no such precondition. A NotFound is
// ignored - the object was deleted while we were reconciling it, and there is no status
// left to record.
func (r *CCLComponentSetReconciler) setConditions(
	ctx context.Context,
	componentSet *cclusternvidiacomv1alpha1.CCLComponentSet,
	conditions ...metav1.Condition,
) error {
	base := componentSet.DeepCopy()

	for _, c := range conditions {
		c.ObservedGeneration = componentSet.Generation
		apimeta.SetStatusCondition(&componentSet.Status.Conditions, c)
	}
	componentSet.Status.ObservedGeneration = componentSet.Generation

	return client.IgnoreNotFound(r.Status().Patch(ctx, componentSet, client.MergeFrom(base)))
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
// the next component be released the moment the previous one reports healthy, rather
// than on a poll.
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
			handler.EnqueueRequestsFromMapFunc(r.componentSetsForInfraPlane))

	// The Application watch is registered only when Argo CD's CRD is already
	// installed. controller-runtime blocks a controller from starting until every
	// watch source syncs, and a source for a kind the API server does not serve never
	// does - so an unconditional watch means that with `make deploy-cp ARGOCD=false`,
	// or simply with the operator started before Argo CD, the CCLComponentSet controller
	// never runs at all. It logs "if kind is a CRD, it should be installed before
	// calling Start" every ten seconds and reconciles nothing, which also makes the
	// reasonApplicationCRDMissing branch below unreachable in a real cluster.
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
// kind. It takes the RESTMapper rather than the manager so the three outcomes below can
// be tested without standing one up.
func argoCDKindInstalled(mapper apimeta.RESTMapper, log logr.Logger, kind string) bool {
	gvk := argocdv1alpha1.GroupVersion.WithKind(kind)
	_, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	switch {
	case err == nil:
		return true
	case apimeta.IsNoMatchError(err):
		log.Info("Argo CD CRD not installed, not watching its kind; "+
			"restart the operator after installing Argo CD", "kind", gvk.String())
		return false
	default:
		// Discovery itself failed. Assume the CRD is present rather than silently
		// dropping the watch for the lifetime of the process.
		log.Error(err, "Failed to check for an Argo CD CRD, registering the watch anyway",
			"kind", gvk.String())
		return true
	}
}
