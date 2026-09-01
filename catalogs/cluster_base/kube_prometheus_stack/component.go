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

// Package kubeprometheusstack describes the values of catalogs/cluster_base/kube_prometheus_stack.
package kubeprometheusstack

import "github.com/NVIDIA/ccluster/catalogs"

const (
	// Name is the component's name in a CCLDeployment, and the key its values
	// nest under.
	Name = "kube-prometheus-stack"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/kube_prometheus_stack"
	// Namespace is where kube-prometheus-stack is installed.
	Namespace = "prometheus"
)

// Values is the top-level values document for
// catalogs/cluster_base/kube_prometheus_stack.
//
// It has a second top-level key beside the chart's own: prometheusRules toggles
// PrometheusRule and PodMonitor resources rendered from this chart's templates/,
// which depend on the monitoring.coreos.com CRDs the chart itself installs.
type Values struct {
	KubePrometheusStack *ChartValues     `json:"kube-prometheus-stack,omitempty"`
	PrometheusRules     *PrometheusRules `json:"prometheusRules,omitempty"`
}

// ChartValues are the upstream kube-prometheus-stack values worth setting per cluster.
type ChartValues struct {
	Grafana    *Toggle     `json:"grafana,omitempty"`
	Prometheus *Prometheus `json:"prometheus,omitempty"`
}

// Prometheus configures the Prometheus instance.
type Prometheus struct {
	PrometheusSpec *PrometheusSpec `json:"prometheusSpec,omitempty"`
}

// PrometheusSpec is the Prometheus custom resource's spec.
type PrometheusSpec struct {
	// ExternalLabels are attached to every scraped series. The chart ships
	// cluster_name: ccluster, which should be the real cluster name.
	ExternalLabels map[string]string `json:"externalLabels,omitempty"`

	// RemoteWrite ships samples to a remote store, AWS AMP for instance. The
	// chart ships it empty so a fresh install does not reach an unconfigured
	// endpoint.
	RemoteWrite []map[string]any `json:"remoteWrite,omitempty"`
}

// PrometheusRules toggles the rules and monitors this chart renders itself.
type PrometheusRules struct {
	PodAlerts  *Toggle `json:"podAlerts,omitempty"`
	EnvoyStats *Toggle `json:"envoyStats,omitempty"`
}

// Toggle is a subchart's enable flag.
type Toggle struct {
	// Enabled turns the feature on.
	Enabled *bool `json:"enabled,omitempty"`
}

// Component is the catalog entry for kube-prometheus-stack.
type Component struct {
	catalogs.ComponentEntry

	// Values overrides the chart's defaults. The zero value means "chart
	// defaults".
	Values Values
}

// New returns the kube-prometheus-stack component with its chart metadata filled in and no value
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
