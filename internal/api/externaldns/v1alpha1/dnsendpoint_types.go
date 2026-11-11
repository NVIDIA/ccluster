/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DNSEndpoint is a set of DNS records for external-dns's "crd" source to publish. The
// control-plane operator writes one per CCLInfraPlane, holding a wildcard record under the
// plane's domain for each of its labelled LoadBalancers.
//
// Partial: writing a DNSEndpoint read into this type back **deletes** every field not
// declared here, such as an endpoint's labels, setIdentifier or providerSpecific. The
// operator owns the DNSEndpoints it creates outright and rewrites their spec on every
// reconcile; the ownership check in internal/controller/cp keeps that confined to them.
//
// See https://kubernetes-sigs.github.io/external-dns/latest/docs/sources/crd/ for the
// full API.
//
// +kubebuilder:object:root=true
type DNSEndpoint struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DNSEndpointSpec   `json:"spec,omitempty"`
	Status DNSEndpointStatus `json:"status,omitempty"`
}

// DNSEndpointSpec is the records a DNSEndpoint asks external-dns to publish.
type DNSEndpointSpec struct {
	Endpoints []Endpoint `json:"endpoints,omitempty"`
}

// Endpoint is one DNS record.
type Endpoint struct {
	// dnsName is the record's name, such as "*.gpu-a.example.com".
	DNSName string `json:"dnsName,omitempty"`

	// targets are the record's values: IP addresses for an A record, a host name for a
	// CNAME.
	Targets []string `json:"targets,omitempty"`

	// recordType is the record's type, such as "A" or "CNAME".
	RecordType string `json:"recordType,omitempty"`

	// recordTTL is the record's TTL in seconds. Zero leaves it to the provider.
	RecordTTL int64 `json:"recordTTL,omitempty"`
}

// DNSEndpointStatus is what external-dns reports back on a DNSEndpoint.
type DNSEndpointStatus struct {
	// observedGeneration is the .metadata.generation external-dns last read the records
	// of. external-dns sets it whether or not it found a zone for them.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// DNSEndpointList contains a list of DNSEndpoint.
//
// +kubebuilder:object:root=true
type DNSEndpointList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []DNSEndpoint `json:"items"`
}
