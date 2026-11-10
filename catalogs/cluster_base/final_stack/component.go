/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package finalstack describes the values of catalogs/cluster_base/final_stack.
package finalstack

import (
	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
)

const (
	// Name is the component's name in a CCLComponentSet.
	Name = "final-stack"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/final_stack"
)

// Values is the top-level values document for catalogs/cluster_base/final_stack.
//
// It is empty: this chart wraps no upstream dependency, and its one template, the
// cluster's cert-manager ClusterIssuer, reads no values.
type Values struct{}

// New returns the final-stack chart entry, installed into namespace with values.
// The zero Values means "chart defaults".
func New(
	namespace string,
	values Values,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	return catalogs.Entry(Chart, namespace, values)
}
