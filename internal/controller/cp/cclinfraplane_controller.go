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
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
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
)

// Condition types and reasons reported on CCLInfraPlane.status.
const (
	conditionTypeAvailable = "Available"

	reasonClusterSecretCreated  = "ClusterSecretCreated"
	reasonSecretNotFound        = "SecretNotFound"
	reasonClusterSecretConflict = "ClusterSecretConflict"
)

// Metadata written onto the mirrored Secret in the Argo CD namespace.
const (
	// argoCDSecretTypeLabel is how Argo CD discovers the Secrets it should treat as
	// cluster registrations.
	argoCDSecretTypeLabel   = "argocd.argoproj.io/secret-type"
	argoCDSecretTypeCluster = "cluster"

	// Back-references to the CCLInfraPlane a mirrored Secret was created for. An
	// owner reference cannot be used: it may not cross namespaces, and the garbage
	// collector deletes objects whose owner it cannot find in their own namespace.
	// These annotations take its place, so the finalizer never deletes, and reconcile
	// never overwrites, a Secret belonging to a different CCLInfraPlane.
	annotationInfraPlaneName      = "ccluster.nvidia.com/infraplane-name"
	annotationInfraPlaneNamespace = "ccluster.nvidia.com/infraplane-namespace"

	// finalizerName guards the mirrored Secret, which nothing else would clean up.
	finalizerName = "ccluster.nvidia.com/argocd-cluster-secret"
)

// errClusterSecretConflict reports that the mirrored Secret name is already taken by
// another CCLInfraPlane. Two CCLInfraPlanes of the same name in different namespaces
// map onto one Secret name in the Argo CD namespace, so this is reachable.
var errClusterSecretConflict = errors.New("cluster Secret belongs to another CCLInfraPlane")

// CCLInfraPlaneReconciler reconciles a CCLInfraPlane object
type CCLInfraPlaneReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// ArgoCDNamespace is the namespace Argo CD runs in, and therefore the namespace
	// the mirrored cluster Secrets are written to.
	ArgoCDNamespace string
}

// +kubebuilder:rbac:groups=ccluster.nvidia.com,resources=cclinfraplanes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ccluster.nvidia.com,resources=cclinfraplanes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ccluster.nvidia.com,resources=cclinfraplanes/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete

// Reconcile clones the Secret named by the CCLInfraPlane's clusterCredentialsRef into
// the Argo CD namespace, under the CCLInfraPlane's own name and labelled as an Argo CD
// cluster Secret, and records the outcome as an Available condition.
//
// The Secret's data is copied verbatim. What it has to contain is Argo CD's business,
// not this operator's.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.1/pkg/reconcile
func (r *CCLInfraPlaneReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var infraPlane cclusternvidiacomv1alpha1.CCLInfraPlane
	if err := r.Get(ctx, req.NamespacedName, &infraPlane); err != nil {
		if apierrors.IsNotFound(err) {
			// The controller's logger already carries the request's name and namespace.
			log.Info("Ignored missing CCLInfraPlane")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !infraPlane.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.finalize(ctx, &infraPlane)
	}

	if controllerutil.AddFinalizer(&infraPlane, finalizerName) {
		// Return after the update: the write bumps the resource version, and the next
		// reconcile works from a settled object.
		return ctrl.Result{}, r.Update(ctx, &infraPlane)
	}

	secretName := infraPlane.Spec.ClusterCredentialsRef.Name

	var source corev1.Secret
	key := client.ObjectKey{Namespace: infraPlane.Namespace, Name: secretName}
	if err := r.Get(ctx, key, &source); err != nil {
		if apierrors.IsNotFound(err) {
			// No requeue: the Secret watch re-triggers reconciliation once it appears.
			log.Info("Missing credentials Secret", "secret", secretName)
			return ctrl.Result{}, r.setAvailable(ctx, &infraPlane, metav1.ConditionFalse,
				reasonSecretNotFound, fmt.Sprintf("Secret %q not found", secretName))
		}
		return ctrl.Result{}, err
	}

	op, err := r.mirrorClusterSecret(ctx, &infraPlane, &source)
	if err != nil {
		if errors.Is(err, errClusterSecretConflict) {
			// No requeue: only an edit elsewhere can resolve a name collision, and the
			// Secret watch picks that up.
			log.Info("Refused to overwrite cluster Secret owned by another CCLInfraPlane",
				"secret", infraPlane.Name, "secretNamespace", r.ArgoCDNamespace)
			return ctrl.Result{}, r.setAvailable(ctx, &infraPlane, metav1.ConditionFalse,
				reasonClusterSecretConflict, fmt.Sprintf(
					"Secret %q in namespace %q belongs to another CCLInfraPlane",
					infraPlane.Name, r.ArgoCDNamespace))
		}
		return ctrl.Result{}, err
	}

	// Names and namespaces only: the credentials themselves must never reach the logs.
	if op != controllerutil.OperationResultNone {
		log.Info("Mirrored Argo CD cluster Secret", "secret", infraPlane.Name,
			"secretNamespace", r.ArgoCDNamespace, "operation", op)
	}

	return ctrl.Result{}, r.setAvailable(ctx, &infraPlane, metav1.ConditionTrue,
		reasonClusterSecretCreated, fmt.Sprintf("Mirrored Secret %q into namespace %q",
			infraPlane.Name, r.ArgoCDNamespace))
}

// mirrorClusterSecret creates or updates the Argo CD cluster Secret for infraPlane from
// source. Only source's data and type are copied; its labels and annotations are not.
func (r *CCLInfraPlaneReconciler) mirrorClusterSecret(
	ctx context.Context,
	infraPlane *cclusternvidiacomv1alpha1.CCLInfraPlane,
	source *corev1.Secret,
) (controllerutil.OperationResult, error) {
	mirror := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      infraPlane.Name,
			Namespace: r.ArgoCDNamespace,
		},
	}

	return controllerutil.CreateOrUpdate(ctx, r.Client, mirror, func() error {
		if !ownedBy(mirror, infraPlane) {
			return errClusterSecretConflict
		}

		if mirror.Labels == nil {
			mirror.Labels = map[string]string{}
		}
		mirror.Labels[argoCDSecretTypeLabel] = argoCDSecretTypeCluster
		mirror.Labels["app.kubernetes.io/name"] = "ccluster"
		mirror.Labels["app.kubernetes.io/managed-by"] = "ccluster-cp-operator"

		if mirror.Annotations == nil {
			mirror.Annotations = map[string]string{}
		}
		mirror.Annotations[annotationInfraPlaneName] = infraPlane.Name
		mirror.Annotations[annotationInfraPlaneNamespace] = infraPlane.Namespace

		mirror.Type = source.Type
		mirror.Data = make(map[string][]byte, len(source.Data))
		for k, v := range source.Data {
			mirror.Data[k] = append([]byte(nil), v...)
		}
		return nil
	})
}

// ownedBy reports whether secret is unclaimed or already back-references infraPlane. A
// Secret created by hand carries no back-reference and is therefore adoptable; one
// pointing at a different CCLInfraPlane is not.
func ownedBy(secret *corev1.Secret, infraPlane *cclusternvidiacomv1alpha1.CCLInfraPlane) bool {
	name, hasName := secret.Annotations[annotationInfraPlaneName]
	namespace, hasNamespace := secret.Annotations[annotationInfraPlaneNamespace]
	if !hasName && !hasNamespace {
		return true
	}
	return name == infraPlane.Name && namespace == infraPlane.Namespace
}

// finalize deletes the mirrored Secret, if it is still ours, and then drops the
// finalizer so the CCLInfraPlane can go away.
func (r *CCLInfraPlaneReconciler) finalize(
	ctx context.Context,
	infraPlane *cclusternvidiacomv1alpha1.CCLInfraPlane,
) error {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(infraPlane, finalizerName) {
		return nil
	}

	var mirror corev1.Secret
	key := client.ObjectKey{Namespace: r.ArgoCDNamespace, Name: infraPlane.Name}
	switch err := r.Get(ctx, key, &mirror); {
	case apierrors.IsNotFound(err):
		// Already gone; nothing to clean up.
	case err != nil:
		return err
	case !ownedBy(&mirror, infraPlane):
		log.Info("Left cluster Secret owned by another CCLInfraPlane in place",
			"secret", infraPlane.Name, "secretNamespace", r.ArgoCDNamespace)
	default:
		if err := r.Delete(ctx, &mirror); client.IgnoreNotFound(err) != nil {
			return err
		}
		log.Info("Deleted Argo CD cluster Secret", "secret", infraPlane.Name,
			"secretNamespace", r.ArgoCDNamespace)
	}

	controllerutil.RemoveFinalizer(infraPlane, finalizerName)
	return r.Update(ctx, infraPlane)
}

// setAvailable sets the Available condition and the observed generation, then writes the
// status subresource. A conflict is returned rather than retried in-loop so that the next
// reconciliation re-reads the object.
func (r *CCLInfraPlaneReconciler) setAvailable(
	ctx context.Context,
	infraPlane *cclusternvidiacomv1alpha1.CCLInfraPlane,
	status metav1.ConditionStatus,
	reason, message string,
) error {
	apimeta.SetStatusCondition(&infraPlane.Status.Conditions, metav1.Condition{
		Type:               conditionTypeAvailable,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: infraPlane.Generation,
	})
	infraPlane.Status.ObservedGeneration = infraPlane.Generation

	return r.Status().Update(ctx, infraPlane)
}

// infraPlanesForSecret maps a Secret to the CCLInfraPlanes that care about it: either a
// mirrored cluster Secret, which names its CCLInfraPlane outright, or a source Secret,
// which is found by searching its own namespace for referrers.
func (r *CCLInfraPlaneReconciler) infraPlanesForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	if obj.GetNamespace() == r.ArgoCDNamespace {
		name, hasName := obj.GetAnnotations()[annotationInfraPlaneName]
		namespace, hasNamespace := obj.GetAnnotations()[annotationInfraPlaneNamespace]
		if hasName && hasNamespace {
			// Drift on, or deletion of, a mirrored Secret is repaired by reconciling
			// the CCLInfraPlane it was created for.
			return []reconcile.Request{{
				NamespacedName: client.ObjectKey{Namespace: namespace, Name: name},
			}}
		}
	}

	var list cclusternvidiacomv1alpha1.CCLInfraPlaneList
	if err := r.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list CCLInfraPlanes for Secret",
			"name", obj.GetName(), "namespace", obj.GetNamespace())
		return nil
	}

	var requests []reconcile.Request
	for _, infraPlane := range list.Items {
		if infraPlane.Spec.ClusterCredentialsRef.Name != obj.GetName() {
			continue
		}
		requests = append(requests, reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(&infraPlane),
		})
	}
	return requests
}

// SetupWithManager sets up the controller with the Manager.
//
// The Secret watch is unfiltered, so the manager caches every Secret in the cluster. If
// that becomes costly, scope it with cache.ByObject in internal/manager.
func (r *CCLInfraPlaneReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cclusternvidiacomv1alpha1.CCLInfraPlane{}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.infraPlanesForSecret)).
		Named("cclinfraplane").
		Complete(r)
}
