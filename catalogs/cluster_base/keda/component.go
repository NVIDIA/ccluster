/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package keda describes the values of catalogs/cluster_base/keda.
package keda

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
)

const (
	// Name is the component's name in a CCLComponentSet, and the key its values
	// nest under.
	Name = "keda"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/keda"
)

// Values is the top-level values document for catalogs/cluster_base/keda.
type Values struct {
	Keda *ChartValues `json:"keda,omitempty"`
}

// ChartValues are the upstream keda values worth setting per cluster.
type ChartValues struct {
	Operator      *Replicas `json:"operator,omitempty"`
	MetricsServer *Replicas `json:"metricsServer,omitempty"`
	Webhooks      *Toggle   `json:"webhooks,omitempty"`
}

// Replicas is a subchart's replica count.
type Replicas struct {
	ReplicaCount *int32 `json:"replicaCount,omitempty"`
}

// Toggle is a subchart's enable flag.
type Toggle struct {
	// Enabled turns the feature on.
	Enabled *bool `json:"enabled,omitempty"`
}

// New returns the keda chart entry, installed into namespace with values.
// The zero Values means "chart defaults".
func New(
	namespace string,
	values Values,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	return catalogs.Entry(Chart, namespace, values)
}

// ScaledObjectCRD is the CustomResourceDefinition of the keda.sh ScaledObject, which
// catalogs/deployments/inference_deployment and dynamo_deployment render.
const ScaledObjectCRD = "scaledobjects.keda.sh"

// APIService is the APIService through which KEDA's metrics server serves the external
// metrics API, external.metrics.k8s.io, that the HorizontalPodAutoscalers KEDA creates
// for ScaledObjects read.
const APIService = "v1beta1.external.metrics.k8s.io"

// Verify checks that the ScaledObjects the catalog's deployments render can scale: the
// CustomResourceDefinition ScaledObjectCRD exists, so Argo CD can apply them, and the
// APIService APIService is Available, so the HPAs KEDA creates from them can read their
// metrics. Available means the server behind the APIService answers, and only one
// server can back it; the CRD is what makes it KEDA's.
//
// It checks KEDA's APIs, not this chart's objects, so a KEDA installed by other means
// passes too. It does not check KEDA's operator, which turns ScaledObjects into HPAs and
// has no API of its own, nor its admission webhook, which the chart registers with
// failurePolicy Ignore. Nothing here depends on the cluster's domain, so the
// CCLClusterConfig spec is not consulted.
func Verify(ctx context.Context, r client.Reader, _ cclusternvidiacomv1alpha1.CCLClusterConfigSpec) error {
	if err := catalogs.VerifyCRDExists(ctx, r, ScaledObjectCRD); err != nil {
		return err
	}
	return catalogs.VerifyAPIServiceAvailable(ctx, r, APIService)
}
