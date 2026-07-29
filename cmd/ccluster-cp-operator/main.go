/*
Copyright 2026 NVIDIA CORPORATION & AFFILIATES.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// ccluster-cp-operator runs in the control-plane cluster.
package main

import (
	"fmt"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
	"github.com/NVIDIA/ccluster/internal/controller/cp"
	"github.com/NVIDIA/ccluster/internal/manager"
	// +kubebuilder:scaffold:imports
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(cclusternvidiacomv1alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

func main() {
	err := manager.Run(manager.Options{
		Name:             "ccluster-cp-operator",
		LeaderElectionID: "cp.ccluster.nvidia.com",
		Scheme:           scheme,
		SetupControllers: setupControllers,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// setupControllers registers every controller owned by the control-plane
// operator. New controllers scaffolded with `kubebuilder create api` must be
// wired up here by hand — the CLI only knows how to inject into cmd/main.go,
// which this project does not have.
func setupControllers(mgr ctrl.Manager) error {
	if err := (&cp.CCLInfraPlaneReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller CCLInfraPlane: %w", err)
	}
	// +kubebuilder:scaffold:builder
	return nil
}
