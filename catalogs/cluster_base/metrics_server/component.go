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

// Package metricsserver describes the values of catalogs/cluster_base/metrics_server.
package metricsserver

import "github.com/NVIDIA/ccluster/catalogs"

const (
	// Name is the component's name in a CCLDeployment, and the key its values
	// nest under.
	Name = "metrics-server"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/metrics_server"
	// Namespace is where the metrics server is installed.
	Namespace = "kube-system"
)

// Values is the top-level values document for catalogs/cluster_base/metrics_server.
type Values struct {
	MetricsServer *ChartValues `json:"metrics-server,omitempty"`
}

// ChartValues are the upstream metrics-server values worth setting per cluster.
type ChartValues struct {
	// Args are the server's command-line flags. The chart ships the kubelet
	// address preferences and resolution that work on most distros; a cluster
	// with unusual kubelet addressing needs its own.
	Args []string `json:"args,omitempty"`
}

// Component is the catalog entry for metrics-server.
type Component struct {
	catalogs.ComponentEntry

	// Values overrides the chart's defaults. The zero value means "chart
	// defaults".
	Values Values
}

// New returns the metrics-server component with its chart metadata filled in and no value
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
