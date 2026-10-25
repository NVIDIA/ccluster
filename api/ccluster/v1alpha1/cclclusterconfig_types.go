/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CCLClusterConfigName is the only name a CCLClusterConfig may have. The kind is a
// cluster-scoped singleton: it describes the cluster the infra-plane operator runs in,
// and there is exactly one of those.
const CCLClusterConfigName = "cluster"

// CCLClusterConfigSpec defines the desired state of CCLClusterConfig
// +kubebuilder:title=CCLClusterConfigSpec
type CCLClusterConfigSpec struct {
	// domain is the cluster's DNS domain, and its SPIFFE trust domain. Components such
	// as spire, external-dns and final_stack must be configured with it; the infra-plane
	// operator verifies that they are. It must be a lowercase DNS name of at least two
	// labels, each at most 63 characters.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?(\.[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?)+$`
	Domain string `json:"domain"`
}

// CCLClusterConfigStatus defines the observed state of CCLClusterConfig. The
// control-plane operator also copies it, unchanged, into the status of the CCLInfraPlane
// that registers the cluster.
// +kubebuilder:title=CCLClusterConfigStatus
type CCLClusterConfigStatus struct {
	// conditions represent the current state of the infra-plane cluster's
	// CCLClusterConfig.
	//
	// Condition types reported by the infra-plane operator:
	//   - "Verified": every catalogs/cluster_base component is installed in this
	//     cluster and configured for it. When False, the message names the charts
	//     that failed verification.
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// observedGeneration is the CCLClusterConfig's .metadata.generation the conditions
	// were computed from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// loadBalancer is the cluster's ingress LoadBalancer as the infra-plane operator last
	// observed it: the Service of type LoadBalancer labelled
	// ccluster.nvidia.com/track-ingress=true. It is unset while there is no such
	// Service, while it has no address yet, and while more than one Service carries the
	// label.
	// +optional
	LoadBalancer *LoadBalancerStatus `json:"loadBalancer,omitempty"`
}

// LoadBalancerStatus is the address of a cluster's ingress LoadBalancer. At least one of
// ip and hostname is set.
type LoadBalancerStatus struct {
	// ip is the LoadBalancer's IP address: the first IP the Service reports.
	// +optional
	IP string `json:"ip,omitempty"`

	// hostname is the LoadBalancer's DNS name, set by cloud providers whose load
	// balancers are addressed by name rather than by IP: the first hostname the Service
	// reports.
	// +optional
	Hostname string `json:"hostname,omitempty"`
}

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Verified",type=string,JSONPath=`.status.conditions[?(@.type=="Verified")].status`
// +kubebuilder:printcolumn:name="LoadBalancer",type=string,JSONPath=`.status.loadBalancer.ip`
// +kubebuilder:printcolumn:name="Domain",type=string,JSONPath=`.spec.domain`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="self.metadata.name == 'cluster'",message="CCLClusterConfig is a singleton and must be named \"cluster\""

// CCLClusterConfig is the Schema for the cclclusterconfigs API.
//
// It describes the cluster the infra-plane operator runs in, and it is that operator's
// only input: the operator verifies every catalogs/cluster_base component against it and
// reports the outcome in status. It is cluster-scoped and there is exactly one, named
// "cluster".
type CCLClusterConfig struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of CCLClusterConfig
	// +required
	Spec CCLClusterConfigSpec `json:"spec"`

	// status defines the observed state of CCLClusterConfig
	// +optional
	Status CCLClusterConfigStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// CCLClusterConfigList contains a list of CCLClusterConfig
type CCLClusterConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []CCLClusterConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CCLClusterConfig{}, &CCLClusterConfigList{})
}
