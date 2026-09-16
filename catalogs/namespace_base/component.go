/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package namespacebase describes catalogs/namespace_base: the baseline a tenant
// namespace gets before any workload chart is installed into it. It sits directly under
// catalogs/ rather than in deployments/ because it is not a workload; it is what the
// workload charts assume is already there. It labels the
// namespace for Istio injection and the baseline Pod Security Standard, enforces mTLS,
// scopes sidecar egress to the namespace and istio-system, restricts ingress to the
// namespace itself, istiod, ingress-nginx and Prometheus, restricts egress to the same
// plus DNS and the public internet, creates the shared custom-metrics scraper, and the
// hostPath model cache volumes the inference chart can mount.
package namespacebase

import (
	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
)

// Chart is the chart directory, relative to the repository root.
const Chart = "catalogs/namespace_base"

// Values is the top-level values document for catalogs/namespace_base. The
// chart takes none: everything it creates is named after, or scoped to, the release
// namespace, which is the namespace passed to New.
type Values struct{}

// New returns the namespace_base chart entry for namespace. It should be the first
// component of a tenant's CCLComponentSet, since the workload charts rely on the
// ecr-image-pull Secret and the model cache claim it creates.
func New(
	namespace string,
	values Values,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	return catalogs.Entry(Chart, namespace, values)
}
