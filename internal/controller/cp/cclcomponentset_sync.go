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
	"k8s.io/client-go/util/retry"
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
	if !requestMatches(app, record) || app.OperationPhase() == "" {
		return
	}
	observedID, _ := app.OperationInfo(annotationSyncRequestID)
	if observedID != record.RequestID {
		adoptLaterResult(app, record)
		return
	}
	finishedAt := app.Status.OperationState.FinishedAt
	if record.FinishedAt != nil && (finishedAt == nil || finishedAt.Before(record.FinishedAt)) {
		// A cached Running phase or an older retry cannot erase a receipt.
		return
	}
	record.Phase = app.OperationPhase()
	record.Message = app.OperationMessage()
	record.FinishedAt = finishedAt.DeepCopy()
}

// adoptLaterResult takes the result of an operation that ran after this request, once
// Argo CD has replaced the request's own result with it - a self-heal, a manual sync, or
// an auto-sync of a new catalog commit.
//
// The request ID and its operation are written in one patch, and Argo CD runs one
// operation at a time, so an idle Application that still carries this request ID records
// either this operation or a later one. Every later one synced this request's spec: a
// spec change would have replaced the request. Callers only observe an Application whose
// spec is the desired one.
//
// A success is never downgraded: a later self-heal must not erase it. Anything else is,
// which is what lets a sync in Argo CD retry a failed request without a spec edit.
// Completion still waits for a comparison after the adopted operation, and for Synced.
func adoptLaterResult(app *argocdv1alpha1.Application, record *cclv1alpha1.ComponentSyncStatus) {
	if record.Phase == argocdv1alpha1.OperationSucceeded || operationInProgress(app) {
		return
	}
	record.Phase = app.OperationPhase()
	record.Message = app.OperationMessage()
	record.FinishedAt = app.Status.OperationState.FinishedAt.DeepCopy()
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

// observationRefreshDue reports that a succeeded request is still waiting for a comparison
// that started after it, and that no refresh has been asked for yet.
func (r *CCLComponentSetReconciler) observationRefreshDue(
	set *cclv1alpha1.CCLComponentSet, component cclv1alpha1.ComponentSpec, app *argocdv1alpha1.Application,
) bool {
	if operationInProgress(app) || app.Annotations[annotationAppRefresh] != "" {
		return false
	}
	phase, _ := r.currentSyncPhase(set, component, app)
	return phase == argocdv1alpha1.OperationSucceeded &&
		!healthObservedAfterSync(app, syncRecord(set, component.Name))
}

// requestSyncObservations nudges Argo CD into the comparison healthObservedAfterSync is
// waiting for. Argo CD schedules one itself after every sync; this covers the case where
// that one landed within the same second the sync finished, which second-resolution
// timestamps cannot order.
//
// The patch carries a resource version, and Argo CD writes this object's status
// continuously, so a conflict is expected rather than exceptional. Each attempt reads
// again and re-judges: by then Argo CD may have refreshed on its own, and the annotation
// must not be forced on top of that.
func (r *CCLComponentSetReconciler) requestSyncObservations(
	ctx context.Context, set *cclv1alpha1.CCLComponentSet, apps map[string]*argocdv1alpha1.Application,
) error {
	for _, component := range set.Spec.Components {
		name := applicationName(set.Name, component.Name)
		if cached := apps[name]; cached == nil || !r.observationRefreshDue(set, component, cached) {
			continue
		}
		key := client.ObjectKey{Namespace: set.Namespace, Name: name}
		err := retry.OnError(retry.DefaultBackoff, staleRead, func() error {
			app := &argocdv1alpha1.Application{}
			if err := r.Get(ctx, key, app); err != nil {
				return client.IgnoreNotFound(err)
			}
			if !r.observationRefreshDue(set, component, app) {
				return nil
			}
			base := app.DeepCopy()
			if app.Annotations == nil {
				app.Annotations = make(map[string]string)
			}
			app.Annotations[annotationAppRefresh] = refreshTypeNormal
			return client.IgnoreNotFound(r.Patch(ctx, app,
				client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})))
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// Watches drive ordinary health changes. Timers backstop cache propagation, busy
// operations and the post-sync freshness barrier. Once Argo CD is idle, the request's own
// result or a later one is already recorded and observed on the same pass, so an idle
// request is not polled.
func (r *CCLComponentSetReconciler) syncRequeueAfter(
	set *cclv1alpha1.CCLComponentSet, apps map[string]*argocdv1alpha1.Application,
) time.Duration {
	for _, component := range set.Spec.Components {
		app := apps[applicationName(set.Name, component.Name)]
		record := syncRecord(set, component.Name)
		if record != nil && !operationCompleted(record.Phase) &&
			(app == nil || !requestMatches(app, record) || operationInProgress(app)) {
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
