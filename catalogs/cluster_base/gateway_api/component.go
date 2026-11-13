/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package gatewayapi describes the values of catalogs/cluster_base/gateway_api.
//
// The component is optional, so cluster_base.Components leaves it out. A plane that uses
// Gateway API adds it to its set before istio; a cluster that already has the CRDs,
// installed by other means, does not. Where it is installed, it is the only installer of
// the CRDs: another chart that bundles them, such as Envoy Gateway's gateway-helm with
// its default crds.enabled, has to leave them out, or Argo CD applies both copies on
// every sync.
package gatewayapi

import (
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
