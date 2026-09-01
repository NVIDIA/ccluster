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

// Package prometheusadapter describes the values of catalogs/cluster_base/prometheus_adapter.
package prometheusadapter

import "github.com/NVIDIA/ccluster/catalogs"

const (
	// Name is the component's name in a CCLDeployment, and the key its values
	// nest under.
	Name = "prometheus-adapter"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/prometheus_adapter"
	// Namespace is where prometheus-adapter is installed.
	Namespace = "prometheus"
)

// Values is the top-level values document for catalogs/cluster_base/prometheus_adapter.
type Values struct {
	PrometheusAdapter *ChartValues `json:"prometheus-adapter,omitempty"`
}

// ChartValues are the upstream prometheus-adapter values worth setting per cluster.
type ChartValues struct {
	Prometheus *Prometheus `json:"prometheus,omitempty"`
	Rules      *Rules      `json:"rules,omitempty"`
}

// Prometheus is where the adapter reads metrics from. The chart's default matches
// kube-prometheus-stack's pinned fullnameOverride, so changing one means changing
// the other.
type Prometheus struct {
	URL  *string `json:"url,omitempty"`
	Port *int32  `json:"port,omitempty"`
}

// Rules are the adapter's metric translation rules.
type Rules struct {
	Custom []map[string]any `json:"custom,omitempty"`
}

// Component is the catalog entry for prometheus-adapter.
type Component struct {
	catalogs.ComponentEntry

	// Values overrides the chart's defaults. The zero value means "chart
	// defaults".
	Values Values
}

// New returns the prometheus-adapter component with its chart metadata filled in and no value
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
