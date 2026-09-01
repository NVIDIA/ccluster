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

// Package networkoperator describes the values of catalogs/cluster_base/network_operator.
package networkoperator

import "github.com/NVIDIA/ccluster/catalogs"

const (
	// Name is the component's name in a CCLDeployment, and the key its values
	// nest under.
	Name = "network-operator"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/network_operator"
	// Namespace is where the NVIDIA network operator is installed.
	Namespace = "network-operator"
)

// Values is the top-level values document for catalogs/cluster_base/network_operator.
type Values struct {
	NetworkOperator *ChartValues `json:"network-operator,omitempty"`
}

// ChartValues are the upstream network-operator values worth setting per cluster.
//
// It is empty: the chart ships {} and no per-cluster tunable has been identified
// yet. It exists as a named struct rather than an anonymous one so that adding the
// first field later is a narrow change.
type ChartValues struct{}

// Component is the catalog entry for network-operator.
type Component struct {
	catalogs.ComponentEntry

	// Values overrides the chart's defaults. The zero value means "chart
	// defaults".
	Values Values
}

// New returns the network-operator component with its chart metadata filled in and no value
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
