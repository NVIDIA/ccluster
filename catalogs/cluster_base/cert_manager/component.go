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

// Package certmanager describes the values of catalogs/cluster_base/cert_manager.
package certmanager

import "github.com/NVIDIA/ccluster/catalogs"

const (
	// Name is the component's name in a CCLDeployment, and the key its values
	// nest under.
	Name = "cert-manager"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/cert_manager"
	// Namespace is where cert-manager is installed.
	Namespace = "cert-manager"
)

// Values is the top-level values document for catalogs/cluster_base/cert_manager.
type Values struct {
	CertManager *ChartValues `json:"cert-manager,omitempty"`
}

// ChartValues are the upstream cert-manager values worth setting per cluster.
type ChartValues struct {
	// ReplicaCount is the controller replica count. The chart ships 2.
	ReplicaCount *int32 `json:"replicaCount,omitempty"`

	// Prometheus scraping is shipped off, because the ServiceMonitor CRD only
	// exists once kube-prometheus-stack is deployed. Enable it after that.
	Prometheus *PrometheusValues `json:"prometheus,omitempty"`
}

// PrometheusValues configures cert-manager's metrics scraping.
type PrometheusValues struct {
	Enabled *bool `json:"enabled,omitempty"`

	// ServiceMonitor is spelled lower-case in this chart, unlike every other
	// chart in the catalog.
	ServiceMonitor *Toggle `json:"servicemonitor,omitempty"`
}

// Toggle is a subchart's enable flag.
type Toggle struct {
	// Enabled turns the feature on.
	Enabled *bool `json:"enabled,omitempty"`
}

// Component is the catalog entry for cert-manager.
type Component struct {
	catalogs.ComponentEntry

	// Values overrides the chart's defaults. The zero value means "chart
	// defaults".
	Values Values
}

// New returns the cert-manager component with its chart metadata filled in and no value
// overrides.
func New() *Component {
	return &Component{ComponentEntry: catalogs.ComponentEntry{
		Name:      Name,
		Chart:     Chart,
		Namespace: Namespace,
	}}
}

// Entry returns the component's chart metadata.
func (c *Component) Entry() catalogs.ComponentEntry { return c.ComponentEntry }

// ValuesObject returns the chart's typed values.
func (c *Component) ValuesObject() any { return c.Values }
