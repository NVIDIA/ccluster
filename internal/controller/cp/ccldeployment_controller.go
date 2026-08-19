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

// Condition types and reasons reported on CCLDeployment.status.
const (
	// conditionTypeValidated reports whether every component's infraPlaneRef resolves
	// to a CCLInfraPlane that Argo CD can already reach.
	conditionTypeValidated = "Validated"
	// conditionTypeProgressing reports on the RollingSync: false once every component
	// is healthy and synced.
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

// Metadata written onto the ApplicationSet, and onto the Applications it generates.
const (
	// Back-references to the CCLDeployment an Application was created for, for the
	// reason given on ownerAnnotations: owner references may not cross namespaces.
	annotationDeploymentName      = "ccluster.nvidia.com/deployment-name"
	annotationDeploymentNamespace = "ccluster.nvidia.com/deployment-namespace"

	// The same back-reference as labels, because a label can be selected on and an
	// annotation cannot: listing a CCLDeployment's Applications is how the operator
	// knows what it has already released. This is why a CCLDeployment's name is capped
	// at a label value's 63 characters.
	labelDeploymentName      = "ccluster.nvidia.com/deployment-name"
	labelDeploymentNamespace = "ccluster.nvidia.com/deployment-namespace"

	// labelWave carries a component's position in the rollout. Nothing acts on it; it
	// exists so `kubectl get applications -L ccluster.nvidia.com/wave` shows the order.
	labelWave = "ccluster.nvidia.com/wave"

	// deploymentFinalizerName guards the generated Applications, which nothing else
	// would clean up. It is distinct from finalizerName so the two controllers never
	// remove each other's.
	deploymentFinalizerName = "ccluster.nvidia.com/argocd-applications"
)

// CCLDeploymentReconciler reconciles a CCLDeployment object
type CCLDeploymentReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// ArgoCDNamespace is the namespace Argo CD runs in, and therefore the namespace the
	// generated ApplicationSet is written to.
	ArgoCDNamespace string

	// CatalogRepoURL and CatalogRevision locate the chart catalog components are
	// deployed from. Component paths in the spec are relative to that repository's
	// root; the repository and revision themselves are not settable per component.
	CatalogRepoURL  string
	CatalogRevision string
}

// +kubebuilder:rbac:groups=ccluster.nvidia.com,resources=ccldeployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ccluster.nvidia.com,resources=ccldeployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ccluster.nvidia.com,resources=ccldeployments/finalizers,verbs=update
// +kubebuilder:rbac:groups=argoproj.io,resources=applications,verbs=get;list;watch;create;update;patch;delete

// Reconcile projects a CCLDeployment onto a single Argo CD ApplicationSet:
//
//  1. Every component's infraPlaneRef is resolved. The CCLInfraPlane must exist and be
//     Available, because the generated Applications address their destination cluster by
//     name, and that name only exists once its cluster Secret has been mirrored.
//     Reported as the Validated condition.
//  2. One Argo CD Application is written per released component. Reported as the
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
func (r *CCLDeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var deployment cclusternvidiacomv1alpha1.CCLDeployment
	if err := r.Get(ctx, req.NamespacedName, &deployment); err != nil {
		if apierrors.IsNotFound(err) {
			// The controller's logger already carries the request's name and namespace.
			log.Info("Ignored missing CCLDeployment")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !deployment.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.finalize(ctx, &deployment)
	}

	base := deployment.DeepCopy()
	if controllerutil.AddFinalizer(&deployment, deploymentFinalizerName) {
		// Return after the write: it bumps the resource version, and the next
		// reconcile works from a settled object.
		return ctrl.Result{}, client.IgnoreNotFound(
			r.Patch(ctx, &deployment, client.MergeFrom(base)))
	}

	reason, message, err := r.resolveInfraPlanes(ctx, &deployment)
	if err != nil {
		return ctrl.Result{}, err
	}
	if reason != "" {
		// No requeue: only an edit to the CCLDeployment or its CCLInfraPlanes can
		// resolve this, and both are watched.
		log.Info("Did not apply CCLDeployment", "reason", reason, "message", message)
		return ctrl.Result{}, r.setConditions(ctx, &deployment,
			condition(conditionTypeValidated, metav1.ConditionFalse, reason, message))
	}

	// The Applications already generated are both the record of what has been
	// released and the evidence for whether the next component may follow.
	applications, err := r.listApplications(ctx, &deployment)
	if err != nil {
		return ctrl.Result{}, err
	}
	released := releaseCount(&deployment, applications)

	changed, applyErr := r.reconcileApplications(ctx, &deployment, released, applications)
	appliedStatus, appliedReason, appliedMessage, retryErr :=
		r.applicationsCondition(&deployment, released, applyErr)

	if applyErr == nil && changed {
		log.Info("Applied Argo CD Applications", "namespace", r.ArgoCDNamespace,
			"released", released, "components", len(deployment.Spec.Components))
	}
	if appliedStatus != metav1.ConditionTrue {
		log.Info("Did not apply Argo CD Applications", "ccldeployment", deployment.Name,
			"reason", appliedReason, "message", appliedMessage)
	}

	conditions := []metav1.Condition{
		condition(conditionTypeValidated, metav1.ConditionTrue,
			reasonComponentsAccepted, fmt.Sprintf(
				"All %d components reference an available CCLInfraPlane",
				len(deployment.Spec.Components))),
		condition(conditionTypeComponentsApplied, appliedStatus, appliedReason, appliedMessage),
	}

	// Only meaningful once the Applications exist; before that there is nothing to read
	// back and the previous Progressing condition, if any, still stands. They are
	// re-listed because reconcileApplications has just created some.
	if appliedStatus == metav1.ConditionTrue {
		applications, err = r.listApplications(ctx, &deployment)
		if err != nil {
			return ctrl.Result{}, err
		}
		progressStatus, progressReason, progressMessage := r.rolloutStatus(&deployment, applications)
		conditions = append(conditions, condition(
			conditionTypeProgressing, progressStatus, progressReason, progressMessage))
	}

	if err := r.setConditions(ctx, &deployment, conditions...); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, retryErr
}

// resolveInfraPlanes checks that every component names a CCLInfraPlane that exists in the
// CCLDeployment's namespace and is Available.
//
// An empty reason means every component resolved. A non-empty reason is a condition to
// report, not an error to retry; a returned error is a genuine API failure.
func (r *CCLDeploymentReconciler) resolveInfraPlanes(
	ctx context.Context,
	deployment *cclusternvidiacomv1alpha1.CCLDeployment,
) (reason, message string, err error) {
	// Components frequently share an infra plane; each distinct one is only looked up
	// once. The cache makes the Get cheap, but not free.
	checked := make(map[string]struct{}, len(deployment.Spec.Components))

	for _, component := range deployment.Spec.Components {
		name := component.InfraPlaneRef.Name
		if _, done := checked[name]; done {
			continue
		}
		checked[name] = struct{}{}

		var infraPlane cclusternvidiacomv1alpha1.CCLInfraPlane
		key := client.ObjectKey{Namespace: deployment.Namespace, Name: name}
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

// finalize deletes the ApplicationSet, if it is still ours, and then drops the finalizer
// so the CCLDeployment can go away.
func (r *CCLDeploymentReconciler) finalize(
	ctx context.Context,
	deployment *cclusternvidiacomv1alpha1.CCLDeployment,
) error {
	if !controllerutil.ContainsFinalizer(deployment, deploymentFinalizerName) {
		return nil
	}

	if err := r.deleteApplications(ctx, deployment); err != nil {
		return err
	}

	// Patched rather than updated, and NotFound ignored, for the reasons in
	// setConditions: the deletion that brought us here may not have reached the cache.
	base := deployment.DeepCopy()
	controllerutil.RemoveFinalizer(deployment, deploymentFinalizerName)
	return client.IgnoreNotFound(r.Patch(ctx, deployment, client.MergeFrom(base)))
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
// on a CCLDeployment that was just written the cached copy can carry a stale resource
// version, and an update would fail the optimistic-concurrency check even though the
// status it computed is correct. A patch carries no such precondition. A NotFound is
// ignored - the object was deleted while we were reconciling it, and there is no status
// left to record.
func (r *CCLDeploymentReconciler) setConditions(
	ctx context.Context,
	deployment *cclusternvidiacomv1alpha1.CCLDeployment,
	conditions ...metav1.Condition,
) error {
	base := deployment.DeepCopy()

	for _, c := range conditions {
		c.ObservedGeneration = deployment.Generation
		apimeta.SetStatusCondition(&deployment.Status.Conditions, c)
	}
	deployment.Status.ObservedGeneration = deployment.Generation

	return client.IgnoreNotFound(r.Status().Patch(ctx, deployment, client.MergeFrom(base)))
}

// deploymentsForInfraPlane maps a CCLInfraPlane to the CCLDeployments that reference it,
// so that a plane becoming Available unblocks them.
func (r *CCLDeploymentReconciler) deploymentsForInfraPlane(ctx context.Context, obj client.Object) []reconcile.Request {
	var list cclusternvidiacomv1alpha1.CCLDeploymentList
	if err := r.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list CCLDeployments for CCLInfraPlane",
			"name", obj.GetName(), "namespace", obj.GetNamespace())
		return nil
	}

	var requests []reconcile.Request
	for i := range list.Items {
		deployment := &list.Items[i]
		for _, component := range deployment.Spec.Components {
			if component.InfraPlaneRef.Name != obj.GetName() {
				continue
			}
			requests = append(requests, reconcile.Request{
				NamespacedName: client.ObjectKeyFromObject(deployment),
			})
			break
		}
	}
	return requests
}

// deploymentForApplication maps an Application back to the CCLDeployment it was created
// for, using the labels the operator stamps on it. This is what lets the next component be
// released the moment the previous one reports healthy, rather than on a poll.
func (r *CCLDeploymentReconciler) deploymentForApplication(_ context.Context, obj client.Object) []reconcile.Request {
	labels := obj.GetLabels()
	name, hasName := labels[labelDeploymentName]
	namespace, hasNamespace := labels[labelDeploymentNamespace]
	if !hasName || !hasNamespace {
		// Not one of ours - Argo CD Applications created by anything else.
		return nil
	}

	return []reconcile.Request{{
		NamespacedName: client.ObjectKey{Namespace: namespace, Name: name},
	}}
}

// SetupWithManager sets up the controller with the Manager.
func (r *CCLDeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cclusternvidiacomv1alpha1.CCLDeployment{}).
		Watches(&cclusternvidiacomv1alpha1.CCLInfraPlane{},
			handler.EnqueueRequestsFromMapFunc(r.deploymentsForInfraPlane)).
		Watches(&argocdv1alpha1.Application{},
			handler.EnqueueRequestsFromMapFunc(r.deploymentForApplication)).
		Named("ccldeployment").
		Complete(r)
}
