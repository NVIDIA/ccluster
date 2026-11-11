/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package certmanager describes the values of catalogs/cluster_base/cert_manager.
package certmanager

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
)

const (
	// Name is the component's name in a CCLComponentSet, and the key its values
	// nest under.
	Name = "cert-manager"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/cert_manager"
)

// Values is the top-level values document for catalogs/cluster_base/cert_manager.
type Values struct {
	CertManager *ChartValues `json:"cert-manager,omitempty"`
}

// ChartValues are the upstream cert-manager values worth setting per cluster.
type ChartValues struct {
	// ReplicaCount is the controller replica count. The chart ships 2.
	ReplicaCount *int32 `json:"replicaCount,omitempty"`

	// Prometheus scraping is shipped off, because the ServiceMonitor CRD only
	// exists once kube-prometheus-stack is deployed. Enable it after that.
	Prometheus *PrometheusValues `json:"prometheus,omitempty"`
}

// PrometheusValues configures cert-manager's metrics scraping.
type PrometheusValues struct {
	Enabled *bool `json:"enabled,omitempty"`

	// ServiceMonitor is spelled lower-case in this chart, unlike every other
	// chart in the catalog.
	ServiceMonitor *Toggle `json:"servicemonitor,omitempty"`
}

// Toggle is a subchart's enable flag.
type Toggle struct {
	// Enabled turns the feature on.
	Enabled *bool `json:"enabled,omitempty"`
}

// New returns the cert-manager chart entry, installed into namespace with values.
// The zero Values means "chart defaults".
func New(
	namespace string,
	values Values,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	return catalogs.Entry(Chart, namespace, values)
}

// Webhook is the entry name of cert-manager's validating admission webhook. The
// upstream chart and manifests fix it, whatever the release is called.
const Webhook = "webhook.cert-manager.io"

// Verify checks that cert-manager accepts the resources the catalog applies: final_stack's
// acme-ca ClusterIssuer, and the Issuers and Certificates ingress_nginx creates for its
// admission webhook. The API server sends every cert-manager.io write to cert-manager's
// webhook and rejects it while the webhook is unreachable, so Verify checks that the
// webhook Webhook is registered, that cainjector has injected its CA bundle, and that the
// Service behind it has a ready endpoint.
//
// It finds the webhook by its entry name and follows it to its Service, so cert-manager
// passes under any release name, in any namespace, however it was installed. It does not
// check that the controller issues certificates: final_stack's ClusterIssuer is where
// that shows. Nothing here depends on the cluster's domain, so the CCLClusterConfig spec
// is not consulted.
func Verify(ctx context.Context, r client.Reader, _ cclusternvidiacomv1alpha1.CCLClusterConfigSpec) error {
	return catalogs.VerifyValidatingWebhookServed(ctx, r, Webhook)
}
