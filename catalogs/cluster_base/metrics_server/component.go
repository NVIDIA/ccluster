/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package metricsserver describes the values of catalogs/cluster_base/metrics_server.
package metricsserver

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
	"github.com/NVIDIA/ccluster/catalogs/common"
)

const (
	// Name is the component's name in a CCLComponentSet, and the key its values
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

// APIService is the APIService metrics-server registers, through which the
// HorizontalPodAutoscaler and kubectl top read pod and node CPU and memory.
const APIService = "v1beta1.metrics.k8s.io"

// Verify checks that APIService is Available. It does not look for this chart's
// Deployment: GKE, AKS, OpenShift and k3s run a metrics-server of their own, and the
// cluster needs the resource metrics API served, not served by this chart. Nothing in
// the chart depends on the cluster's domain.
func Verify(ctx context.Context, r client.Reader, _ cclusternvidiacomv1alpha1.CCLClusterConfigSpec) error {
	return common.APIServiceAvailable(ctx, r, APIService)
}
