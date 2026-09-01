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

// Package keda describes the values of catalogs/cluster_base/keda.
package keda

import "github.com/NVIDIA/ccluster/catalogs"

const (
	// Name is the component's name in a CCLDeployment, and the key its values
	// nest under.
	Name = "keda"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/keda"
	// Namespace is where KEDA is installed.
	Namespace = "keda"
)

// Values is the top-level values document for catalogs/cluster_base/keda.
type Values struct {
	Keda *ChartValues `json:"keda,omitempty"`
}

// ChartValues are the upstream keda values worth setting per cluster.
type ChartValues struct {
	Operator      *Replicas `json:"operator,omitempty"`
	MetricsServer *Replicas `json:"metricsServer,omitempty"`
	Webhooks      *Toggle   `json:"webhooks,omitempty"`
}

// Replicas is a subchart's replica count.
type Replicas struct {
	ReplicaCount *int32 `json:"replicaCount,omitempty"`
}

// Toggle is a subchart's enable flag.
type Toggle struct {
	// Enabled turns the feature on.
	Enabled *bool `json:"enabled,omitempty"`
}

// Component is the catalog entry for keda.
type Component struct {
	catalogs.ComponentEntry

	// Values overrides the chart's defaults. The zero value means "chart
	// defaults".
	Values Values
}

// New returns the keda component with its chart metadata filled in and no value
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
