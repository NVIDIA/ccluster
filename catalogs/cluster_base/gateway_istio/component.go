/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package gatewayistio describes the values of catalogs/cluster_base/gateway_istio, the
// cluster's shared Gateway API Gateway, gateway-system/ccl-gateway, which istiod serves.
// templates/gateway.yaml describes its listeners and which namespaces may attach to it.
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
// It is empty: other charts attach to the Gateway by its fixed name and namespace, and
// nothing in the chart varies per cluster.
type Values struct{}

// New returns the gateway-istio chart entry, installed into namespace with values. The
// chart's objects are all in gateway-system, whatever namespace is.
func New(
	namespace string,
	values Values,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	return catalogs.Entry(Chart, namespace, values)
}
