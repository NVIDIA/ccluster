/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package v1alpha1

import (
	"fmt"
	"net/netip"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

// CCLClusterConfigName is the only name a CCLClusterConfig may have. The kind is a
// cluster-scoped singleton: it describes the cluster the infra-plane operator runs in,
// and there is exactly one of those.
const CCLClusterConfigName = "cluster"

// CCLClusterConfigSpec defines the desired state of CCLClusterConfig
// +kubebuilder:title=CCLClusterConfigSpec
type CCLClusterConfigSpec struct {
	// domain is the cluster's DNS domain, which the infra-plane operator verifies the
	// cluster_base components against. For a cluster registered with a CCLInfraPlane, the
	// control-plane operator writes the plane's domain here:
	// "<name>.<namespace>.<base domain>". It must be a lowercase DNS name of at least two
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
	//     cluster and configured for it, except an optional one, which may be absent.
	//     When False, the message names the charts that failed verification.
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

	// domain is the spec.domain the infra-plane operator last verified the cluster
	// against, in the same pass as observedGeneration. The control-plane operator copies
	// it, with the rest of this status, into the CCLInfraPlane that registers the cluster.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?(\.[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?)+$`
	Domain string `json:"domain,omitempty"`

	// loadBalancers are the cluster's LoadBalancer Services labelled
	// ccluster.nvidia.com/loadbalancer, one per label value, as the infra-plane operator
	// last observed them. A value carried by more than one Service is left out and
	// reported in the Verified condition.
	// +listType=map
	// +listMapKey=name
	// +optional
	LoadBalancers []LoadBalancerStatus `json:"loadBalancers,omitempty"`
}

// The condition types the infra-plane operator reports in CCLClusterConfig.status.
const (
	// CCLClusterConfigConditionVerified reports whether every cluster_base component
	// passed its Verifier against the spec.
	CCLClusterConfigConditionVerified = "Verified"
)

// The reasons of the CCLClusterConfig conditions.
const (
	// Verified.
	CCLClusterConfigReasonComponentsVerified    = "ComponentsVerified"
	CCLClusterConfigReasonComponentsNotVerified = "ComponentsNotVerified"
	// CCLClusterConfigReasonLoadBalancerConflict is reported when every component
	// verifies but more than one LoadBalancer Service carries the same
	// ccluster.nvidia.com/loadbalancer value.
	CCLClusterConfigReasonLoadBalancerConflict = "LoadBalancerConflict"
)

// LoadBalancerStatus is the address of a LoadBalancer Service labelled
// ccluster.nvidia.com/loadbalancer, as the Service reports it. ip and hostname are both
// empty until the cloud provider assigns one. Neither the schema nor the infra-plane
// operator checks the address: the control-plane operator does, when it publishes the
// LoadBalancer's DNS record, and reports one it cannot use in its DNSReady condition.
type LoadBalancerStatus struct {
	// name is the Service's ccluster.nvidia.com/loadbalancer label value.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`

	// ip is the LoadBalancer's IP address: the first IP the Service reports.
	// +optional
	// +kubebuilder:validation:MaxLength=45
	IP string `json:"ip,omitempty"`

	// hostname is the LoadBalancer's DNS name, set by cloud providers whose load
	// balancers are addressed by name rather than by IP: the first hostname the Service
	// reports.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Hostname string `json:"hostname,omitempty"`
}

// CanonicalLoadBalancerIP returns ip in the form a DNS record takes: canonical, with an
// IPv4-mapped IPv6 address written as IPv4. It fails if ip is not an IP address, or has
// a zone.
func CanonicalLoadBalancerIP(ip string) (string, error) {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return "", fmt.Errorf("ip %q is not an IP address", ip)
	}
	if addr.Zone() != "" {
		return "", fmt.Errorf("ip %q has a zone", ip)
	}
	return addr.Unmap().String(), nil
}

// ValidateLoadBalancerHostname returns why hostname cannot be a CNAME target, a lowercase
// DNS name that is not an IP address, or nil. It is the rule Kubernetes
// applies to a Service's load-balancer hostname.
func ValidateLoadBalancerHostname(hostname string) error {
	if _, err := netip.ParseAddr(hostname); err == nil {
		return fmt.Errorf("hostname %q is an IP address", hostname)
	}
	if errs := validation.IsDNS1123Subdomain(hostname); len(errs) > 0 {
		return fmt.Errorf("hostname %q is not a DNS name: %s", hostname, strings.Join(errs, ", "))
	}
	return nil
}

// Canonical returns lb with its address in the form a DNS record takes, or why it cannot
// be: CanonicalLoadBalancerIP and ValidateLoadBalancerHostname applied to whichever is set.
func (lb LoadBalancerStatus) Canonical() (LoadBalancerStatus, error) {
	if lb.IP != "" {
		ip, err := CanonicalLoadBalancerIP(lb.IP)
		if err != nil {
			return lb, err
		}
		lb.IP = ip
	}
	if lb.Hostname != "" {
		if err := ValidateLoadBalancerHostname(lb.Hostname); err != nil {
			return lb, err
		}
	}
	return lb, nil
}

// LoadBalancer returns the reported LoadBalancer with the given name, or nil if there is
// none.
func (s *CCLClusterConfigStatus) LoadBalancer(name string) *LoadBalancerStatus {
	for i := range s.LoadBalancers {
		if s.LoadBalancers[i].Name == name {
			return &s.LoadBalancers[i]
		}
	}
	return nil
}

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Verified",type=string,JSONPath=`.status.conditions[?(@.type=="Verified")].status`
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
