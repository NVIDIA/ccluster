/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ClusterIssuer is a cert-manager issuer that Certificates in any namespace can request,
// such as the acme-ca issuer catalogs/cluster_base/final_stack renders.
//
// Partial and read-only: it declares no spec, so writing a ClusterIssuer read into this
// type back would delete its issuer configuration. The infra-plane operator only reads
// its status.
//
// See https://cert-manager.io/docs/reference/api-docs/#cert-manager.io/v1.ClusterIssuer
// for the full API.
//
// +kubebuilder:object:root=true
type ClusterIssuer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Status IssuerStatus `json:"status,omitempty"`
}

// IssuerStatus is what cert-manager reports back on an issuer.
type IssuerStatus struct {
	// conditions describe the issuer's current state. cert-manager sets the Ready type.
	Conditions []IssuerCondition `json:"conditions,omitempty"`
}

// IssuerConditionReady is the condition cert-manager sets True once an issuer can issue:
// for an ACME issuer, once its account is registered with the ACME server.
const IssuerConditionReady = "Ready"

// IssuerCondition is one of an issuer's conditions.
type IssuerCondition struct {
	// type of the condition, such as IssuerConditionReady.
	Type string `json:"type"`
	// status of the condition: True, False or Unknown.
	Status metav1.ConditionStatus `json:"status"`
	// reason is a machine-readable explanation of the status.
	Reason string `json:"reason,omitempty"`
	// message is a human-readable explanation of the status.
	Message string `json:"message,omitempty"`
	// observedGeneration is the .metadata.generation the condition was set for.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// ClusterIssuerList contains a list of ClusterIssuer.
//
// +kubebuilder:object:root=true
type ClusterIssuerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterIssuer `json:"items"`
}
