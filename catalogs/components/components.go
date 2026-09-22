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
// component.go declaring Chart, Values and New, add one line to All below (with
// catalogs.DefaultVerifier under cluster_base), and run make python-generate.
// TestAllCoversTheTree fails when the directory and the list disagree, and the chart's
// path under catalogs/ becomes its path under ccluster.catalogs in Python without
// further registration.
//
// This is a separate package from catalogs because the chart packages import catalogs
// for Entry, and this list imports every chart package.
package components

import (
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
)

// Component is one chart: its directory relative to the repository root, which is also
// the path Argo CD resolves and the chart's Python package; the zero value of its Values
// type, from which its schema is reflected; and, for cluster_base charts, the Verifier
// the infra-plane operator runs to check the component is installed and configured.
// Every cluster_base chart starts with catalogs.DefaultVerifier, which reports true, until
// it has a check of its own. Verifier is nil for the tenant charts, which nothing verifies.
type Component struct {
	Chart    string
	Values   any
	Verifier catalogs.Verifier
}

// All is every chart in the catalog, grouped by directory and sorted by chart within it.
var All = []Component{
	// The per-tenant namespace baseline the deployments charts assume is in place.
	{namespacebase.Chart, namespacebase.Values{}, nil},

	// cluster_base: the platform components every infra-plane cluster gets.
	{argorollouts.Chart, argorollouts.Values{}, catalogs.DefaultVerifier{}},
	{certmanager.Chart, certmanager.Values{}, catalogs.DefaultVerifier{}},
	{externaldns.Chart, externaldns.Values{}, catalogs.DefaultVerifier{}},
	{finalstack.Chart, finalstack.Values{}, catalogs.DefaultVerifier{}},
	{fluentbit.Chart, fluentbit.Values{}, catalogs.DefaultVerifier{}},
	{gpuoperator.Chart, gpuoperator.Values{}, catalogs.DefaultVerifier{}},
	{ingressnginx.Chart, ingressnginx.Values{}, catalogs.DefaultVerifier{}},
	{istio.Chart, istio.Values{}, catalogs.DefaultVerifier{}},
	{keda.Chart, keda.Values{}, catalogs.DefaultVerifier{}},
	{kubeprometheusstack.Chart, kubeprometheusstack.Values{}, catalogs.DefaultVerifier{}},
	{longhorn.Chart, longhorn.Values{}, catalogs.DefaultVerifier{}},
	{metricsserver.Chart, metricsserver.Values{}, catalogs.DefaultVerifier{}},
	{networkoperator.Chart, networkoperator.Values{}, catalogs.DefaultVerifier{}},
	{otelcollector.Chart, otelcollector.Values{}, catalogs.DefaultVerifier{}},
	{prometheusadapter.Chart, prometheusadapter.Values{}, catalogs.DefaultVerifier{}},
	{spire.Chart, spire.Values{}, catalogs.DefaultVerifier{}},
	{spirecrds.Chart, spirecrds.Values{}, catalogs.DefaultVerifier{}},

	// deployments: the tenant workload charts.
	{computedeployment.Chart, computedeployment.Values{}, nil},
	{dynamodeployment.Chart, dynamodeployment.Values{}, nil},
	{inferencedeployment.Chart, inferencedeployment.Values{}, nil},
	{jobdeployment.Chart, jobdeployment.Values{}, nil},

	// resources: cluster and tenant resources the workloads use.
	{kaigpuqueues.Chart, kaigpuqueues.Values{}, nil},
	{persistentvolume.Chart, persistentvolume.Values{}, nil},
}
