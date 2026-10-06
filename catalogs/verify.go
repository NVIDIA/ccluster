/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package catalogs

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
)

// Verifier is implemented by a chart package that can check, from live objects in the
// cluster the chart is installed in, that its component is installed and configured for
// that cluster. The infra-plane operator runs every Verifier in components.ClusterBase and
// reports the outcome on CCLClusterConfig.
//
// Verification reads the cluster only. It never consults Argo CD or Helm release state,
// so it holds whichever of the two installed the chart: the control plane through a
// CCLComponentSet, or a user with helm. ctx is the reconcile context every
// controller-runtime read needs; r is a cached, read-only client; cfg is the cluster's
// declared configuration, for checks such as "spire's trust domain is this cluster's
// domain".
//
// The operator runs all Verifiers concurrently, so an implementation must not rely on
// another component's Verifier having run, and must be safe to call from its own
// goroutine; r is safe for concurrent reads.
type Verifier interface {
	Verify(ctx context.Context, r client.Reader, cfg cclusternvidiacomv1alpha1.CCLClusterConfigSpec) bool
}

// DefaultVerifier is the Verifier every cluster_base component starts with: it reports
// the component as verified without looking. A component replaces it with a check of
// its own when it has something to look at.
type DefaultVerifier struct{}

// Compile-time check that DefaultVerifier satisfies Verifier; the blank variable is
// never used and costs nothing at runtime.
var _ Verifier = DefaultVerifier{}

// Verify always reports true.
func (DefaultVerifier) Verify(context.Context, client.Reader, cclusternvidiacomv1alpha1.CCLClusterConfigSpec) bool {
	return true
}
