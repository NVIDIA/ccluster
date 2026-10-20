/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package statusregistry is the registry of catalog charts' status functions, which the
// cp operator runs to report each deployed component's state.
package statusregistry

import (
	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
	argorollouts "github.com/NVIDIA/ccluster/catalogs/cluster_base/argo_rollouts"
	certmanager "github.com/NVIDIA/ccluster/catalogs/cluster_base/cert_manager"
	externaldns "github.com/NVIDIA/ccluster/catalogs/cluster_base/external_dns"
	finalstack "github.com/NVIDIA/ccluster/catalogs/cluster_base/final_stack"
	fluentbit "github.com/NVIDIA/ccluster/catalogs/cluster_base/fluent_bit"
	gpuoperator "github.com/NVIDIA/ccluster/catalogs/cluster_base/gpu_operator"
	ingressnginx "github.com/NVIDIA/ccluster/catalogs/cluster_base/ingress_nginx"
	"github.com/NVIDIA/ccluster/catalogs/cluster_base/istio"
	"github.com/NVIDIA/ccluster/catalogs/cluster_base/keda"
	kubeprometheusstack "github.com/NVIDIA/ccluster/catalogs/cluster_base/kube_prometheus_stack"
	"github.com/NVIDIA/ccluster/catalogs/cluster_base/longhorn"
	metricsserver "github.com/NVIDIA/ccluster/catalogs/cluster_base/metrics_server"
	networkoperator "github.com/NVIDIA/ccluster/catalogs/cluster_base/network_operator"
	otelcollector "github.com/NVIDIA/ccluster/catalogs/cluster_base/opentelemetry_collector"
	prometheusadapter "github.com/NVIDIA/ccluster/catalogs/cluster_base/prometheus_adapter"
	"github.com/NVIDIA/ccluster/catalogs/cluster_base/spire"
	spirecrds "github.com/NVIDIA/ccluster/catalogs/cluster_base/spire_crds"
	computedeployment "github.com/NVIDIA/ccluster/catalogs/deployments/compute_deployment"
	dynamodeployment "github.com/NVIDIA/ccluster/catalogs/deployments/dynamo_deployment"
	inferencedeployment "github.com/NVIDIA/ccluster/catalogs/deployments/inference_deployment"
	jobdeployment "github.com/NVIDIA/ccluster/catalogs/deployments/job_deployment"
	namespacebase "github.com/NVIDIA/ccluster/catalogs/namespace_base"
	kaigpuqueues "github.com/NVIDIA/ccluster/catalogs/resources/kai_gpu_queues"
	persistentvolume "github.com/NVIDIA/ccluster/catalogs/resources/persistent_volume"
	"github.com/NVIDIA/ccluster/internal/registry"
)

// ReasonStatusNotImplemented is the reason reported for a chart without a status
// function of its own.
const ReasonStatusNotImplemented = "StatusNotImplemented"

// StatusFunc is the signature of a chart's status function. The function itself is a
// plain function in the chart's package, conventionally named Status; the chart package
// does not import this one, and Register checks the signature at compile time.
//
// It reports the component's state from entry, the chart, namespace and values the
// component was deployed with, and observed, everything the operator read about the
// component's objects through Argo CD before calling it. It is pure: it does no I/O and
// cannot fail. The operator handles read failures itself and does not call a chart's
// function without an observation. A resource whose Manifest is nil had gone by the
// time it was read; one with no Events had none recorded.
//
// The operator calls each component's function from its own goroutine, so a function
// must not keep state between calls. A panic is recovered and reported as Unknown, but is
// a bug in the function. The Message is shown to users next to the component, so it
// should be short and specific, e.g. `container "main" is in CrashLoopBackOff`.
type StatusFunc func(entry cclusternvidiacomv1alpha1.ComponentEntry, observed catalogs.ObservedResources) catalogs.Status

// unknownStatus is registered for a chart without a status function of its own yet: it
// reports the component's state as Unknown without looking.
func unknownStatus(cclusternvidiacomv1alpha1.ComponentEntry, catalogs.ObservedResources) catalogs.Status {
	return catalogs.Status{
		State:   cclusternvidiacomv1alpha1.ComponentStateUnknown,
		Reason:  ReasonStatusNotImplemented,
		Message: "The chart does not report its status yet",
	}
}

// Registry maps a chart to its status function. The zero value has none for any chart.
type Registry struct {
	registry.Registry[StatusFunc]
}

// Get returns the registry of every catalog chart's status function. TestGet fails when
// it and components.All disagree. A chart without a function of its own registers
// unknownStatus; one with a function registers its package's Status.
func Get() *Registry {
	r := &Registry{}
	r.Register(namespacebase.Chart, unknownStatus)
	r.Register(argorollouts.Chart, unknownStatus)
	r.Register(certmanager.Chart, unknownStatus)
	r.Register(externaldns.Chart, unknownStatus)
	r.Register(finalstack.Chart, unknownStatus)
	r.Register(fluentbit.Chart, unknownStatus)
	r.Register(gpuoperator.Chart, unknownStatus)
	r.Register(ingressnginx.Chart, unknownStatus)
	r.Register(istio.Chart, unknownStatus)
	r.Register(keda.Chart, unknownStatus)
	r.Register(kubeprometheusstack.Chart, unknownStatus)
	r.Register(longhorn.Chart, unknownStatus)
	r.Register(metricsserver.Chart, unknownStatus)
	r.Register(networkoperator.Chart, unknownStatus)
	r.Register(otelcollector.Chart, unknownStatus)
	r.Register(prometheusadapter.Chart, unknownStatus)
	r.Register(spire.Chart, unknownStatus)
	r.Register(spirecrds.Chart, unknownStatus)
	r.Register(computedeployment.Chart, unknownStatus)
	r.Register(dynamodeployment.Chart, unknownStatus)
	r.Register(inferencedeployment.Chart, unknownStatus)
	r.Register(jobdeployment.Chart, unknownStatus)
	r.Register(kaigpuqueues.Chart, unknownStatus)
	r.Register(persistentvolume.Chart, unknownStatus)
	return r
}
