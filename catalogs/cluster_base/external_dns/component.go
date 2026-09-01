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

// Package externaldns describes the values of catalogs/cluster_base/external_dns.
package externaldns

import "github.com/NVIDIA/ccluster/catalogs"

const (
	// Name is the component's name in a CCLDeployment, and the key its values
	// nest under.
	Name = "external-dns"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/external_dns"
	// Namespace is where external-dns is installed.
	Namespace = "external-dns"
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

// Component is the catalog entry for external-dns.
type Component struct {
	catalogs.ComponentEntry

	// Values overrides the chart's defaults. The zero value means "chart
	// defaults".
	Values Values
}

// New returns the external-dns component with its chart metadata filled in and no value
// overrides.
func New() *Component {
	return &Component{ComponentEntry: catalogs.ComponentEntry{
		Name:      Name,
		Chart:     Chart,
		Namespace: Namespace,
	}}
}

// ValuesObject returns the chart's typed values.
func (c *Component) ValuesObject() any { return c.Values }
