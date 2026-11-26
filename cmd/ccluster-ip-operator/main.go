/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// ccluster-ip-operator runs in an infra-plane cluster.
//
// It runs every catalogs/cluster_base component's Verifier against the CCLClusterConfig
// singleton and reports the outcome there, along with the cluster's labelled
// LoadBalancers. It installs nothing.
package main

import (
	"fmt"
	"os"

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	apiregistrationv1 "k8s.io/kube-aggregator/pkg/apis/apiregistration/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	externalsecrets "github.com/NVIDIA/ccluster/catalogs/cluster_base/external_secrets"
	gatewayapi "github.com/NVIDIA/ccluster/catalogs/cluster_base/gateway_api"
	"github.com/NVIDIA/ccluster/internal/controller/ip"
	"github.com/NVIDIA/ccluster/internal/manager"
	verifierregistry "github.com/NVIDIA/ccluster/internal/registry/verifier_registry"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(cclusternvidiacomv1alpha1.AddToScheme(scheme))
	// APIServices, which metrics_server's Verifier reads.
	utilruntime.Must(apiregistrationv1.AddToScheme(scheme))
}

func main() {
	err := manager.Run(manager.Options{
		Name:             "ccluster-ip-operator",
		LeaderElectionID: "ip.ccluster.nvidia.com",
		Scheme:           scheme,
		// Leases, which the argo_rollouts and cert_manager Verifiers list by name. Caching
		// them would watch every node's heartbeat Lease and every controller's leader Lease.
		// And the kinds Verifiers check with catalogs.VerifyKindServed, which needs one
		// object's metadata per pass, not a watch on all of them.
		UncachedObjects: []client.Object{
			&coordinationv1.Lease{},
			metadataOf(externalsecrets.ExternalSecretKind),
			metadataOf(gatewayapi.HTTPRouteKind),
		},
		SetupControllers: setupControllers,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// metadataOf returns kind as a metadata-only object, the form in which UncachedObjects
// names a kind read with metav1.PartialObjectMetadata.
func metadataOf(kind schema.GroupVersionKind) client.Object {
	obj := &metav1.PartialObjectMetadata{}
	obj.SetGroupVersionKind(kind)
	return obj
}

// setupControllers registers every controller owned by the infra-plane operator.
func setupControllers(mgr ctrl.Manager) error {
	if err := (&ip.CCLClusterConfigReconciler{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		Verifiers: verifierregistry.Get(),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller CCLClusterConfig: %w", err)
	}
	return nil
}
