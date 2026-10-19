/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

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

// IgnoreDifferencesFunc is the signature of a chart's Argo CD ignoreDifferences rules.
// The rules themselves are a plain function in the chart's package, conventionally named
// IgnoreDifferences; the chart package does not import this one, and Register checks the
// signature at compile time.
//
// The rules name fields of the chart's objects that a controller on the infra-plane
// cluster owns by design, so the rendered value is never the live one and a full diff
// would keep the Application OutOfSync, stalling every later component. Each call
// returns a new slice: the operator writes it into an Application spec.
type IgnoreDifferencesFunc func() []argocdv1alpha1.ResourceIgnoreDifferences

// noIgnoreDifferences is registered for a chart whose objects are diffed in full.
func noIgnoreDifferences() []argocdv1alpha1.ResourceIgnoreDifferences {
	return nil
}

// IgnoreDifferencesRegistry maps a chart to its ignoreDifferences rules. The zero value
// has no rules for any chart.
type IgnoreDifferencesRegistry struct {
	registry.Registry[IgnoreDifferencesFunc]
}

// CatalogIgnoreDifferences returns the registry of every catalog chart's
// ignoreDifferences rules. TestCatalogIgnoreDifferences fails when it and components.All
// disagree. A chart diffed in full registers noIgnoreDifferences; one with
// rules registers its package's IgnoreDifferences.
func CatalogIgnoreDifferences() IgnoreDifferencesRegistry {
	var r IgnoreDifferencesRegistry
	r.Register(namespacebase.Chart, noIgnoreDifferences)
	r.Register(argorollouts.Chart, noIgnoreDifferences)
	r.Register(certmanager.Chart, noIgnoreDifferences)
	r.Register(externaldns.Chart, noIgnoreDifferences)
	r.Register(finalstack.Chart, noIgnoreDifferences)
	r.Register(fluentbit.Chart, noIgnoreDifferences)
	r.Register(gpuoperator.Chart, noIgnoreDifferences)
	r.Register(ingressnginx.Chart, noIgnoreDifferences)
	r.Register(istio.Chart, istio.IgnoreDifferences)
	r.Register(keda.Chart, noIgnoreDifferences)
	r.Register(kubeprometheusstack.Chart, noIgnoreDifferences)
	r.Register(longhorn.Chart, noIgnoreDifferences)
	r.Register(metricsserver.Chart, noIgnoreDifferences)
	r.Register(networkoperator.Chart, noIgnoreDifferences)
	r.Register(otelcollector.Chart, noIgnoreDifferences)
	r.Register(prometheusadapter.Chart, noIgnoreDifferences)
	r.Register(spire.Chart, noIgnoreDifferences)
	r.Register(spirecrds.Chart, noIgnoreDifferences)
	r.Register(computedeployment.Chart, noIgnoreDifferences)
	r.Register(dynamodeployment.Chart, noIgnoreDifferences)
	r.Register(inferencedeployment.Chart, noIgnoreDifferences)
	r.Register(jobdeployment.Chart, noIgnoreDifferences)
	r.Register(kaigpuqueues.Chart, noIgnoreDifferences)
	r.Register(persistentvolume.Chart, noIgnoreDifferences)
	return r
}
