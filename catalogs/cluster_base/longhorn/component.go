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

// Package longhorn describes the values of catalogs/cluster_base/longhorn.
package longhorn

import (
	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
)

const (
	// Name is the component's name in a CCLComponentSet, and the key its values
	// nest under.
	Name = "longhorn"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/longhorn"
)

// Values is the top-level values document for catalogs/cluster_base/longhorn.
type Values struct {
	Longhorn *ChartValues `json:"longhorn,omitempty"`
}

// ChartValues are the upstream longhorn values worth setting per cluster.
//
// It is empty: the chart ships {} and no per-cluster tunable has been identified
// yet. It exists as a named struct rather than an anonymous one so that adding the
// first field later is a narrow change.
type ChartValues struct{}

// New returns the longhorn chart entry, installed into namespace with values.
// The zero Values means "chart defaults".
func New(
	namespace string,
	values Values,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	return catalogs.Entry(Chart, namespace, values)
}
