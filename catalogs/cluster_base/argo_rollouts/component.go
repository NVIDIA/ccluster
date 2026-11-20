/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package argorollouts describes the values of catalogs/cluster_base/argo_rollouts.
package argorollouts

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
)

// Chart is the chart directory, relative to the repository root.
const Chart = "catalogs/cluster_base/argo_rollouts"

// Values is the top-level values document for catalogs/cluster_base/argo_rollouts.
// Its single key is the upstream chart's alias, because this chart wraps it as a
// dependency — which is the only reason the nesting exists.
type Values struct {
	ArgoRollouts *ChartValues `json:"argo-rollouts,omitempty"`
}

// ChartValues are the upstream argo-rollouts values worth setting per cluster.
// Anything not set here comes from catalogs/cluster_base/argo_rollouts/values.yaml.
type ChartValues struct {
	Controller *Controller `json:"controller,omitempty"`
	Dashboard  *Dashboard  `json:"dashboard,omitempty"`
}

// Controller configures the rollouts controller.
type Controller struct {
	// Replicas is the controller replica count. The chart ships 2.
	Replicas *int32 `json:"replicas,omitempty"`
}

// Dashboard configures the rollouts dashboard.
type Dashboard struct {
	// Enabled turns the dashboard on. The chart ships it off.
	Enabled *bool `json:"enabled,omitempty"`
}

// New returns the argo-rollouts chart entry, installed into namespace with values.
// The zero Values means "chart defaults".
func New(
	namespace string,
	values Values,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	return catalogs.Entry(Chart, namespace, values)
}

// RolloutCRD is the CustomResourceDefinition of the argoproj.io Rollout, which
// catalogs/deployments/inference_deployment renders and its ScaledObject scales.
const RolloutCRD = "rollouts.argoproj.io"

// ControllerLease is the coordination.k8s.io Lease the Argo Rollouts controller holds
// while it leads. The name is a constant in its source (controller/controller.go),
// whatever the release, and the controller creates it in its own namespace. Leader
// election is on by default, and this chart's two controller replicas need it.
const ControllerLease = "argo-rollouts-controller-lock"

// Verify checks that the cluster serves the Rollout kind the catalog's inference
// deployments render, and that a controller reconciles it: the CustomResourceDefinition
// RolloutCRD exists, and a controller holds ControllerLease and has renewed it lately.
// Without the CRD Argo CD cannot apply an inference_deployment at all; without the
// controller the Rollouts it applies never create pods.
//
// The Lease is what tells a running controller from an uninstalled one. Uninstalling
// the chart leaves the CRDs behind, and the controller does not release its Lease when
// it stops, so both stay; only the Lease's renewTime goes stale. A controller started
// with leader election off holds no Lease and fails here.
//
// It checks the API and the Lease, not this chart's objects, whose names follow the
// release name, so an Argo Rollouts installed by other means passes too. Nothing in the
// chart depends on the cluster's domain, so the CCLClusterConfig spec is not consulted.
func Verify(ctx context.Context, r client.Reader, _ cclusternvidiacomv1alpha1.CCLClusterConfigSpec) error {
	if err := catalogs.VerifyCRDExists(ctx, r, RolloutCRD); err != nil {
		return err
	}
	return catalogs.VerifyLeaseRenewed(ctx, r, ControllerLease)
}
