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
	// conditions represent the current state of the CCLInfraPlane resource.
	//
	// Condition types reported by the control-plane operator:
	//   - "Available": the namespace's Argo CD AppProject exists and the cluster
	//     Secret has been mirrored into the Argo CD namespace.
	//   - "ClusterConfigApplied": the CCLClusterConfig singleton on the infra-plane
	//     cluster carries this CCLInfraPlane's domain.
	//   - "DNSReady": external-dns has read the current records pointing
	//     "*.<lb>.<domain>" at each of the cluster's LoadBalancers labelled
	//     ccluster.nvidia.com/loadbalancer: <lb>, and every label value is a DNS label. external-dns marks the records read before it
	//     writes them, so True does not mean they were published: the provider may
	//     reject them, or external-dns may skip them.
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// observedGeneration is the .metadata.generation the conditions were computed from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// clusterConfig is the status of the CCLClusterConfig singleton on the infra-plane
	// cluster, copied verbatim whenever it changes there. It is what the infra-plane
	// operator reports about the cluster; ClusterConfigApplied says whether the copy is
	// current. Its conditions and observedGeneration are the infra-plane operator's, and
	// refer to the CCLClusterConfig, not to this CCLInfraPlane. Its domain is the
	// plane's domain once the infra-plane operator has verified the cluster against it.
	// +optional
	ClusterConfig *CCLClusterConfigStatus `json:"clusterConfig,omitempty"`
}

// The condition types the control-plane operator reports in CCLInfraPlane.status.
const (
	// CCLInfraPlaneConditionAvailable reports on the cluster's registration with Argo
	// CD: its namespace's AppProject and its mirrored cluster Secret.
	CCLInfraPlaneConditionAvailable = "Available"
	// CCLInfraPlaneConditionClusterConfigApplied reports on the CCLClusterConfig
	// singleton the operator writes into the infra-plane cluster.
	CCLInfraPlaneConditionClusterConfigApplied = "ClusterConfigApplied"
	// CCLInfraPlaneConditionDNSReady reports on the DNSEndpoint that has external-dns
	// publish the plane's wildcard records.
	CCLInfraPlaneConditionDNSReady = "DNSReady"
)

// The reasons of the CCLInfraPlane conditions.
const (
	// Available.
	CCLInfraPlaneReasonClusterSecretCreated  = "ClusterSecretCreated"
	CCLInfraPlaneReasonSecretNotFound        = "SecretNotFound"
	CCLInfraPlaneReasonClusterSecretConflict = "ClusterSecretConflict"
	CCLInfraPlaneReasonProjectConflict       = "ProjectConflict"

	// ClusterConfigApplied.
	CCLInfraPlaneReasonApplied                = "Applied"
	CCLInfraPlaneReasonNotAvailable           = "NotAvailable"
	CCLInfraPlaneReasonCRDMissing             = "CRDMissing"
	CCLInfraPlaneReasonConflict               = "Conflict"
	CCLInfraPlaneReasonAccessDenied           = "AccessDenied"
	CCLInfraPlaneReasonClusterUnreachable     = "ClusterUnreachable"
	CCLInfraPlaneReasonUnsupportedCredentials = "UnsupportedCredentials"
	CCLInfraPlaneReasonInvalidCredentials     = "InvalidCredentials"

	// DNSReady.
	CCLInfraPlaneReasonPublished             = "Published"
	CCLInfraPlaneReasonPending               = "Pending"
	CCLInfraPlaneReasonLoadBalancerPending   = "LoadBalancerPending"
	CCLInfraPlaneReasonInvalidLoadBalancer   = "InvalidLoadBalancer"
	CCLInfraPlaneReasonDNSEndpointCRDMissing = "DNSEndpointCRDMissing"
	CCLInfraPlaneReasonDNSEndpointConflict   = "DNSEndpointConflict"
	CCLInfraPlaneReasonDNSEndpointError      = "DNSEndpointError"
)

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Domain",type=string,JSONPath=`.status.clusterConfig.domain`
// +kubebuilder:printcolumn:name="Available",type=string,JSONPath=`.status.conditions[?(@.type=="Available")].status`
// +kubebuilder:printcolumn:name="ClusterConfig",type=string,JSONPath=`.status.conditions[?(@.type=="ClusterConfigApplied")].status`
// +kubebuilder:printcolumn:name="Verified",type=string,JSONPath=`.status.clusterConfig.conditions[?(@.type=="Verified")].status`
// +kubebuilder:printcolumn:name="DNS",type=string,JSONPath=`.status.conditions[?(@.type=="DNSReady")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="size(self.metadata.name) <= 63 && self.metadata.name.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$')",message="name must be a DNS label: at most 63 lowercase letters, digits and '-', starting and ending with a letter or digit"

// CCLInfraPlane is the Schema for the cclinfraplanes API.
//
// It registers an infra-plane cluster with the control plane. The cluster's DNS domain is
// "<name>.<namespace>.<base domain>", the base domain being the control-plane operator's
// --base-domain, so the name must be a DNS label. The operator writes that domain to the
// cluster's CCLClusterConfig and points "*.<lb>.<domain>" at each of the cluster's
// LoadBalancers labelled ccluster.nvidia.com/loadbalancer: <lb>, such as
// "*.ingress.<domain>" at its ingress. status.clusterConfig.domain shows the domain once
// the infra-plane operator has verified the cluster against it.
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
