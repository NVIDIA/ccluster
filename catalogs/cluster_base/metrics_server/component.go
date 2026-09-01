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

import (
	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
)

const (
	// Name is the component's name in a CCLDeployment, and the key its values
	// nest under.
	Name = "metrics-server"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/metrics_server"
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

// New returns the metrics-server chart entry, installed into namespace with values.
// The zero Values means "chart defaults".
func New(
	namespace string,
	values Values,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	return catalogs.Entry(Chart, namespace, values)
}
