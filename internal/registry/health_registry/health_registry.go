/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package healthregistry is the registry of catalog charts' health functions, which the
// cp operator runs to judge each deployed component's health.
package healthregistry

import (
	"context"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
	argorollouts "github.com/NVIDIA/ccluster/catalogs/cluster_base/argo_rollouts"
	certmanager "github.com/NVIDIA/ccluster/catalogs/cluster_base/cert_manager"
	finalstack "github.com/NVIDIA/ccluster/catalogs/cluster_base/final_stack"
	fluentbit "github.com/NVIDIA/ccluster/catalogs/cluster_base/fluent_bit"
	gatewayapi "github.com/NVIDIA/ccluster/catalogs/cluster_base/gateway_api"
	gpuoperator "github.com/NVIDIA/ccluster/catalogs/cluster_base/gpu_operator"
	ingressnginx "github.com/NVIDIA/ccluster/catalogs/cluster_base/ingress_nginx"
	"github.com/NVIDIA/ccluster/catalogs/cluster_base/istio"
	kaischeduler "github.com/NVIDIA/ccluster/catalogs/cluster_base/kai_scheduler"
	"github.com/NVIDIA/ccluster/catalogs/cluster_base/keda"
	kubeprometheusstack "github.com/NVIDIA/ccluster/catalogs/cluster_base/kube_prometheus_stack"
	"github.com/NVIDIA/ccluster/catalogs/cluster_base/longhorn"
	metricsserver "github.com/NVIDIA/ccluster/catalogs/cluster_base/metrics_server"
	networkoperator "github.com/NVIDIA/ccluster/catalogs/cluster_base/network_operator"
	prometheusadapter "github.com/NVIDIA/ccluster/catalogs/cluster_base/prometheus_adapter"
	computedeployment "github.com/NVIDIA/ccluster/catalogs/deployments/compute_deployment"
	dynamodeployment "github.com/NVIDIA/ccluster/catalogs/deployments/dynamo_deployment"
	inferencedeployment "github.com/NVIDIA/ccluster/catalogs/deployments/inference_deployment"
	jobdeployment "github.com/NVIDIA/ccluster/catalogs/deployments/job_deployment"
	namespacebase "github.com/NVIDIA/ccluster/catalogs/namespace_base"
	kaigpuqueues "github.com/NVIDIA/ccluster/catalogs/resources/kai_gpu_queues"
	persistentvolume "github.com/NVIDIA/ccluster/catalogs/resources/persistent_volume"
	"github.com/NVIDIA/ccluster/internal/registry"
)

// HealthFunc judges the health of one deployed component. A chart exports one as Health;
// a chart without one registers catalogs.DefaultHealth. The operator releases the next
// component only at Healthy or Succeeded.
//
// It reads live objects only through observed.Reader, and only the few the tree points
// at. It must still judge what the previous catalog revision renders. See DESIGN.md §7,
// "Component health".
type HealthFunc func(
	ctx context.Context, entry cclusternvidiacomv1alpha1.ComponentEntry, observed catalogs.Observation,
) catalogs.Health

// Registry maps a chart to its health function. The zero value has none for any chart.
type Registry struct {
	registry.Registry[HealthFunc]
}

// Get returns the registry of every catalog chart's health function. TestGet fails when
// it and components.All disagree. A chart without a function of its own registers
// catalogs.DefaultHealth; one with a function registers its package's Health.
func Get() *Registry {
	r := &Registry{}
	r.Register(namespacebase.Chart, catalogs.DefaultHealth)
	r.Register(argorollouts.Chart, catalogs.DefaultHealth)
	r.Register(certmanager.Chart, catalogs.DefaultHealth)
	r.Register(finalstack.Chart, catalogs.DefaultHealth)
	r.Register(fluentbit.Chart, catalogs.DefaultHealth)
	r.Register(gatewayapi.Chart, catalogs.DefaultHealth)
	r.Register(gpuoperator.Chart, catalogs.DefaultHealth)
	r.Register(ingressnginx.Chart, catalogs.DefaultHealth)
	r.Register(istio.Chart, catalogs.DefaultHealth)
	r.Register(kaischeduler.Chart, catalogs.DefaultHealth)
	r.Register(keda.Chart, catalogs.DefaultHealth)
	r.Register(kubeprometheusstack.Chart, catalogs.DefaultHealth)
	r.Register(longhorn.Chart, catalogs.DefaultHealth)
	r.Register(metricsserver.Chart, catalogs.DefaultHealth)
	r.Register(networkoperator.Chart, catalogs.DefaultHealth)
	r.Register(prometheusadapter.Chart, catalogs.DefaultHealth)
	r.Register(computedeployment.Chart, computedeployment.Health)
	r.Register(dynamodeployment.Chart, dynamodeployment.Health)
	r.Register(inferencedeployment.Chart, catalogs.DefaultHealth)
	r.Register(jobdeployment.Chart, jobdeployment.Health)
	r.Register(kaigpuqueues.Chart, catalogs.DefaultHealth)
	r.Register(persistentvolume.Chart, catalogs.DefaultHealth)
	return r
}
