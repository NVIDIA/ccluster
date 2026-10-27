/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package keda describes the values of catalogs/cluster_base/keda.
package keda

import (
	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
)

const (
	// Name is the component's name in a CCLComponentSet, and the key its values
	// nest under.
	Name = "keda"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/keda"
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

// New returns the keda chart entry, installed into namespace with values.
// The zero Values means "chart defaults".
func New(
	namespace string,
	values Values,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	return catalogs.Entry(Chart, namespace, values)
}
