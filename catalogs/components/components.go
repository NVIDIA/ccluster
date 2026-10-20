/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package components is the registry of every chart in the catalog: one entry per chart
// directory, whether catalogs/<catalog>/<chart> or catalogs/<chart>, naming the chart and
// the zero value of its Values type. Anything that needs to enumerate the catalog - the Python generator in
// hack/pyschema, tests - reads All rather than keeping a list of its own.
//
// Adding a chart is three steps: create its directory with the Helm chart and a
// component.go declaring Chart, Values and New, add one line to its directory's group
// below, and run make python-generate. A cluster_base chart is also registered with the
// infra-plane operator's verifier registry in internal/registry/verifier_registry.
// TestAllCoversTheTree fails when the directory and the list disagree, and the chart's
// path under catalogs/ becomes its path under ccluster.catalogs in Python without
// further registration.
//
// This is a separate package from catalogs because the chart packages import catalogs
// for Entry, and this list imports every chart package.
package components

import (
	"slices"

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
)

// Component is one chart: its directory relative to the repository root, which is also
// the path Argo CD resolves and the chart's Python package; and the zero value of its
// Values type, from which its schema is reflected.
type Component struct {
	Chart  string
	Values any
}

// NamespaceBase is the per-tenant namespace baseline the deployments charts assume is
// in place.
var NamespaceBase = []Component{
	{namespacebase.Chart, namespacebase.Values{}},
}

// ClusterBase is the platform components every infra-plane cluster gets, sorted by
// chart. The infra-plane operator's verifier registry must cover exactly these.
var ClusterBase = []Component{
	{argorollouts.Chart, argorollouts.Values{}},
	{certmanager.Chart, certmanager.Values{}},
	{externaldns.Chart, externaldns.Values{}},
	{finalstack.Chart, finalstack.Values{}},
	{fluentbit.Chart, fluentbit.Values{}},
	{gpuoperator.Chart, gpuoperator.Values{}},
	{ingressnginx.Chart, ingressnginx.Values{}},
	{istio.Chart, istio.Values{}},
	{keda.Chart, keda.Values{}},
	{kubeprometheusstack.Chart, kubeprometheusstack.Values{}},
	{longhorn.Chart, longhorn.Values{}},
	{metricsserver.Chart, metricsserver.Values{}},
	{networkoperator.Chart, networkoperator.Values{}},
	{otelcollector.Chart, otelcollector.Values{}},
	{prometheusadapter.Chart, prometheusadapter.Values{}},
	{spire.Chart, spire.Values{}},
	{spirecrds.Chart, spirecrds.Values{}},
}

// Deployments is the tenant workload charts, sorted by chart.
var Deployments = []Component{
	{computedeployment.Chart, computedeployment.Values{}},
	{dynamodeployment.Chart, dynamodeployment.Values{}},
	{inferencedeployment.Chart, inferencedeployment.Values{}},
	{jobdeployment.Chart, jobdeployment.Values{}},
}

// Resources is the cluster and tenant resources the workloads use, sorted by chart.
var Resources = []Component{
	{kaigpuqueues.Chart, kaigpuqueues.Values{}},
	{persistentvolume.Chart, persistentvolume.Values{}},
}

// All is every chart in the catalog: the groups above, concatenated in that order. The
// groups are disjoint, each holding the charts of one catalogs/ directory.
var All = slices.Concat(NamespaceBase, ClusterBase, Deployments, Resources)
