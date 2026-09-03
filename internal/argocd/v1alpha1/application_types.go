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

	// status is read, never written: Argo CD owns it.
	//
	// A pointer so that an Application the operator is creating serialises without a
	// status at all. Argo CD's CRD marks status.sync.status required, so sending the
	// zero value - which a non-pointer struct always does, omitempty or not - is
	// rejected outright.
	Status *ApplicationStatus `json:"status,omitempty"`
}

// HealthStatus, SyncStatus and HealthMessage read through a status Argo CD may not have
// written yet.
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

func (a *Application) HealthMessage() string {
	if a.Status == nil {
		return ""
	}
	return a.Status.Health.Message
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
	// a CCLDeployment component's values straight through, and passing them as an
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

// ApplicationStatus is the observed state of an Application.
type ApplicationStatus struct {
	// health is Argo CD's assessment of the deployed resources.
	Health ApplicationHealthStatus `json:"health,omitempty"`

	// sync reports whether the live state matches the source revision.
	Sync ApplicationSyncStatus `json:"sync,omitempty"`
}

// ApplicationHealthStatus is the health half of an Application's status.
type ApplicationHealthStatus struct {
	Status  string `json:"status,omitempty"`
	Message string `json:"message,omitempty"`
}

// ApplicationSyncStatus is the sync half of an Application's status.
type ApplicationSyncStatus struct {
	Status string `json:"status,omitempty"`
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
