/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package prometheusadapter describes the values of catalogs/cluster_base/prometheus_adapter.
package prometheusadapter

import (
	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
)

const (
	// Name is the component's name in a CCLComponentSet, and the key its values
	// nest under.
	Name = "prometheus-adapter"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/prometheus_adapter"
)

// Values is the top-level values document for catalogs/cluster_base/prometheus_adapter.
type Values struct {
	PrometheusAdapter *ChartValues `json:"prometheus-adapter,omitempty"`
}

// ChartValues are the upstream prometheus-adapter values worth setting per cluster.
type ChartValues struct {
	Prometheus *Prometheus `json:"prometheus,omitempty"`
	Rules      *Rules      `json:"rules,omitempty"`
}

// Prometheus is where the adapter reads metrics from. The chart's default matches
// kube-prometheus-stack's pinned fullnameOverride, so changing one means changing
// the other.
type Prometheus struct {
	URL  *string `json:"url,omitempty"`
	Port *int32  `json:"port,omitempty"`
}

// Rules are the adapter's metric translation rules.
type Rules struct {
	Custom []map[string]any `json:"custom,omitempty"`
}

// New returns the prometheus-adapter chart entry, installed into namespace with values.
// The zero Values means "chart defaults".
func New(
	namespace string,
	values Values,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	return catalogs.Entry(Chart, namespace, values)
}
