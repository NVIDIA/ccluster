/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package externaldns describes the values of catalogs/cluster_base/external_dns.
package externaldns

import (
	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
)

const (
	// Name is the component's name in a CCLComponentSet, and the key its values
	// nest under.
	Name = "external-dns"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/external_dns"
)

// Values is the top-level values document for catalogs/cluster_base/external_dns.
type Values struct {
	ExternalDNS *ChartValues `json:"external-dns,omitempty"`
}

// ChartValues are the upstream external-dns values worth setting per cluster.
type ChartValues struct {
	// Provider is the DNS provider. The chart ships aws; switch it and supply
	// the matching credentials through the service account.
	Provider *Provider `json:"provider,omitempty"`

	// DomainFilters constrains external-dns to these zones. Unset, it manages
	// every zone the credentials can reach.
	DomainFilters []string `json:"domainFilters,omitempty"`

	// TxtOwnerID identifies this instance's TXT ownership records. Two
	// clusters sharing a zone must not share it.
	TxtOwnerID *string `json:"txtOwnerId,omitempty"`

	// ExtraArgs is where --zone-id-filter goes to constrain Route53 changes
	// to a single hosted zone.
	ExtraArgs []string `json:"extraArgs,omitempty"`

	ServiceAccount *ServiceAccount `json:"serviceAccount,omitempty"`

	// ServiceMonitor requires the kube-prometheus-stack CRDs.
	ServiceMonitor *Toggle `json:"serviceMonitor,omitempty"`
}

// Provider is the DNS provider external-dns talks to.
type Provider struct {
	Name *string `json:"name,omitempty"`
}

// ServiceAccount configures the identity external-dns runs as.
type ServiceAccount struct {
	// Create is shipped true for standalone testing. Set it false and
	// pre-create the account, with IRSA for instance, in production.
	Create *bool   `json:"create,omitempty"`
	Name   *string `json:"name,omitempty"`
}

// Toggle is a subchart's enable flag.
type Toggle struct {
	// Enabled turns the feature on.
	Enabled *bool `json:"enabled,omitempty"`
}

// New returns the external-dns chart entry, installed into namespace with values.
// The zero Values means "chart defaults".
func New(
	namespace string,
	values Values,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	return catalogs.Entry(Chart, namespace, values)
}
