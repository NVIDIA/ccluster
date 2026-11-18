/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package v1alpha1

import (
	"encoding/json"

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

// Application is a partial mirror of the Argo CD Application API.
//
// It models fields the control-plane operator writes or reads for rollout decisions.
// The operator patches metadata and spec when they differ, and never writes operation
// or status, which Argo CD owns. Replacing spec removes fields the operator does not
// write, so ownership must be checked first. A whole-object update would discard
// unmodelled fields and must not be used.
//
// See https://argo-cd.readthedocs.io/en/stable/operator-manual/declarative-setup/ for the
// full API.
//
// +kubebuilder:object:root=true
type Application struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ApplicationSpec `json:"spec,omitempty"`

	// operation is a sync Argo CD has been asked to run, by its automated sync or by a
	// user. Argo CD copies it into status.operationState.operation and keeps this field
	// set until the operation reaches a terminal phase, so it is set while the operation
	// runs and retries, not only while it is pending.
	//
	// Read, never written. A decoded operation holds only the fields below.
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

// OperationCompleted reports whether phase is terminal.
func OperationCompleted(phase string) bool {
	return phase == OperationSucceeded || phase == OperationFailed || phase == OperationError
}

// OperationInProgress reports whether a request is pending or an operation is nonterminal.
func (a *Application) OperationInProgress() bool {
	return a.Operation != nil || (a.OperationPhase() != "" && !OperationCompleted(a.OperationPhase()))
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

	// ignoreDifferences are fields Argo CD leaves out of its diff, for fields another
	// controller on the destination cluster owns by design.
	IgnoreDifferences []ResourceIgnoreDifferences `json:"ignoreDifferences,omitempty"`
}

// ResourceIgnoreDifferences selects resources by group, kind and, optionally, name and
// namespace, and names the fields of theirs Argo CD does not diff. Every field Argo CD
// defines is modelled, so a rule reads back exactly as written.
type ResourceIgnoreDifferences struct {
	// group is the resource's API group; empty for the core group.
	Group string `json:"group,omitempty"`

	// kind is the resource's kind.
	Kind string `json:"kind"`

	// name restricts the rule to one resource. Empty matches every resource of the kind.
	Name string `json:"name,omitempty"`

	// namespace restricts the rule to one namespace. Empty for cluster-scoped resources.
	Namespace string `json:"namespace,omitempty"`

	// jsonPointers are RFC 6901 pointers to the ignored fields.
	JSONPointers []string `json:"jsonPointers,omitempty"`

	// jqPathExpressions are jq paths to the ignored fields.
	JQPathExpressions []string `json:"jqPathExpressions,omitempty"`

	// managedFieldsManagers ignores every field these field managers own.
	ManagedFieldsManagers []string `json:"managedFieldsManagers,omitempty"`
}

// ApplicationSource locates a chart in a Git repository. Argo CD also supports Helm and
// OCI repositories, which need different fields and are not modelled.
//
// Unmodelled keys are retained on read so source drift is visible in both spec and
// status.sync.comparedTo. Otherwise a field such as helm.valueFiles could change rendered
// manifests while the decoded source still matched the operator's desired source.
type ApplicationSource struct {
	// repoURL is the Git repository Argo CD's repo-server clones.
	RepoURL string `json:"repoURL"`

	// targetRevision is a branch, tag or commit SHA within it.
	TargetRevision string `json:"targetRevision,omitempty"`

	// path is the chart's directory, relative to the repository root.
	Path string `json:"path,omitempty"`

	// helm carries chart overrides.
	Helm *ApplicationSourceHelm `json:"helm,omitempty"`

	// Unmodelled retains other non-zero source fields for comparison. It is excluded
	// from writes so replacing spec removes source drift.
	Unmodelled map[string]apiextensionsv1.JSON `json:"-"`
}

// sourceKeys are the keys ApplicationSource models. UnmarshalJSON removes them from what
// it puts in Unmodelled, so adding a field above means adding it here.
var sourceKeys = []string{"repoURL", "targetRevision", "path", "helm"}

func (s *ApplicationSource) UnmarshalJSON(data []byte) error {
	type plain ApplicationSource
	if err := json.Unmarshal(data, (*plain)(s)); err != nil {
		return err
	}
	if s.Helm != nil && s.Helm.Values == "" && s.Helm.Unmodelled == nil {
		// Normalize an empty helm block to the absent block used by the operator.
		s.Helm = nil
	}
	rest, err := unmodelledKeys(data, sourceKeys)
	s.Unmodelled = rest
	return err
}

// ApplicationSourceHelm holds Helm-specific overrides for an Application's source.
type ApplicationSourceHelm struct {
	// values is a YAML document of Helm values, serialized with the conversion Argo CD
	// uses for valuesObject. A string preserves explicit nulls in status.sync.comparedTo;
	// Argo CD's status merge patch removes null-valued keys from an object.
	Values string `json:"values,omitempty"`

	// Unmodelled retains other non-zero Helm fields, including valueFiles and parameters.
	// See ApplicationSource.Unmodelled.
	Unmodelled map[string]apiextensionsv1.JSON `json:"-"`
}

// helmKeys are the keys ApplicationSourceHelm models.
var helmKeys = []string{"values"}

func (h *ApplicationSourceHelm) UnmarshalJSON(data []byte) error {
	type plain ApplicationSourceHelm
	if err := json.Unmarshal(data, (*plain)(h)); err != nil {
		return err
	}
	rest, err := unmodelledKeys(data, helmKeys)
	h.Unmodelled = rest
	return err
}

// unmodelledKeys returns non-zero JSON fields absent from modelled, or nil if none remain.
//
// Argo CD omits zero-valued optional source fields. Ignoring them also prevents zero
// defaults added by a future CRD from causing repeated spec writes.
//
// Empty objects are retained because kustomize and plugin blocks select the source type.
func unmodelledKeys(data []byte, modelled []string) (map[string]apiextensionsv1.JSON, error) {
	var all map[string]apiextensionsv1.JSON
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, err
	}
	for _, key := range modelled {
		delete(all, key)
	}
	for key, value := range all {
		if isZeroJSON(value.Raw) {
			delete(all, key)
		}
	}
	if len(all) == 0 {
		return nil, nil
	}
	return all, nil
}

// isZeroJSON reports whether raw holds the zero value of its type. Unparseable bytes are
// not zero: this decides whether to ignore a field, and an unreadable one is not ignored.
func isZeroJSON(raw []byte) bool {
	// apiextensionsv1.JSON leaves Raw nil for a literal null, which is the absent field.
	if len(raw) == 0 {
		return true
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return false
	}
	switch value := value.(type) {
	case nil:
		return true
	case bool:
		return !value
	case float64:
		return value == 0
	case string:
		return value == ""
	case []any:
		return len(value) == 0
	}
	return false
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
}

// SyncOperation is the sync half of an Operation.
type SyncOperation struct {
	// source is the source the sync was asked for, including Helm values. Argo CD's
	// automated sync fills it in for a single-source Application. It attributes an
	// operation that failed before Argo CD recorded a sync result.
	Source *ApplicationSource `json:"source,omitempty"`
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

	// message describes operation progress, retries or the final result.
	Message string `json:"message,omitempty"`

	// syncResult records the source and resolved Git revision this operation synced.
	// Nil if the operation failed before Argo CD initialized the sync result.
	SyncResult *SyncOperationResult `json:"syncResult,omitempty"`

	// finishedAt marks completion, or the failed attempt while Argo CD waits to retry.
	// Only a terminal phase establishes that the operation has completed.
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
}

// SyncOperationResult identifies the manifests an operation tried to apply.
type SyncOperationResult struct {
	Source   ApplicationSource `json:"source,omitempty"`
	Revision string            `json:"revision,omitempty"`
}

// ApplicationHealthStatus is the health half of an Application's status. Argo CD also
// declares a message here and marks it deprecated; v3.5.1 never writes one, so the detail
// behind an unhealthy Application is only in status.resources, which is not modelled.
type ApplicationHealthStatus struct {
	Status string `json:"status,omitempty"`
}

// ApplicationSyncStatus is the sync half of an Application's status.
//
// Argo CD assigns status and comparedTo together from one comparison, so status describes
// the recorded source, destination and ignoreDifferences.
type ApplicationSyncStatus struct {
	// status is the sync state. Argo CD's CRD requires it whenever status.sync is
	// present, so it carries no omitempty.
	Status string `json:"status"`

	// comparedTo is the input of the comparison that produced status.
	ComparedTo ComparedTo `json:"comparedTo,omitempty"`

	// revision is the resolved Git revision used by the comparison.
	Revision string `json:"revision,omitempty"`
}

// ComparedTo is what Argo CD compared the live state against. Argo CD also records
// sources here; the operator writes a single source, so that is not modelled.
type ComparedTo struct {
	// source is the source of the compared manifests.
	Source ApplicationSource `json:"source,omitempty"`

	// destination is the cluster and namespace they were compared in.
	Destination ApplicationDestination `json:"destination,omitempty"`

	// ignoreDifferences are the rules the comparison left out of its diff.
	IgnoreDifferences []ResourceIgnoreDifferences `json:"ignoreDifferences,omitempty"`
}

// ApplicationList contains a list of Application.
//
// +kubebuilder:object:root=true
type ApplicationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Application `json:"items"`
}
