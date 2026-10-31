/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package v1alpha1

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ComponentEntry is the chart half of a component: which chart, where it is installed,
// and the values it is installed with.
//
// It is embedded into ComponentSpec inline, so chart, namespace and values remain
// component-level fields on the wire. It exists as a type so that the catalog packages
// under catalogs/ can build one from a typed, per-chart values struct — see
// catalogs.Entry. Nothing validates a submitted CCLComponentSet against those typed
// schemas: values here is as untyped as it has always been.
type ComponentEntry struct {
	// chart is the chart's directory, relative to the root of the catalog repository.
	//
	// The repository and revision are not settable per component: they are how the
	// operator itself is configured (--catalog-repo-url and --catalog-revision), so
	// every component of every CCLComponentSet deploys from the same catalog.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	Chart string `json:"chart"`

	// namespace is where the chart is installed on the target cluster. It is created
	// if it does not exist.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Namespace string `json:"namespace"`

	// values overrides the chart's defaults. It is passed to Helm verbatim; the
	// operator does not interpret or validate it against the chart.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Values *apiextensionsv1.JSON `json:"values,omitempty"`
}

// ComponentSpec is one Helm chart deployed onto one infra-plane cluster.
// +kubebuilder:title=ComponentSpec
type ComponentSpec struct {
	// name identifies the component within this CCLComponentSet. It is templated into
	// the generated Argo CD Application's name, so it has to be DNS-1123.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// infraPlaneRef names the CCLInfraPlane this component is deployed to. It must be
	// in the same namespace as this CCLComponentSet, and must already be Available -
	// that is, its cluster credentials have been mirrored into the Argo CD namespace,
	// which is what lets the generated Application address the cluster by name.
	// +required
	InfraPlaneRef LocalObjectReference `json:"infraPlaneRef"`

	// ComponentEntry names the chart, the namespace it is installed into, and the
	// values it is installed with. Embedded anonymously so those three stay
	// component-level fields in YAML.
	ComponentEntry `json:",inline"`
}

// CCLComponentSetSpec defines the desired state of CCLComponentSet
// +kubebuilder:title=CCLComponentSetSpec
type CCLComponentSetSpec struct {
	// components are rolled out in the order given: the operator withholds a
	// component's Argo CD Application until every component before it reports both
	// Healthy and Synced. There is no timeout on that wait - a component that never
	// becomes healthy stalls everything after it, which the Progressing condition
	// reports.
	//
	// Ordering is positional rather than a dependsOn field because nothing yet needs to
	// express a graph. When that changes, the list order stays a valid topological
	// order, so adding dependencies will not invalidate existing objects.
	//
	// +required
	// +listType=atomic
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=50
	// +kubebuilder:validation:XValidation:rule="self.all(c, self.exists_one(x, x.name == c.name))",message="component names must be unique"
	Components []ComponentSpec `json:"components"`
}

// CCLComponentSetStatus defines the observed state of CCLComponentSet.
// +kubebuilder:title=CCLComponentSetStatus
type CCLComponentSetStatus struct {
	// conditions represent the current state of the CCLComponentSet resource.
	//
	// Condition types reported by the control-plane operator:
	//   - "Validated": every component's infraPlaneRef resolves to an Available
	//     CCLInfraPlane.
	//   - "ComponentsApplied": the Argo CD Applications for the components released so
	//     far have been written.
	//   - "Progressing": whether the rollout has finished, and which component it is
	//     waiting on if it has not.
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// observedGeneration is the .metadata.generation of the spec the operator last
	// reconciled. componentSyncs always describes this generation. A condition can be
	// older, as its own observedGeneration says: ComponentsApplied is not rewritten on a
	// pass that fails validation.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// componentSyncs reports the latest observed Application state for each released
	// component, judged against the spec at status.observedGeneration.
	// +listType=map
	// +listMapKey=name
	// +optional
	ComponentSyncs []ComponentSyncStatus `json:"componentSyncs,omitempty"`
}

// The condition types the control-plane operator reports in CCLComponentSet.status.
const (
	// CCLComponentSetConditionValidated reports whether every component's infraPlaneRef
	// resolves to a CCLInfraPlane that Argo CD can already reach.
	CCLComponentSetConditionValidated = "Validated"
	// CCLComponentSetConditionComponentsApplied reports on the Argo CD Applications that
	// deploy the components onto their infra-plane clusters.
	CCLComponentSetConditionComponentsApplied = "ComponentsApplied"
	// CCLComponentSetConditionProgressing reports on the rollout: False once every
	// component has synced the spec the operator wrote and is healthy.
	CCLComponentSetConditionProgressing = "Progressing"
)

// The reasons of the CCLComponentSet conditions.
const (
	// Validated.
	CCLComponentSetReasonComponentsAccepted = "ComponentsAccepted"
	CCLComponentSetReasonInfraPlaneNotFound = "InfraPlaneNotFound"
	CCLComponentSetReasonInfraPlaneNotReady = "InfraPlaneNotReady"

	// Progressing. When validation failed and the component being waited on needs an
	// Application written, Progressing carries the Validated reason instead.
	CCLComponentSetReasonWaveSyncing     = "WaveSyncing"
	CCLComponentSetReasonWaveSyncFailed  = "WaveSyncFailed"
	CCLComponentSetReasonRolloutComplete = "RolloutComplete"

	// ComponentsApplied.
	CCLComponentSetReasonApplicationsCreated   = "ApplicationsCreated"
	CCLComponentSetReasonApplicationCRDMissing = "ApplicationCRDMissing"
	CCLComponentSetReasonApplicationConflict   = "ApplicationConflict"
	CCLComponentSetReasonApplicationFailed     = "ApplicationFailed"
)

// ComponentSyncStatus reports the latest observed Application state.
// The controller derives rollout decisions from Applications, not this report.
// +kubebuilder:title=ComponentSyncStatus
type ComponentSyncStatus struct {
	// name identifies the component in spec.components.
	Name string `json:"name"`

	// comparedToMatchesSpec reports whether Argo CD's latest comparison used the
	// desired source, destination and ignoreDifferences. While false, health and sync do
	// not establish convergence on the desired configuration.
	ComparedToMatchesSpec bool `json:"comparedToMatchesSpec"`

	// health is Argo CD's observed Application health, empty before its first comparison.
	// +optional
	Health string `json:"health,omitempty"`

	// sync is Argo CD's observed Application sync status.
	// +optional
	Sync string `json:"sync,omitempty"`

	// message is Argo CD's latest operation message, including progress, retries and failures.
	// +optional
	Message string `json:"message,omitempty"`
}

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Applied",type=string,JSONPath=`.status.conditions[?(@.type=="ComponentsApplied")].status`
// +kubebuilder:printcolumn:name="Progress",type=string,JSONPath=`.status.conditions[?(@.type=="Progressing")].reason`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="self.metadata.name.size() <= 63",message="name must be at most 63 characters, because it is used as a label value on the generated Argo CD Applications"

// CCLComponentSet is the Schema for the cclcomponentsets API.
//
// It describes a bundle of Helm charts from the ccluster catalog, each with its own
// values and its own destination cluster, rolled out in a fixed order. The operator does
// not install anything itself: it creates one Argo CD Application per component, in
// order, and Argo CD does the work.
type CCLComponentSet struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of CCLComponentSet
	// +required
	Spec CCLComponentSetSpec `json:"spec"`

	// status defines the observed state of CCLComponentSet
	// +optional
	Status CCLComponentSetStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// CCLComponentSetList contains a list of CCLComponentSet
type CCLComponentSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []CCLComponentSet `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CCLComponentSet{}, &CCLComponentSetList{})
}
