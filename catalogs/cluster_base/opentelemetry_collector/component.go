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

// Package opentelemetrycollector describes the values of catalogs/cluster_base/opentelemetry_collector.
package opentelemetrycollector

import "github.com/NVIDIA/ccluster/catalogs"

const (
	// Name is the component's name in a CCLDeployment, and the key its values
	// nest under.
	Name = "opentelemetry-collector"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/opentelemetry_collector"
	// Namespace is where the OpenTelemetry collector is installed.
	Namespace = "opentelemetry"
)

// Values is the top-level values document for
// catalogs/cluster_base/opentelemetry_collector.
type Values struct {
	OpenTelemetryCollector *ChartValues `json:"opentelemetry-collector,omitempty"`
}

// ChartValues are the upstream opentelemetry-collector values worth setting per
// cluster. Mode and Image are both mandatory in chart 0.130.2 — upstream removed
// their defaults to force callers to pick deliberately — so the catalog chart ships
// values for them and they are only overridden here.
type ChartValues struct {
	// Mode is the collector's deployment mode: deployment, daemonset,
	// statefulset or sidecar.
	Mode *string `json:"mode,omitempty"`

	Image *Image `json:"image,omitempty"`

	// Config is the collector's own configuration: receivers, processors,
	// exporters and pipelines. It is untyped because it is OpenTelemetry's
	// schema rather than the chart's, and it changes independently of both.
	// The chart ships a stub that accepts OTLP and writes to the debug
	// exporter; replace it with real exporters.
	Config map[string]any `json:"config,omitempty"`
}

// Image is the collector image. The catalog chart ships the contrib
// distribution, which is the one carrying most exporters.
type Image struct {
	Repository *string `json:"repository,omitempty"`
	Tag        *string `json:"tag,omitempty"`
}

// Component is the catalog entry for opentelemetry-collector.
type Component struct {
	catalogs.ComponentEntry

	// Values overrides the chart's defaults. The zero value means "chart
	// defaults".
	Values Values
}

// New returns the opentelemetry-collector component with its chart metadata filled in and no value
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
