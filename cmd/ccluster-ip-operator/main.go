/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// ccluster-ip-operator runs in an infra-plane cluster.
//
// It starts a manager with health probes and a metrics server but registers no
// controllers yet; the CCLClusterConfig it will reconcile is already in its scheme. Add
// controllers to setupControllers below, with their reconcilers in internal/controller/ip.
package main

import (
	"fmt"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
	"github.com/NVIDIA/ccluster/internal/manager"
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

// setupControllers registers every controller owned by the infra-plane
// operator. There are none yet.
func setupControllers(_ ctrl.Manager) error {
	return nil
}
