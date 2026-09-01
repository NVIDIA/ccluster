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

// Package argorollouts describes the values of catalogs/cluster_base/argo_rollouts.
package argorollouts

import "github.com/NVIDIA/ccluster/catalogs"

const (
	// Name is the component's name in a CCLDeployment, and the key its values nest
	// under.
	Name = "argo-rollouts"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/argo_rollouts"
	// Namespace is where argo-rollouts is installed.
	Namespace = "argo-rollouts"
)

// Values is the top-level values document for catalogs/cluster_base/argo_rollouts.
// Its single key is the upstream chart's alias, because this chart wraps it as a
// dependency — which is the only reason the nesting exists.
type Values struct {
	ArgoRollouts *ChartValues `json:"argo-rollouts,omitempty"`
}

// ChartValues are the upstream argo-rollouts values worth setting per cluster.
// Anything not set here comes from catalogs/cluster_base/argo_rollouts/values.yaml.
type ChartValues struct {
	Controller *Controller `json:"controller,omitempty"`
	Dashboard  *Dashboard  `json:"dashboard,omitempty"`
}

// Controller configures the rollouts controller.
type Controller struct {
	// Replicas is the controller replica count. The chart ships 2.
	Replicas *int32 `json:"replicas,omitempty"`
}

// Dashboard configures the rollouts dashboard.
type Dashboard struct {
	// Enabled turns the dashboard on. The chart ships it off.
	Enabled *bool `json:"enabled,omitempty"`
}

// Component is the catalog entry for argo-rollouts.
type Component struct {
	catalogs.ComponentEntry

	// Values overrides the chart's defaults. The zero value means "chart defaults".
	Values Values
}

// New returns the argo-rollouts component with its chart metadata filled in and no
// value overrides.
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
