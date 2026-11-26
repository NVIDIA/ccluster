/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package gatewayapi describes the values of catalogs/cluster_base/gateway_api.
//
// cluster_base.Components installs it before istio. A cluster whose CRDs come from
// elsewhere leaves it out of its set. Where it is installed, it is the only installer of
// the CRDs: another chart that bundles them, such as Envoy Gateway's gateway-helm with
// its default crds.enabled, has to leave them out, or Argo CD applies both copies on
// every sync.
package gatewayapi

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
)

const (
	// Name is the component's name in a CCLComponentSet.
	Name = "gateway-api"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/gateway_api"
)

// Values is the top-level values document for catalogs/cluster_base/gateway_api.
//
// It is empty: the chart's values.yaml fixes the upstream chart to the Gateway API
// standard channel, without Envoy Gateway's own CRDs, and nothing in it varies per
// cluster.
type Values struct{}

// New returns the gateway-api chart entry, installed into namespace with values.
// Every object in the chart is cluster-scoped, so none of them lands in namespace.
func New(
	namespace string,
	values Values,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	return catalogs.Entry(Chart, namespace, values)
}

// crds are the kinds cert-manager uses with its Gateway API support on: it watches
// Gateways and creates an HTTPRoute for each ACME HTTP-01 challenge.
var crds = []string{
	"gateways.gateway.networking.k8s.io",
	"httproutes.gateway.networking.k8s.io",
}

// HTTPRouteKind is the kind Verify checks is served. The ip operator reads it from the
// API server, not its cache (see catalogs.VerifyKindServed).
var HTTPRouteKind = schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute"}

// Verify checks that the cluster has what cert_manager's GatewayAPI switch needs: the
// crds exist and gateway.networking.k8s.io/v1 is served. Without that API, cert-manager's
// controller exits at startup (pkg/controller/context.go in v1.21.3), so no certificate
// is issued.
func Verify(ctx context.Context, r client.Reader, _ cclusternvidiacomv1alpha1.CCLClusterConfigSpec) error {
	for _, crd := range crds {
		if err := catalogs.VerifyCRDExists(ctx, r, crd); err != nil {
			return err
		}
	}
	return catalogs.VerifyKindServed(ctx, r, HTTPRouteKind, "Gateway API before v1.0 does not serve v1")
}
