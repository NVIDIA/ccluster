/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package driftregistry is the registry of catalog charts' drift: fields of their objects
// that are allowed to differ from the rendered chart, which the cp operator writes into
// each Application as Argo CD ignoreDifferences.
package driftregistry

import (
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
	argocdv1alpha1 "github.com/NVIDIA/ccluster/internal/argocd/v1alpha1"
	"github.com/NVIDIA/ccluster/internal/registry"
)

// DriftFunc is the signature of a chart's drift: Argo CD ignoreDifferences rules for the
// fields of its objects that are allowed to drift from the rendered chart. The rules
// themselves are a plain function in the chart's package, conventionally named Drift;
// the chart package does not import this one, and Register checks the signature at
// compile time.
//
// The rules name fields of the chart's objects that a controller on the infra-plane
// cluster owns by design, so the rendered value is never the live one and a full diff
// would keep the Application OutOfSync, stalling every later component. Each call
// returns a new slice: the operator writes it into an Application spec.
type DriftFunc func() []argocdv1alpha1.ResourceIgnoreDifferences

// noDrift is registered for a chart whose objects are diffed in full.
func noDrift() []argocdv1alpha1.ResourceIgnoreDifferences {
	return nil
}

// Registry maps a chart to its drift rules. The zero value
// has no rules for any chart.
type Registry struct {
	registry.Registry[DriftFunc]
}

// Drift returns the registry of every catalog chart's drift rules. TestDrift fails when
// it and components.All disagree. A chart diffed in full registers noDrift; one with
// rules registers its package's Drift.
func Drift() Registry {
	var r Registry
	r.Register(namespacebase.Chart, noDrift)
	r.Register(argorollouts.Chart, noDrift)
	r.Register(certmanager.Chart, noDrift)
	r.Register(externaldns.Chart, noDrift)
	r.Register(finalstack.Chart, noDrift)
	r.Register(fluentbit.Chart, noDrift)
	r.Register(gpuoperator.Chart, noDrift)
	r.Register(ingressnginx.Chart, noDrift)
	r.Register(istio.Chart, istio.Drift)
	r.Register(keda.Chart, noDrift)
	r.Register(kubeprometheusstack.Chart, noDrift)
	r.Register(longhorn.Chart, noDrift)
	r.Register(metricsserver.Chart, noDrift)
	r.Register(networkoperator.Chart, noDrift)
	r.Register(otelcollector.Chart, noDrift)
	r.Register(prometheusadapter.Chart, noDrift)
	r.Register(spire.Chart, noDrift)
	r.Register(spirecrds.Chart, noDrift)
	r.Register(computedeployment.Chart, noDrift)
	r.Register(dynamodeployment.Chart, noDrift)
	r.Register(inferencedeployment.Chart, noDrift)
	r.Register(jobdeployment.Chart, noDrift)
	r.Register(kaigpuqueues.Chart, noDrift)
	r.Register(persistentvolume.Chart, noDrift)
	return r
}
