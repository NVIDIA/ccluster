/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package finalstack describes the values of catalogs/cluster_base/final_stack.
package finalstack

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
	certmanagerv1 "github.com/NVIDIA/ccluster/internal/api/certmanager/v1"
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

// ClusterIssuer is the name of the cert-manager ClusterIssuer this chart renders, which
// the Ingresses of catalogs/deployments/inference_deployment and dynamo_deployment
// request with the cert-manager.io/cluster-issuer annotation.
const ClusterIssuer = "acme-ca"

// Verify checks that the ClusterIssuer the catalog's Ingresses request can issue: it
// exists under the name they hardcode, and cert-manager reports it Ready. For this
// chart's ACME issuer, cert-manager sets Ready once it has registered the issuer's
// account with Let's Encrypt, so Ready also means the cert-manager controller runs and
// reaches the ACME server. It does not mean HTTP-01 challenges will succeed, which needs
// the Ingress hosts to resolve publicly and port 80 of the ingress LoadBalancer to be
// reachable from the internet.
//
// Any issuer named ClusterIssuer passes, so a cluster that brings its own, such as a
// platform-managed DNS-01 issuer of that name, verifies too. A cluster without
// cert-manager has no ClusterIssuer kind, and that is reported as such rather than as a
// failed read. Nothing here depends on the cluster's domain, so the CCLClusterConfig spec
// is not consulted.
//
// r must be able to read cert-manager.io/v1 ClusterIssuers: the operator's scheme
// registers internal/api/certmanager/v1 and its ClusterRole grants get, list and watch.
func Verify(ctx context.Context, r client.Reader, _ cclusternvidiacomv1alpha1.CCLClusterConfigSpec) error {
	var issuer certmanagerv1.ClusterIssuer
	if err := r.Get(ctx, client.ObjectKey{Name: ClusterIssuer}, &issuer); err != nil {
		switch {
		case meta.IsNoMatchError(err):
			return errors.New("ClusterIssuer kind not served, is cert-manager installed?")
		case apierrors.IsNotFound(err):
			return fmt.Errorf("ClusterIssuer %s not found", ClusterIssuer)
		}
		return fmt.Errorf("reading ClusterIssuer %s: %w", ClusterIssuer, err)
	}
	for _, c := range issuer.Status.Conditions {
		if c.Type != certmanagerv1.IssuerConditionReady {
			continue
		}
		if c.Status == metav1.ConditionTrue {
			return nil
		}
		return fmt.Errorf("ClusterIssuer %s not ready: %s: %s", ClusterIssuer, c.Reason, c.Message)
	}
	return fmt.Errorf("ClusterIssuer %s not ready", ClusterIssuer)
}
