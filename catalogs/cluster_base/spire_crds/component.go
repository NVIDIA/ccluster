/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package spirecrds describes the values of catalogs/cluster_base/spire_crds.
package spirecrds

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
	"github.com/NVIDIA/ccluster/catalogs/common"
)

const (
	// Name is the component's name in a CCLComponentSet, and the key its values
	// nest under.
	Name = "spire-crds"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/spire_crds"
)

// Values is the top-level values document for catalogs/cluster_base/spire_crds.
type Values struct {
	SpireCRDs *ChartValues `json:"spire-crds,omitempty"`
}

// ChartValues are the upstream spire-crds values worth setting per cluster.
type ChartValues struct {
	// Annotations are applied to the CRDs. The chart ships
	// helm.sh/resource-policy: keep, so uninstalling does not delete every
	// SPIRE custom resource in the cluster.
	Annotations map[string]string `json:"annotations,omitempty"`
}

// New returns the spire-crds chart entry, installed into namespace with values.
// The zero Values means "chart defaults".
func New(
	namespace string,
	values Values,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	return catalogs.Entry(Chart, namespace, values)
}

// CRDs are the CustomResourceDefinitions the upstream spire-crds chart installs, which
// the spire chart's controller manager and final_stack's ClusterSPIFFEIDs depend on.
var CRDs = []string{
	"clusterfederatedtrustdomains.spire.spiffe.io",
	"clusterspiffeids.spire.spiffe.io",
	"clusterstaticentries.spire.spiffe.io",
}

// Verify checks that every CRD in CRDs is Established. The chart installs nothing else
// and nothing in it depends on the cluster's domain, so there is no configuration to
// compare against the CCLClusterConfig spec.
func Verify(ctx context.Context, r client.Reader, _ cclusternvidiacomv1alpha1.CCLClusterConfigSpec) error {
	return common.CRDsEstablished(ctx, r, CRDs...)
}
