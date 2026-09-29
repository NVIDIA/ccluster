/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package v1alpha1

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ResourcesFinalizer makes deleting an Argo CD Application also delete the resources it
// deployed. Argo CD treats an Application without it as a record to drop rather than a
// deployment to tear down, so removing one silently orphans its workloads on the
// destination cluster.
const ResourcesFinalizer = "resources-finalizer.argocd.argoproj.io"

// Health and sync status values the operator compares against. Argo CD defines more of
// each; only the ones the rollout report distinguishes are named here.
const (
	HealthStatusHealthy     = "Healthy"
	HealthStatusProgressing = "Progressing"
	HealthStatusDegraded    = "Degraded"

	SyncStatusSynced = "Synced"

	// Terminal phases of a sync operation. Any other phase - Running, Terminating -
	// means it has not stopped yet.
	OperationSucceeded = "Succeeded"
	OperationFailed    = "Failed"
	OperationError     = "Error"
)

// Application is an Argo CD Application: one chart deployed to one cluster.
//
// Only the fields the control-plane operator writes are modelled, so this type is not a
// faithful copy of Argo CD's - most of the spec and nearly all of the status are absent.
// That is safe only because the operator owns the Applications it creates outright and
// rewrites their spec on every reconcile: reading one of these into this type and writing
// it back **deletes** any field not declared below. The ownership check in
// internal/controller/cp is what keeps that confined to Applications the operator
// authored. Do not use this type to modify an Application it did not create.
//
// See https://argo-cd.readthedocs.io/en/stable/operator-manual/declarative-setup/ for the
// full API.
//
// +kubebuilder:object:root=true
type Application struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ApplicationSpec `json:"spec,omitempty"`

	// operation requests a sync. Argo CD copies it into status.operationState.operation and
	// then clears this field, so it is a one-shot request rather than desired state. The
	// operator sets it to tie Argo CD's next verdict to the spec it just wrote.
	//
	// It gets its own patch path. A read of an operation Argo CD's own auto-sync requested
	// decodes only the fields below, so a decoded one must never be written back.
	Operation *Operation `json:"operation,omitempty"`

	// status is read, never written: Argo CD owns it.
	//
	// A pointer so that an Application the operator is creating serialises without a
	// status at all. Argo CD's CRD marks status.sync.status required, so sending the
	// zero value - which a non-pointer struct always does, omitempty or not - is
	// rejected outright.
	Status *ApplicationStatus `json:"status,omitempty"`
}

// HealthStatus and SyncStatus read through a status Argo CD may not have written yet.
func (a *Application) HealthStatus() string {
	if a.Status == nil {
		return ""
	}
	return a.Status.Health.Status
}

func (a *Application) SyncStatus() string {
	if a.Status == nil {
		return ""
	}
	return a.Status.Sync.Status
}

// OperationPhase and OperationMessage read through an Application that has never synced.
func (a *Application) OperationPhase() string {
	if a.Status == nil || a.Status.OperationState == nil {
		return ""
	}
	return a.Status.OperationState.Phase
}

func (a *Application) OperationMessage() string {
	if a.Status == nil || a.Status.OperationState == nil {
		return ""
	}
	return a.Status.OperationState.Message
}

// OperationInfo returns what Argo CD echoed back for one info item of the last sync.
// Argo CD sets no info on the syncs it starts itself, so a hit means the operator asked
// for this one.
func (a *Application) OperationInfo(name string) (string, bool) {
	if a.Status == nil || a.Status.OperationState == nil {
		return "", false
	}
	for _, info := range a.Status.OperationState.Operation.Info {
		if info.Name == name {
			return info.Value, true
		}
	}
	return "", false
}

// ApplicationSpec is the desired state of an Argo CD Application.
type ApplicationSpec struct {
	// project is the Argo CD AppProject the Application belongs to.
	Project string `json:"project"`

	// source is where the Application's manifests come from.
	Source ApplicationSource `json:"source"`

	// destination is the cluster and namespace they are applied to.
	Destination ApplicationDestination `json:"destination"`

	SyncPolicy *SyncPolicy `json:"syncPolicy,omitempty"`
}

// ApplicationSource locates a chart in a Git repository. Argo CD also supports Helm and
// OCI repositories, which need different fields and are not modelled.
type ApplicationSource struct {
	// repoURL is the Git repository Argo CD's repo-server clones.
	RepoURL string `json:"repoURL"`

	// targetRevision is a branch, tag or commit SHA within it.
	TargetRevision string `json:"targetRevision,omitempty"`

	// path is the chart's directory, relative to the repository root.
	Path string `json:"path,omitempty"`

	// helm carries chart overrides.
	Helm *ApplicationSourceHelm `json:"helm,omitempty"`
}

// ApplicationSourceHelm holds Helm-specific overrides for an Application's source.
type ApplicationSourceHelm struct {
	// valuesObject is structured Helm values. Argo CD also accepts `values`, a string
	// holding a YAML document; the structured form is used because the operator copies
	// a CCLComponentSet component's values straight through, and passing them as an
	// object avoids serialising and re-parsing what the API server already validated.
	// +kubebuilder:pruning:PreserveUnknownFields
	ValuesObject *apiextensionsv1.JSON `json:"valuesObject,omitempty"`
}

// ApplicationDestination is where an Application is deployed. AppProject reuses it to say
// where Applications *may* be deployed, with "*" as a wildcard in any field.
type ApplicationDestination struct {
	// name is the cluster's name as Argo CD knows it, which is what the operator's
	// mirrored cluster Secret defines. Applications address their cluster this way,
	// because the name is the one identifier the operator has to agree with itself on.
	Name string `json:"name,omitempty"`

	// server is the cluster's API server URL, the alternative to name. The operator
	// never sets it on an Application; it appears in AppProject destinations as the
	// wildcard "*".
	Server string `json:"server,omitempty"`

	// namespace is the namespace on that cluster.
	Namespace string `json:"namespace,omitempty"`
}

// SyncPolicy controls when and how Argo CD applies an Application.
type SyncPolicy struct {
	// automated enables syncing without human approval. Nil means manual sync only.
	Automated *SyncPolicyAutomated `json:"automated,omitempty"`

	// syncOptions are Argo CD's per-Application sync flags, e.g. "CreateNamespace=true".
	SyncOptions []string `json:"syncOptions,omitempty"`
}

// SyncPolicyAutomated configures automated syncing.
type SyncPolicyAutomated struct {
	// prune deletes resources that have disappeared from the source.
	Prune bool `json:"prune,omitempty"`

	// selfHeal reverts changes made to live resources outside Argo CD.
	SelfHeal bool `json:"selfHeal,omitempty"`
}

// Operation is a sync Argo CD has been asked to run.
type Operation struct {
	// sync is required: Argo CD fails an operation that does not name one.
	Sync *SyncOperation `json:"sync,omitempty"`

	// initiatedBy records who asked. Argo CD has an `automated` flag here too, left
	// unmodelled on purpose - it force-resolves the revision after a sync that is not
	// automated, which is what stops a just-synced Application reporting OutOfSync.
	InitiatedBy OperationInitiator `json:"initiatedBy,omitempty"`

	// info is free-form and copied verbatim into status.operationState.operation.info. It
	// is the only thing Argo CD hands back unchanged, and so the only way the operator can
	// recognise its own sync.
	Info []Info `json:"info,omitempty"`

	// retry lets Argo CD back a failing sync off itself. The phase stays Running while it
	// does, so the operator sees one long attempt rather than a flapping one.
	Retry RetryStrategy `json:"retry,omitempty"`
}

// SyncOperation is the sync half of an Operation.
//
// Argo CD reads none of this from spec.syncPolicy - the sync it starts itself copies the
// policy across field by field, and so must the operator. A revision is deliberately not
// modelled: without one Argo CD uses spec.source.targetRevision, which is the revision the
// operator set in the first place.
type SyncOperation struct {
	// prune must repeat spec.syncPolicy.automated.prune.
	Prune bool `json:"prune,omitempty"`

	// syncOptions must repeat spec.syncPolicy.syncOptions. Leaving them out drops
	// ServerSideApply, which this catalog cannot sync without - see DESIGN.md.
	SyncOptions []string `json:"syncOptions,omitempty"`
}

// OperationInitiator names who asked for an operation.
type OperationInitiator struct {
	Username string `json:"username,omitempty"`
}

// RetryStrategy bounds how often Argo CD retries a failed sync. Its backoff is not
// modelled: unset is Argo CD's own default, which is what its auto-sync uses.
type RetryStrategy struct {
	Limit int64 `json:"limit,omitempty"`
}

// Info is a name/value pair carried on an operation and reported back with its state.
type Info struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ApplicationStatus is the observed state of an Application.
type ApplicationStatus struct {
	// health is Argo CD's assessment of the deployed resources.
	Health ApplicationHealthStatus `json:"health,omitempty"`

	// sync reports whether the live state matches the source revision.
	Sync ApplicationSyncStatus `json:"sync,omitempty"`

	// operationState is the last sync Argo CD ran, and the operation that asked for it.
	// Nil until one has run.
	OperationState *OperationState `json:"operationState,omitempty"`
}

// OperationState is how the sync an Operation asked for is going.
type OperationState struct {
	// operation is the request, copied across whole when the sync started.
	Operation Operation `json:"operation"`

	// phase is Running, Terminating, Succeeded, Failed or Error.
	Phase string `json:"phase"`

	// message explains a Failed or Error phase.
	Message string `json:"message,omitempty"`
}

// ApplicationHealthStatus is the health half of an Application's status. Argo CD also
// declares a message here and marks it deprecated; v3.5.1 never writes one, so the detail
// behind an unhealthy Application is only in status.resources, which is not modelled.
type ApplicationHealthStatus struct {
	Status string `json:"status,omitempty"`
}

// ApplicationSyncStatus is the sync half of an Application's status.
type ApplicationSyncStatus struct {
	// status is the sync state. Argo CD's CRD requires it whenever status.sync is
	// present, so it carries no omitempty.
	Status string `json:"status"`
}

// ApplicationList contains a list of Application.
//
// +kubebuilder:object:root=true
type ApplicationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Application `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Application{}, &ApplicationList{})
}
