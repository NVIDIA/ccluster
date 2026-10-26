/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package opentelemetrycollector describes the values of catalogs/cluster_base/opentelemetry_collector.
package opentelemetrycollector

import (
	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
)

const (
	// Name is the component's name in a CCLComponentSet, and the key its values
	// nest under.
	Name = "opentelemetry-collector"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/opentelemetry_collector"
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

// New returns the opentelemetry-collector chart entry, installed into namespace with values.
// The zero Values means "chart defaults".
func New(
	namespace string,
	values Values,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	return catalogs.Entry(Chart, namespace, values)
}
