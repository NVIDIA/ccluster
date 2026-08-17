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

// ccluster-ip-operator runs in an infra-plane cluster.
//
// This is a placeholder: it starts a manager with health probes and a metrics
// server but registers no controllers yet. Add them to setupControllers below,
// with their reconcilers in internal/controller/ip.
package main

import (
	"fmt"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/NVIDIA/ccluster/internal/manager"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
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
