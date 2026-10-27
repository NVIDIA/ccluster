/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// CCLInfraPlaneSpec defines the desired state of CCLInfraPlane
// +kubebuilder:title=CCLInfraPlaneSpec
type CCLInfraPlaneSpec struct {
	// clusterCredentialsRef references a Secret in the same namespace as this
	// CCLInfraPlane that holds the credentials for the infra-plane cluster's API
	// server.
	//
	// Its data is copied verbatim into a Secret in the Argo CD namespace, named
	// after this CCLInfraPlane and labelled "argocd.argoproj.io/secret-type:
	// cluster". The operator does not interpret the data, so the referenced Secret
	// must already carry Argo CD's cluster-secret keys - "server" and "config".
	//
	// The exception is "name", which Argo CD displays as the cluster name: the
	// operator always sets it to this CCLInfraPlane's name, overwriting whatever the
	// referenced Secret holds. See
	// https://argo-cd.readthedocs.io/en/stable/operator-manual/declarative-setup/#clusters
	// +required
	ClusterCredentialsRef LocalObjectReference `json:"clusterCredentialsRef"`
}

// CCLInfraPlaneStatus defines the observed state of CCLInfraPlane.
// +kubebuilder:title=CCLInfraPlaneStatus
type CCLInfraPlaneStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	// conditions represent the current state of the CCLInfraPlane resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// observedGeneration is the .metadata.generation the conditions were computed from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Available",type=string,JSONPath=`.status.conditions[?(@.type=="Available")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// CCLInfraPlane is the Schema for the cclinfraplanes API
type CCLInfraPlane struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of CCLInfraPlane
	// +required
	Spec CCLInfraPlaneSpec `json:"spec"`

	// status defines the observed state of CCLInfraPlane
	// +optional
	Status CCLInfraPlaneStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// CCLInfraPlaneList contains a list of CCLInfraPlane
type CCLInfraPlaneList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []CCLInfraPlane `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CCLInfraPlane{}, &CCLInfraPlaneList{})
}
