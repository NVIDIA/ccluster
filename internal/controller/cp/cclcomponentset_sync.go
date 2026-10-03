/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

import (
	"context"
	"errors"
	"slices"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cclv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
	argocdv1alpha1 "github.com/NVIDIA/ccluster/internal/argocd/v1alpha1"
)

var errApplicationSyncInProgress = errors.New("application has a pending or running operation")

func operationCompleted(phase string) bool {
	return phase == argocdv1alpha1.OperationSucceeded ||
		phase == argocdv1alpha1.OperationFailed || phase == argocdv1alpha1.OperationError
}

func operationInProgress(app *argocdv1alpha1.Application) bool {
	return app.Operation != nil || (app.OperationPhase() != "" && !operationCompleted(app.OperationPhase()))
}

func syncRecord(set *cclv1alpha1.CCLComponentSet, name string) *cclv1alpha1.ComponentSyncStatus {
	for i := range set.Status.ComponentSyncs {
		if set.Status.ComponentSyncs[i].Name == name {
			return &set.Status.ComponentSyncs[i]
		}
	}
	return nil
}

func rememberSync(set *cclv1alpha1.CCLComponentSet, record cclv1alpha1.ComponentSyncStatus) {
	if previous := syncRecord(set, record.Name); previous != nil {
		*previous = record
		return
	}
	set.Status.ComponentSyncs = append(set.Status.ComponentSyncs, record)
}

func requestMatches(app *argocdv1alpha1.Application, record *cclv1alpha1.ComponentSyncStatus) bool {
	return record != nil && record.RequestID != "" && record.ApplicationUID == string(app.UID) &&
		record.RequestID == app.Annotations[annotationSyncRequestID]
}

func observeSync(app *argocdv1alpha1.Application, record *cclv1alpha1.ComponentSyncStatus) {
	if !requestMatches(app, record) {
		return
	}
	observedID, _ := app.OperationInfo(annotationSyncRequestID)
	if observedID != record.RequestID || app.OperationPhase() == "" {
		return
	}
	finishedAt := app.Status.OperationState.FinishedAt
	if record.FinishedAt != nil && (finishedAt == nil || finishedAt.Before(record.FinishedAt)) {
		// A cached Running phase or an older delivery attempt cannot erase a receipt.
		return
	}
	record.Phase = app.OperationPhase()
	record.Message = app.OperationMessage()
	record.FinishedAt = finishedAt.DeepCopy()
}

func (r *CCLComponentSetReconciler) observeSyncs(
	set *cclv1alpha1.CCLComponentSet, apps map[string]*argocdv1alpha1.Application,
) {
	for _, component := range set.Spec.Components {
		app := apps[applicationName(set.Name, component.Name)]
		if app != nil && equality.Semantic.DeepEqual(app.Spec, r.applicationSpec(set, component)) {
			observeSync(app, syncRecord(set, component.Name))
		}
	}
}

func (r *CCLComponentSetReconciler) pruneSyncRecords(set *cclv1alpha1.CCLComponentSet) {
	set.Status.ComponentSyncs = slices.DeleteFunc(set.Status.ComponentSyncs, func(record cclv1alpha1.ComponentSyncStatus) bool {
		return !slices.ContainsFunc(set.Spec.Components, func(component cclv1alpha1.ComponentSpec) bool {
			return component.Name == record.Name
		})
	})
}

// currentSyncPhase checks both the desired input and the independently tracked request.
// Old cache entries cannot establish completion by matching their own old annotations.
func (r *CCLComponentSetReconciler) currentSyncPhase(
	set *cclv1alpha1.CCLComponentSet, component cclv1alpha1.ComponentSpec, app *argocdv1alpha1.Application,
) (string, string) {
	if !equality.Semantic.DeepEqual(app.Spec, r.applicationSpec(set, component)) {
		return "", ""
	}
	record := syncRecord(set, component.Name)
	if app.Annotations[annotationSyncRequestID] == "" && record == nil {
		// Existing Applications join request tracking on their next spec change.
		return argocdv1alpha1.OperationSucceeded, ""
	}
	if !requestMatches(app, record) {
		return "", ""
	}
	return record.Phase, record.Message
}

// healthObservedAfterSync requires a comparison that started after the tracked sync
// and any later operation. Strict ordering also waits out timestamp truncation to seconds.
// This is a freshness barrier, not an Argo CD proof of the comparison's input identity.
func healthObservedAfterSync(app *argocdv1alpha1.Application, record *cclv1alpha1.ComponentSyncStatus) bool {
	if operationInProgress(app) {
		return false
	}
	var finishedAt *metav1.Time
	if record != nil {
		if record.FinishedAt == nil {
			return false
		}
		finishedAt = record.FinishedAt
	}
	if app.Status != nil && app.Status.OperationState != nil {
		latest := app.Status.OperationState.FinishedAt
		if latest != nil && (finishedAt == nil || latest.After(finishedAt.Time)) {
			finishedAt = latest
		}
	}
	if finishedAt == nil {
		return true
	}
	return app.Status != nil && app.Status.ReconciledAt != nil && app.Status.ReconciledAt.After(finishedAt.Time)
}

func (r *CCLComponentSetReconciler) finished(
	set *cclv1alpha1.CCLComponentSet, component cclv1alpha1.ComponentSpec, app *argocdv1alpha1.Application,
) bool {
	phase, _ := r.currentSyncPhase(set, component, app)
	return phase == argocdv1alpha1.OperationSucceeded &&
		healthObservedAfterSync(app, syncRecord(set, component.Name)) &&
		app.HealthStatus() == argocdv1alpha1.HealthStatusHealthy && app.SyncStatus() == argocdv1alpha1.SyncStatusSynced
}

// needsSync distinguishes a missing receipt from a receipt replaced by self-heal.
// Delivery retries retain the request ID and are bounded separately from Argo CD retries.
func needsSync(app *argocdv1alpha1.Application, specChanged bool, record *cclv1alpha1.ComponentSyncStatus, now time.Time) bool {
	if operationInProgress(app) {
		return false
	}
	if specChanged {
		return true
	}
	if !requestMatches(app, record) || operationCompleted(record.Phase) {
		return false
	}
	observedID, _ := app.OperationInfo(annotationSyncRequestID)
	if observedID == record.RequestID {
		return false
	}
	return record.RequestedAt == nil || !now.Before(record.RequestedAt.Add(syncRequestRetryInterval))
}

func (r *CCLComponentSetReconciler) requestSyncObservations(
	ctx context.Context, set *cclv1alpha1.CCLComponentSet, apps map[string]*argocdv1alpha1.Application,
) error {
	for _, component := range set.Spec.Components {
		app := apps[applicationName(set.Name, component.Name)]
		if app == nil || operationInProgress(app) {
			continue
		}
		phase, _ := r.currentSyncPhase(set, component, app)
		if phase != argocdv1alpha1.OperationSucceeded || healthObservedAfterSync(app, syncRecord(set, component.Name)) {
			continue
		}
		if app.Annotations[annotationAppRefresh] == refreshTypeHard {
			continue
		}
		base := app.DeepCopy()
		if app.Annotations == nil {
			app.Annotations = make(map[string]string)
		}
		app.Annotations[annotationAppRefresh] = refreshTypeHard
		if err := client.IgnoreNotFound(r.Patch(ctx, app, client.MergeFrom(base))); err != nil {
			return err
		}
	}
	return nil
}

// Watches drive ordinary health changes. Timers only backstop request delivery,
// busy operations and the post-sync freshness barrier; terminal failures do not poll.
func (r *CCLComponentSetReconciler) syncRequeueAfter(
	set *cclv1alpha1.CCLComponentSet, apps map[string]*argocdv1alpha1.Application,
) time.Duration {
	for _, component := range set.Spec.Components {
		app := apps[applicationName(set.Name, component.Name)]
		record := syncRecord(set, component.Name)
		if record != nil && !operationCompleted(record.Phase) {
			return syncObservationRetryInterval
		}
		if app == nil {
			continue
		}
		if !equality.Semantic.DeepEqual(app.Spec, r.applicationSpec(set, component)) && operationInProgress(app) {
			return syncObservationRetryInterval
		}
		phase, _ := r.currentSyncPhase(set, component, app)
		if phase == argocdv1alpha1.OperationSucceeded && !healthObservedAfterSync(app, record) {
			return syncObservationRetryInterval
		}
	}
	return 0
}
