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
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
)

// Condition types and reasons reported on CCLInfraPlane.status.
const (
	conditionTypeAvailable = "Available"

	reasonCredentialsResolved = "CredentialsResolved"
	reasonSecretNotFound      = "SecretNotFound"
	reasonSecretInvalid       = "SecretInvalid"
)

// CCLInfraPlaneReconciler reconciles a CCLInfraPlane object
type CCLInfraPlaneReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=ccluster.nvidia.com,resources=cclinfraplanes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ccluster.nvidia.com,resources=cclinfraplanes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ccluster.nvidia.com,resources=cclinfraplanes/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile resolves the Secret referenced by the CCLInfraPlane's
// clusterCredentialsRef, validates that it carries the credentials needed to reach the
// infra-plane cluster's API server, and records the outcome as an Available condition.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.1/pkg/reconcile
func (r *CCLInfraPlaneReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var infraPlane cclusternvidiacomv1alpha1.CCLInfraPlane
	if err := r.Get(ctx, req.NamespacedName, &infraPlane); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Ignored missing CCLInfraPlane", "name", req.Name, "namespace", req.Namespace)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	secretName := infraPlane.Spec.ClusterCredentialsRef.Name

	var secret corev1.Secret
	key := client.ObjectKey{Namespace: infraPlane.Namespace, Name: secretName}
	if err := r.Get(ctx, key, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			// No requeue: the Secret watch re-triggers reconciliation once it appears.
			log.Info("Missing credentials Secret", "secret", secretName)
			return ctrl.Result{}, r.setAvailable(ctx, &infraPlane, metav1.ConditionFalse,
				reasonSecretNotFound, fmt.Sprintf("Secret %q not found", secretName))
		}
		return ctrl.Result{}, err
	}

	server, missing := credentialsFrom(&secret)
	if len(missing) > 0 {
		// No requeue: an edit to the Secret re-triggers reconciliation.
		log.Info("Rejected credentials Secret", "secret", secretName, "missingKeys", missing)
		return ctrl.Result{}, r.setAvailable(ctx, &infraPlane, metav1.ConditionFalse,
			reasonSecretInvalid, fmt.Sprintf("Secret %q is missing keys: %s",
				secretName, strings.Join(missing, ", ")))
	}

	// Only the server URL is logged; the bearer token and CA certificate must never
	// reach the logs.
	log.Info("Resolved infra-plane credentials", "secret", secretName, "server", server)

	return ctrl.Result{}, r.setAvailable(ctx, &infraPlane, metav1.ConditionTrue,
		reasonCredentialsResolved, fmt.Sprintf("Resolved credentials from Secret %q", secretName))
}

// credentialsFrom returns the server URL held in secret, along with the names of any
// required keys that are absent or empty.
func credentialsFrom(secret *corev1.Secret) (server string, missing []string) {
	for _, k := range []string{
		cclusternvidiacomv1alpha1.CredentialsKeyServer,
		cclusternvidiacomv1alpha1.CredentialsKeyBearerToken,
		cclusternvidiacomv1alpha1.CredentialsKeyCACert,
	} {
		if len(secret.Data[k]) == 0 {
			missing = append(missing, k)
		}
	}
	return string(secret.Data[cclusternvidiacomv1alpha1.CredentialsKeyServer]), missing
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

// infraPlanesForSecret maps a Secret to the CCLInfraPlanes in its namespace that
// reference it.
func (r *CCLInfraPlaneReconciler) infraPlanesForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
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
