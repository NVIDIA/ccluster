/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package gatewayistio describes the values of catalogs/cluster_base/gateway_istio, the
// cluster's shared Gateway API Gateway, gateway-system/ccl-gateway, which istiod serves.
//
// The Gateway has only an HTTP listener on port 80, for ACME HTTP-01 challenges. Each
// deployment attaches its own HTTPS listener and certificate through a ListenerSet in
// its namespace, and its routes to that ListenerSet. Both are admitted only from
// namespaces labelled ccluster.nvidia.com/tenant: "true", which namespace_base sets.
package gatewayistio

import (
	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
)

const (
	// Name is the component's name in a CCLComponentSet.
	Name = "gateway-istio"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/gateway_istio"
)

// Values is the top-level values document for catalogs/cluster_base/gateway_istio.
//
// It is empty: the Gateway's name, namespace and listeners are what the deployment
// charts and acme-ca reference, so nothing in the chart varies per cluster.
type Values struct{}

// New returns the gateway-istio chart entry, installed into namespace with values. The
// chart's objects are all in gateway-system, whatever namespace is.
func New(
	namespace string,
	values Values,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	return catalogs.Entry(Chart, namespace, values)
}
