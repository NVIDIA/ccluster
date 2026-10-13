/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

import (
	"context"
	"fmt"
	"slices"
	"strings"
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
	// conditionTypeValidated reports whether the spec passed the operator's sanity
	// checks: catalog charts, valid values, a single CCLInfraPlane that exists and is
	// Available, and free Application names. Nothing is written to Argo CD until it is
	// True.
	conditionTypeValidated = "Validated"
	// conditionTypeProgressing reports on the rollout: false once every component has
	// synced the spec the operator wrote and is healthy. Deprecated: rollout state is
	// moving to status fields.
	conditionTypeProgressing = "Progressing"
	// conditionTypeComponentsApplied reports on the Argo CD Applications that deploy
	// the components onto their infra-plane clusters. Deprecated: write outcomes are
	// moving to status fields.
	conditionTypeComponentsApplied = "ComponentsApplied"

	// Validated reasons, in the order their checks run. A failing Validated carries the
	// reason of the first failing check.
	reasonSpecValid           = "SpecValid"
	reasonUnknownChart        = "UnknownChart"
	reasonInvalidValues       = "InvalidValues"
	reasonMultipleInfraPlanes = "MultipleInfraPlanes"
	reasonInfraPlaneNotFound  = "InfraPlaneNotFound"
	reasonInfraPlaneNotReady  = "InfraPlaneNotReady"
	reasonNameConflict        = "NameConflict"

	// reasonNotValidated marks the conditions that were not evaluated because Validated
	// is False, so none of them is left standing from an older generation.
	reasonNotValidated = "NotValidated"

	reasonWaveSyncing     = "WaveSyncing"
	reasonWaveSyncFailed  = "WaveSyncFailed"
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

	// Validators is the registry of value checks, one per catalog chart, normally
	// CatalogValidators(). A component whose chart is not registered fails validation.
	Validators *ValidatorRegistry
}

// +kubebuilder:rbac:groups=ccluster.nvidia.com,resources=cclcomponentsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ccluster.nvidia.com,resources=cclcomponentsets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ccluster.nvidia.com,resources=cclcomponentsets/finalizers,verbs=update
// +kubebuilder:rbac:groups=argoproj.io,resources=applications,verbs=get;list;watch;create;update;patch;delete

// Reconcile projects a CCLComponentSet onto a set of Argo CD Applications:
//
//  1. The spec is sanity-checked: every chart is a catalog chart whose values pass its
//     registered check, every component names the same CCLInfraPlane, that plane exists
//     and is Available, and no generated Application name belongs to another
//     CCLComponentSet. The plane must be Available because the generated Applications
//     address their destination cluster by name, and that name only exists once its
//     cluster Secret has been mirrored. An Available CCLInfraPlane also guarantees its
//     namespace's AppProject exists, so the project is never checked here. Reported as
//     the Validated condition; nothing is written to Argo CD until it is True.
//  2. One Argo CD Application is written per released component, into the
//     CCLComponentSet's own namespace under that project. Reported as the
//     ComponentsApplied condition.
//  3. Those Applications are read back, both to decide whether the next component may be
//     released and to report where the rollout has got to. Reported as the Progressing
//     condition.
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

	reason, message, err := r.validate(ctx, &componentSet)
	if err != nil {
		return ctrl.Result{}, err
	}
	if reason != "" {
		// No requeue: only an edit to the CCLComponentSet, its CCLInfraPlane or the
		// conflicting Application can resolve this, and all three are watched. Nothing
		// is written, patched or pruned; released Applications keep the last valid spec.
		log.Info("Did not validate CCLComponentSet", "reason", reason, "message", message)
		notEvaluated := "Not evaluated: the CCLComponentSet is not Validated"
		return ctrl.Result{}, r.setConditions(ctx, &componentSet, componentSet.DeepCopy(),
			condition(conditionTypeValidated, metav1.ConditionFalse, reason, message),
			condition(conditionTypeComponentsApplied, metav1.ConditionFalse,
				reasonNotValidated, notEvaluated),
			condition(conditionTypeProgressing, metav1.ConditionFalse,
				reasonNotValidated, notEvaluated))
	}

	// The Applications already generated are both the record of what has been
	// released and the evidence for whether the next component may follow.
	all, err := r.listApplications(ctx, &componentSet)
	if err != nil {
		return ctrl.Result{}, err
	}
	applications := liveApplications(all)
	statusBase := componentSet.DeepCopy()
	released := r.releaseCount(&componentSet, applications)

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
			reasonSpecValid, fmt.Sprintf("All %d components passed validation",
				len(componentSet.Spec.Components))),
		condition(conditionTypeComponentsApplied, appliedStatus, appliedReason, appliedMessage),
	}

	// Only meaningful once the Applications exist; before that there is nothing to read
	// back and the previous Progressing condition, if any, still stands. They are
	// re-listed because reconcileApplications has just created some.
	if applyErr == nil {
		all, err = r.listApplications(ctx, &componentSet)
		if err != nil {
			return ctrl.Result{}, err
		}
		applications = liveApplications(all)
		r.reportComponentSyncs(&componentSet, applications)
		progressStatus, progressReason, progressMessage := r.rolloutStatus(&componentSet, applications)
		conditions = append(conditions, condition(
			conditionTypeProgressing, progressStatus, progressReason, progressMessage))
	}

	if err := r.setConditions(ctx, &componentSet, statusBase, conditions...); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, retryErr
}

// validate sanity-checks componentSet before anything is written for it.
//
// Every check runs and every failure is reported, so one edit can fix them all: the
// message lists them, and the reason is the first failing check's, in the order the
// reason constants are declared. An empty reason means the spec is valid. A non-empty
// reason is a condition to report, not an error to retry; a returned error is a genuine
// API failure.
func (r *CCLComponentSetReconciler) validate(
	ctx context.Context,
	componentSet *cclusternvidiacomv1alpha1.CCLComponentSet,
) (reason, message string, err error) {
	type failure struct{ reason, message string }
	var failures []failure
	fail := func(reason, format string, args ...any) {
		failures = append(failures, failure{reason, fmt.Sprintf(format, args...)})
	}

	for _, component := range componentSet.Spec.Components {
		validate, known := r.Validators.Lookup(component.Chart)
		if !known {
			fail(reasonUnknownChart, "component %q: chart %q is not in the catalog",
				component.Name, component.Chart)
			continue
		}
		if err := validate(component.Values); err != nil {
			fail(reasonInvalidValues, "component %q: %v", component.Name, err)
		}
	}

	// One CCLInfraPlane per CCLComponentSet is a limitation of the current design, not
	// of the API: infraPlaneRef stays per component so that lifting it needs no schema
	// change.
	var planes []string
	for _, component := range componentSet.Spec.Components {
		if name := component.InfraPlaneRef.Name; !slices.Contains(planes, name) {
			planes = append(planes, name)
		}
	}
	if len(planes) > 1 {
		fail(reasonMultipleInfraPlanes, "components reference CCLInfraPlanes %s: a "+
			"CCLComponentSet spanning more than one infra plane is not supported yet",
			strings.Join(planes, ", "))
	}

	for _, name := range planes {
		reason, message, err := r.checkInfraPlane(ctx, componentSet.Namespace, name)
		if err != nil {
			return "", "", err
		}
		if reason != "" {
			fail(reason, "%s", message)
		}
	}

	for _, component := range componentSet.Spec.Components {
		name := applicationName(componentSet.Name, component.Name)
		application := &argocdv1alpha1.Application{}
		err := r.Get(ctx, client.ObjectKey{Namespace: componentSet.Namespace, Name: name}, application)
		switch {
		case apierrors.IsNotFound(err) || apimeta.IsNoMatchError(err):
			// Free, or Argo CD is not installed, which ComponentsApplied reports.
			continue
		case err != nil:
			return "", "", err
		}
		if !componentSetOwner.ownedBy(application, componentSet) {
			annotations := application.GetAnnotations()
			fail(reasonNameConflict, "component %q: Application %q belongs to CCLComponentSet %s/%s",
				component.Name, name, annotations[annotationComponentSetNamespace],
				annotations[annotationComponentSetName])
		}
	}

	if len(failures) == 0 {
		return "", "", nil
	}
	order := []string{
		reasonUnknownChart, reasonInvalidValues, reasonMultipleInfraPlanes,
		reasonInfraPlaneNotFound, reasonInfraPlaneNotReady, reasonNameConflict,
	}
	for _, candidate := range order {
		if slices.ContainsFunc(failures, func(f failure) bool { return f.reason == candidate }) {
			reason = candidate
			break
		}
	}
	messages := make([]string, 0, len(failures))
	for _, f := range failures {
		messages = append(messages, f.message)
	}
	return reason, strings.Join(messages, "; "), nil
}

// checkInfraPlane checks that the CCLInfraPlane called name exists in namespace and is
// Available for its current generation. An empty reason means it is.
func (r *CCLComponentSetReconciler) checkInfraPlane(
	ctx context.Context,
	namespace, name string,
) (reason, message string, err error) {
	var infraPlane cclusternvidiacomv1alpha1.CCLInfraPlane
	switch err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &infraPlane); {
	case apierrors.IsNotFound(err):
		return reasonInfraPlaneNotFound, fmt.Sprintf("CCLInfraPlane %q does not exist", name), nil
	case err != nil:
		return "", "", err
	}

	// Neither case is an error: the CCLInfraPlane may simply not have been reconciled
	// yet, and its watch brings us back when it is.
	//
	// They are reported apart because a plane whose status is behind its spec still
	// prints Available=True - the printer column shows the condition as it stands - so
	// calling it "not yet Available" sends whoever reads the status after a problem
	// that is not there.
	if infraPlane.Status.ObservedGeneration < infraPlane.Generation {
		return reasonInfraPlaneNotReady, fmt.Sprintf(
			"CCLInfraPlane %q, whose status has not caught up with its spec", name), nil
	}
	if !apimeta.IsStatusConditionTrue(infraPlane.Status.Conditions, conditionTypeAvailable) {
		return reasonInfraPlaneNotReady, fmt.Sprintf(
			"CCLInfraPlane %q is not yet Available", name), nil
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
// status subresource in one patch.
//
// Carry the original resource version so a concurrent spec edit cannot receive a
// completion computed for older input. A conflict is retried by the controller; a missing
// object has no status left to record.
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
