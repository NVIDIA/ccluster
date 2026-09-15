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

// Package components is the registry of every chart in the catalog: one entry per chart
// directory, whether catalogs/<catalog>/<chart> or catalogs/<chart>, naming the chart and
// the zero value of its Values type. Anything that needs to enumerate the catalog - the Python generator in
// hack/pyschema, tests - reads All rather than keeping a list of its own.
//
// Adding a chart is three steps: create its directory with the Helm chart and a
// component.go declaring Chart, Values and New, add one line to All below, and run make
// python-generate. TestAllCoversTheTree fails when the directory and the list disagree,
// and the chart's path under catalogs/ becomes its path under ccluster.catalogs in Python
// without further registration.
//
// This is a separate package from catalogs because the chart packages import catalogs
// for Entry, and this list imports every chart package.
package components

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
	teleportkubeagent "github.com/NVIDIA/ccluster/catalogs/cluster_base/teleport_kube_agent"
	computedeployment "github.com/NVIDIA/ccluster/catalogs/deployments/compute_deployment"
	inferencedeployment "github.com/NVIDIA/ccluster/catalogs/deployments/inference_deployment"
	jobdeployment "github.com/NVIDIA/ccluster/catalogs/deployments/job_deployment"
	namespacebase "github.com/NVIDIA/ccluster/catalogs/namespace_base"
)

// Component is one chart: its directory relative to the repository root, which is also
// the path Argo CD resolves and the chart's Python package, and the zero value of its
// Values type, from which its schema is reflected.
type Component struct {
	Chart  string
	Values any
}

// All is every chart in the catalog, grouped by directory and sorted by chart within it.
var All = []Component{
	// The per-tenant namespace baseline the deployments charts assume is in place.
	{namespacebase.Chart, namespacebase.Values{}},

	// cluster_base: the platform components every infra-plane cluster gets.
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
	{teleportkubeagent.Chart, teleportkubeagent.Values{}},

	// deployments: the tenant workload charts.
	{computedeployment.Chart, computedeployment.Values{}},
	{inferencedeployment.Chart, inferencedeployment.Values{}},
	{jobdeployment.Chart, jobdeployment.Values{}},
}
