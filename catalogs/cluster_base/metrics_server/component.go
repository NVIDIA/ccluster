/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package metricsserver describes the values of catalogs/cluster_base/metrics_server.
package metricsserver

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiregistrationv1 "k8s.io/kube-aggregator/pkg/apis/apiregistration/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
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

// APIService is the APIService through which the API server serves the resource metrics
// API, metrics.k8s.io, that HorizontalPodAutoscalers and KEDA's cpu and memory triggers
// read.
const APIService = "v1beta1.metrics.k8s.io"

// Verify checks that the resource metrics API is served: the APIService exists and is
// Available. The API server's aggregator sets Available only once the Service behind
// it answers discovery, and metrics-server is ready only after its first kubelet
// scrape, so Available means metrics are being served.
//
// It checks the API, not this chart's objects. A managed cluster such as GKE or AKS
// ships its own metrics-server under the same APIService, which this chart must not be
// installed over, and that one passes too, as does any other server of the API.
// Nothing in the chart depends on the cluster's domain, so the CCLClusterConfig spec is
// not consulted.
//
// r must be able to read apiregistration.k8s.io/v1 APIServices: the operator's scheme
// registers the kind and its ClusterRole grants get, list and watch.
func Verify(ctx context.Context, r client.Reader, _ cclusternvidiacomv1alpha1.CCLClusterConfigSpec) error {
	var svc apiregistrationv1.APIService
	if err := r.Get(ctx, client.ObjectKey{Name: APIService}, &svc); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("APIService %s not found", APIService)
		}
		return fmt.Errorf("reading APIService %s: %w", APIService, err)
	}
	for _, c := range svc.Status.Conditions {
		if c.Type != apiregistrationv1.Available {
			continue
		}
		if c.Status == apiregistrationv1.ConditionTrue {
			return nil
		}
		return fmt.Errorf("APIService %s not available: %s: %s", APIService, c.Reason, c.Message)
	}
	return fmt.Errorf("APIService %s not available", APIService)
}
