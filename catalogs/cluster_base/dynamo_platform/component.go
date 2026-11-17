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
	// Name is the component's name in a CCLComponentSet, and the key its values
	// nest under.
	Name = "dynamo-platform"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/dynamo_platform"
)

// Values is the top-level values document for catalogs/cluster_base/dynamo_platform.
type Values struct {
	DynamoPlatform *ChartValues `json:"dynamo-platform,omitempty"`
}

// ChartValues are the upstream dynamo-platform values worth setting per cluster.
//
// It is empty: no per-cluster tunable has been identified yet, and the chart's
// values.yaml fixes the upstream values the catalog relies on. It exists as a named
// struct rather than an anonymous one so that adding the first field later is a
// narrow change.
type ChartValues struct{}

// New returns the dynamo_platform chart entry, installed into namespace with values.
// The zero Values means "chart defaults".
func New(
	namespace string,
	values Values,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	return catalogs.Entry(Chart, namespace, values)
}
