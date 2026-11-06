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

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/internal/controller/ip"
	"github.com/NVIDIA/ccluster/internal/manager"
	verifierregistry "github.com/NVIDIA/ccluster/internal/registry/verifier_registry"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(cclusternvidiacomv1alpha1.AddToScheme(scheme))
}

func main() {
	err := manager.Run(manager.Options{
		Name:             "ccluster-ip-operator",
		LeaderElectionID: "ip.ccluster.nvidia.com",
		Scheme:           scheme,
		SetupControllers: setupControllers,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
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
