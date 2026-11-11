/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package dynamoplatform describes the values of catalogs/cluster_base/dynamo_platform:
// the NVIDIA Dynamo operator, which serves the DynamoGraphDeployment the
// catalogs/deployments/dynamo_deployment chart creates, with its NATS event plane. The
// chart bundles the Grove gang scheduler but leaves it off, as the legacy platform's
// production clusters do. Install it before any dynamo_deployment.
package dynamoplatform

import (
	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
)

const (
	// Name is the component's name in a CCLComponentSet.
	Name = "dynamo-platform"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/dynamo_platform"
)

// Values is the top-level values document for catalogs/cluster_base/dynamo_platform.
//
// It is empty: nothing needs setting per cluster yet, and the chart's values.yaml fixes
// the upstream values the catalog relies on.
type Values struct{}

// New returns the dynamo_platform chart entry, installed into namespace with values.
// The zero Values means "chart defaults".
func New(
	namespace string,
	values Values,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	return catalogs.Entry(Chart, namespace, values)
}
