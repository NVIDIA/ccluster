/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package kubeprometheusstack describes the values of catalogs/cluster_base/kube_prometheus_stack.
package kubeprometheusstack

import (
	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
)

const (
	// Name is the component's name in a CCLComponentSet, and the key its values
	// nest under.
	Name = "kube-prometheus-stack"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/kube_prometheus_stack"
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

// New returns the kube-prometheus-stack chart entry, installed into namespace with values.
// The zero Values means "chart defaults".
func New(
	namespace string,
	values Values,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	return catalogs.Entry(Chart, namespace, values)
}
