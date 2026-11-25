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

// ControllerLease is the coordination.k8s.io Lease the cert-manager controller holds
// while it leads. The name is a constant in its source (cmd/controller/app/controller.go),
// whatever the release; the Lease lives in kube-system unless the chart's
// global.leaderElection.namespace moves it. Leader election is on by default, and this
// chart's two controller replicas need it.
const ControllerLease = "cert-manager-controller"

// crds are the CustomResourceDefinitions of the kinds Verify names.
var crds = []string{
	"certificates.cert-manager.io",
	"clusterissuers.cert-manager.io",
	"issuers.cert-manager.io",
}

// Verify checks that cert-manager accepts and acts on the resources the catalog applies:
// final_stack's acme-ca ClusterIssuer, and the Issuers and Certificates ingress_nginx
// creates for its admission webhook. The API server rejects every cert-manager.io write
// the webhook Webhook does not answer, so the webhook must be served. The CRDs of those
// kinds must exist: the webhook runs without them, and the upstream chart installs them
// only with installCRDs set, which is off by default. A controller must hold
// ControllerLease, since without one no Certificate is issued; a ClusterIssuer's Ready
// condition cannot show that, because it stays True after the controller stops.
//
// The webhook, CRDs and Lease are found by name, not by release, so a cert-manager
// installed by other means passes too. Nothing here depends on the cluster's domain, so
// the CCLClusterConfig spec is not consulted.
func Verify(ctx context.Context, r client.Reader, _ cclusternvidiacomv1alpha1.CCLClusterConfigSpec) error {
	if err := catalogs.VerifyValidatingWebhookServed(ctx, r, Webhook); err != nil {
		return err
	}
	for _, crd := range crds {
		if err := catalogs.VerifyCRDExists(ctx, r, crd); err != nil {
			return err
		}
	}
	return catalogs.VerifyLeaseRenewed(ctx, r, ControllerLease)
}
