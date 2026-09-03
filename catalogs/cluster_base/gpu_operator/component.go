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

// Package gpuoperator describes the values of catalogs/cluster_base/gpu_operator.
package gpuoperator

import (
	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
)

const (
	// Name is the component's name in a CCLComponentSet, and the key its values
	// nest under.
	Name = "gpu-operator"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/gpu_operator"
)

// Values is the top-level values document for catalogs/cluster_base/gpu_operator.
type Values struct {
	GPUOperator *ChartValues `json:"gpu-operator,omitempty"`
}

// ChartValues are the upstream gpu-operator values worth setting per cluster.
//
// Driver and Toolkit are the important ones, and the reason every optional scalar
// in these packages is a pointer. The chart ships both disabled for k3d and kind,
// where the node is a container sharing the host kernel and the in-cluster driver
// DaemonSet cannot load modules — there, the host supplies the driver and the
// container toolkit. On bare metal, EKS and GKE both must be enabled. A plain bool
// could not express the difference between "leave the chart's default" and
// "explicitly false".
type ChartValues struct {
	// Driver installs the NVIDIA driver in-cluster. The chart ships it off.
	Driver *Toggle `json:"driver,omitempty"`

	// Toolkit installs the NVIDIA container toolkit. The chart ships it off.
	Toolkit *Toggle `json:"toolkit,omitempty"`

	// NFD is node feature discovery, which labels nodes by GPU model.
	NFD *Toggle `json:"nfd,omitempty"`

	// CDI is the Container Device Interface.
	CDI *CDI `json:"cdi,omitempty"`

	// MIG configures Multi-Instance GPU partitioning.
	MIG *MIG `json:"mig,omitempty"`
}

// CDI configures the Container Device Interface.
type CDI struct {
	Enabled *bool `json:"enabled,omitempty"`
	// Default makes CDI the default injection mechanism.
	Default *bool `json:"default,omitempty"`
}

// MIG configures Multi-Instance GPU partitioning.
type MIG struct {
	// Strategy is how MIG devices are advertised: none, single or mixed.
	Strategy *string `json:"strategy,omitempty"`
}

// Toggle is a subchart's enable flag.
type Toggle struct {
	// Enabled turns the feature on.
	Enabled *bool `json:"enabled,omitempty"`
}

// New returns the gpu-operator chart entry, installed into namespace with values.
// The zero Values means "chart defaults".
func New(
	namespace string,
	values Values,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	return catalogs.Entry(Chart, namespace, values)
}
