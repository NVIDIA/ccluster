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

// Package finalstack describes the values of catalogs/cluster_base/final_stack.
package finalstack

import "github.com/NVIDIA/ccluster/catalogs"

const (
	// Name is the component's name in a CCLDeployment.
	Name = "final-stack"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/final_stack"
	// Namespace is where the final stack is installed.
	Namespace = "final-stack"
)

// Values is the top-level values document for catalogs/cluster_base/final_stack.
//
// It is empty, and that is a known gap rather than a chart with nothing to configure.
// This chart wraps no upstream dependency and has no chart-level values: its templates
// consume only .Values.global.cluster.domain, which every one of its five resources
// interpolates — the cluster issuers, the OIDC discovery Ingress, and the four
// ClusterSPIFFEIDs.
//
// Global values are not modelled by this catalog yet. Until they are, this component
// renders against the chart's own defaults and the resources it produces will carry
// the placeholder domain, so a caller that needs a real one has to set
// global.cluster.domain on the CCLDeployment component's values by hand.
type Values struct{}

// Component is the catalog entry for final-stack.
type Component struct {
	catalogs.ComponentEntry

	// Values overrides the chart's defaults. This chart has none — see Values.
	Values Values
}

// New returns the final-stack component with its chart metadata filled in.
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
