/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package longhorn describes the values of catalogs/cluster_base/longhorn.
package longhorn

import (
	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
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
