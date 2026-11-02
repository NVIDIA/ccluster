/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package computedeployment

import (
	"context"
	"fmt"

	"github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
)

// ReasonLoadBalancerPending: the SSH Service has no load balancer address yet.
const ReasonLoadBalancerPending = "LoadBalancerPending"

// Health judges a compute deployment as DefaultHealth does. It also names the wait for
// the SSH Service's load balancer address, ReasonLoadBalancerPending, when that Service is
// the only rendered object not ready. Argo CD gives that wait no message.
func Health(ctx context.Context, entry v1alpha1.ComponentEntry, observed catalogs.Observation) catalogs.Health {
	health := catalogs.DefaultHealth(ctx, entry, observed)
	if health.Status != v1alpha1.ComponentHealthProgressing || health.Reason != "" {
		return health
	}

	var notReady catalogs.ObservedResources
	for _, r := range observed.Resources {
		if r.Rendered() && r.Health != "" && r.Health != string(v1alpha1.ComponentHealthHealthy) {
			notReady = append(notReady, r)
		}
	}
	if len(notReady) == 1 && notReady[0].Group == "" && notReady[0].Kind == "Service" {
		health.Reason = ReasonLoadBalancerPending
		health.Message = fmt.Sprintf("Service %s is waiting for a load balancer address", notReady[0].Name)
	}
	return health
}
