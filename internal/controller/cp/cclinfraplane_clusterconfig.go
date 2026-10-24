/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
	"github.com/NVIDIA/ccluster/internal/remote"
)

// The ClusterConfigApplied condition and its reasons.
const (
	// conditionTypeClusterConfigApplied reports on the CCLClusterConfig singleton the
	// operator writes into the infra-plane cluster.
	conditionTypeClusterConfigApplied = "ClusterConfigApplied"

	reasonClusterConfigApplied    = "Applied"
	reasonDomainMissing           = "DomainMissing"
	reasonClusterConfigCRDMissing = "CRDMissing"
	reasonClusterConfigConflict   = "Conflict"
	reasonClusterUnreachable      = "ClusterUnreachable"
	reasonUnsupportedCredentials  = "UnsupportedCredentials"
	reasonInvalidCredentials      = "InvalidCredentials"
)

// clusterConfigRetryInterval is how soon a CCLClusterConfig that could not be written is
// tried again. Nothing on the infra-plane cluster is watched, so a fix there - the plane
// coming back, the infra-plane operator and its CRD being installed - is only noticed by
// polling.
const clusterConfigRetryInterval = time.Minute

// errClusterConfigConflict reports that the infra-plane cluster's CCLClusterConfig was
// written for a different CCLInfraPlane: two CCLInfraPlanes register the same cluster.
var errClusterConfigConflict = errors.New("CCLClusterConfig belongs to another CCLInfraPlane")

// reconcileClusterConfig writes the CCLClusterConfig singleton into the infra-plane cluster
// that source holds the credentials for, and returns the ClusterConfigApplied condition and
// when to look again. Failures are reported in the condition, not returned: none of them is
// fixed by an immediate retry.
func (r *CCLInfraPlaneReconciler) reconcileClusterConfig(
	ctx context.Context,
	infraPlane *cclusternvidiacomv1alpha1.CCLInfraPlane,
	source *corev1.Secret,
) (metav1.Condition, ctrl.Result) {
	log := logf.FromContext(ctx)

	condition := func(status metav1.ConditionStatus, reason, message string) metav1.Condition {
		return metav1.Condition{
			Type:               conditionTypeClusterConfigApplied,
			Status:             status,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: infraPlane.Generation,
		}
	}

	if infraPlane.Spec.Domain == "" {
		// Only a CCLInfraPlane stored before spec.domain was required gets here. Setting
		// the domain is a spec change, which re-triggers reconciliation.
		return condition(metav1.ConditionFalse, reasonDomainMissing,
			"spec.domain is not set"), ctrl.Result{}
	}

	op, err := r.applyClusterConfig(ctx, infraPlane, source)
	switch {
	case err == nil:
		if op != controllerutil.OperationResultNone {
			log.Info("Applied CCLClusterConfig on infra-plane cluster",
				"name", cclusternvidiacomv1alpha1.CCLClusterConfigName,
				"domain", infraPlane.Spec.Domain, "operation", op)
		}
		return condition(metav1.ConditionTrue, reasonClusterConfigApplied, fmt.Sprintf(
				"CCLClusterConfig %q on the infra-plane cluster has domain %q",
				cclusternvidiacomv1alpha1.CCLClusterConfigName, infraPlane.Spec.Domain)),
			ctrl.Result{RequeueAfter: r.ResyncPeriod}

	case errors.Is(err, remote.ErrUnsupportedCredentials):
		// No requeue: the Secret watch re-triggers reconciliation once it changes.
		log.Info("Skipped CCLClusterConfig for unsupported credentials",
			"secret", source.Name, "reason", err.Error())
		return condition(metav1.ConditionFalse, reasonUnsupportedCredentials, err.Error()),
			ctrl.Result{}

	case errors.Is(err, remote.ErrInvalidCredentials):
		log.Info("Skipped CCLClusterConfig for invalid credentials",
			"secret", source.Name, "reason", err.Error())
		return condition(metav1.ConditionFalse, reasonInvalidCredentials, err.Error()),
			ctrl.Result{}

	case errors.Is(err, errClusterConfigConflict):
		log.Info("Refused to overwrite CCLClusterConfig owned by another CCLInfraPlane",
			"name", cclusternvidiacomv1alpha1.CCLClusterConfigName)
		return condition(metav1.ConditionFalse, reasonClusterConfigConflict, fmt.Sprintf(
				"CCLClusterConfig %q on the infra-plane cluster belongs to another CCLInfraPlane",
				cclusternvidiacomv1alpha1.CCLClusterConfigName)),
			ctrl.Result{RequeueAfter: r.ResyncPeriod}

	case apimeta.IsNoMatchError(err):
		log.Info("CCLClusterConfig CRD not installed on infra-plane cluster, skipped CCLClusterConfig")
		return condition(metav1.ConditionFalse, reasonClusterConfigCRDMissing,
				"the CCLClusterConfig CRD is not installed on the infra-plane cluster; "+
					"install the ccluster-ip chart there"),
			ctrl.Result{RequeueAfter: clusterConfigRetryInterval}

	default:
		log.Error(err, "Failed to apply CCLClusterConfig on infra-plane cluster",
			"name", cclusternvidiacomv1alpha1.CCLClusterConfigName)
		return condition(metav1.ConditionFalse, reasonClusterUnreachable, err.Error()),
			ctrl.Result{RequeueAfter: clusterConfigRetryInterval}
	}
}

// applyClusterConfig creates or updates the infra-plane cluster's CCLClusterConfig so its
// spec carries infraPlane's domain. Like the mirrored Secret, it is stamped with
// infraPlaneOwner, and one claimed by another CCLInfraPlane is left alone.
func (r *CCLInfraPlaneReconciler) applyClusterConfig(
	ctx context.Context,
	infraPlane *cclusternvidiacomv1alpha1.CCLInfraPlane,
	source *corev1.Secret,
) (controllerutil.OperationResult, error) {
	remoteClient, err := r.RemoteClients.ClientFor(source)
	if err != nil {
		return controllerutil.OperationResultNone, err
	}

	config := &cclusternvidiacomv1alpha1.CCLClusterConfig{
		ObjectMeta: metav1.ObjectMeta{Name: cclusternvidiacomv1alpha1.CCLClusterConfigName},
	}
	// Update, not a patch, writes only the spec and metadata: status belongs to the
	// infra-plane operator and is a separate subresource.
	return controllerutil.CreateOrUpdate(ctx, remoteClient, config, func() error {
		if !infraPlaneOwner.ownedBy(config, infraPlane) {
			return errClusterConfigConflict
		}
		infraPlaneOwner.stamp(config, infraPlane)
		config.Spec.Domain = infraPlane.Spec.Domain
		return nil
	})
}

// deleteClusterConfig deletes the infra-plane cluster's CCLClusterConfig if infraPlane
// wrote it. It never fails the finalizer: an infra-plane cluster that is gone or
// unreachable must not keep its CCLInfraPlane from being deleted, so every failure is
// logged and skipped.
func (r *CCLInfraPlaneReconciler) deleteClusterConfig(
	ctx context.Context,
	infraPlane *cclusternvidiacomv1alpha1.CCLInfraPlane,
) {
	log := logf.FromContext(ctx).WithValues("name", cclusternvidiacomv1alpha1.CCLClusterConfigName)

	source, err := r.teardownCredentials(ctx, infraPlane)
	if err != nil {
		log.Info("Skipped deleting CCLClusterConfig without credentials Secret",
			"secret", infraPlane.Spec.ClusterCredentialsRef.Name, "reason", err.Error())
		return
	}

	remoteClient, err := r.RemoteClients.ClientFor(source)
	if err != nil {
		log.Info("Skipped deleting CCLClusterConfig with unusable credentials",
			"secret", source.Name, "secretNamespace", source.Namespace, "reason", err.Error())
		return
	}

	var config cclusternvidiacomv1alpha1.CCLClusterConfig
	err = remoteClient.Get(ctx, client.ObjectKey{Name: cclusternvidiacomv1alpha1.CCLClusterConfigName}, &config)
	switch {
	case apierrors.IsNotFound(err), apimeta.IsNoMatchError(err):
		// Already gone, or never could have existed.
		return
	case err != nil:
		log.Error(err, "Failed to read CCLClusterConfig on infra-plane cluster, left it in place")
		return
	case !infraPlaneOwner.ownedBy(&config, infraPlane):
		log.Info("Left CCLClusterConfig owned by another CCLInfraPlane in place")
		return
	}

	if err := remoteClient.Delete(ctx, &config); client.IgnoreNotFound(err) != nil {
		log.Error(err, "Failed to delete CCLClusterConfig on infra-plane cluster, left it in place")
		return
	}
	log.Info("Deleted CCLClusterConfig on infra-plane cluster")
}

// teardownCredentials returns the Secret to reach infraPlane's cluster with during
// finalization: the credentials Secret, or else the mirrored Secret if it is still ours.
// Deleting the credentials Secret right after its CCLInfraPlane, as `ccluster unregister`
// does, usually beats the finalizer to it. The mirror carries the same data and outlives
// this step, because the finalizer deletes it afterwards; a CCLClusterConfig is only ever
// written after the mirror was.
func (r *CCLInfraPlaneReconciler) teardownCredentials(
	ctx context.Context,
	infraPlane *cclusternvidiacomv1alpha1.CCLInfraPlane,
) (*corev1.Secret, error) {
	var source corev1.Secret
	key := client.ObjectKey{Namespace: infraPlane.Namespace, Name: infraPlane.Spec.ClusterCredentialsRef.Name}
	err := r.Get(ctx, key, &source)
	if !apierrors.IsNotFound(err) {
		return &source, err
	}

	var mirror corev1.Secret
	key = client.ObjectKey{Namespace: r.ArgoCDNamespace, Name: argoCDClusterName(infraPlane.Namespace, infraPlane.Name)}
	if mirrorErr := r.Get(ctx, key, &mirror); mirrorErr != nil {
		return nil, errors.Join(err, mirrorErr)
	}
	if !infraPlaneOwner.ownedBy(&mirror, infraPlane) {
		return nil, errors.Join(err, errClusterSecretConflict)
	}
	return &mirror, nil
}
