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

// Package clusterbase assembles the cluster_base catalog: the platform components
// every infra-plane cluster gets, in the order they have to be installed.
package clusterbase

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
	teleportkubeagent "github.com/NVIDIA/ccluster/catalogs/cluster_base/teleport_kube_agent"
)

// Components returns the cluster_base catalog in rollout order.
//
// The slice order is the contract. The operator has no concept of waves: it releases
// components one at a time in the order spec.components lists them, withholding each
// until the one before it reports Healthy and Synced. This order reproduces the sync
// waves of the Python catalog it was taken from (catalog/cluster_base/__init__.py),
// which are noted per line; where several components shared a wave there, they are
// ordered here as they were declared there. Do not re-sort this alphabetically.
//
// Because everything serialises, installing the whole catalog is eighteen sequential
// convergence waits. Components that could have installed concurrently — the four that
// shared wave 30, and the two that shared wave 40 — do not.
//
// Callers own the returned components and are expected to set values on them before
// converting. teleport-kube-agent in particular is returned with the values the chart
// requires left empty, so catalogs.ToComponentSpecs refuses to convert it until they
// are filled in; see Configure.
func Components() []catalogs.Component {
	return []catalogs.Component{
		spirecrds.New(),           // wave -70
		spire.New(),               // wave -65
		kubeprometheusstack.New(), // wave -60
		istio.New(),               // wave -50
		certmanager.New(),         // wave -45
		ingressnginx.New(),        // wave -40
		externaldns.New(),         // wave  10
		finalstack.New(),          // wave  20
		prometheusadapter.New(),   // wave  30
		keda.New(),                // wave  30
		fluentbit.New(),           // wave  30
		argorollouts.New(),        // wave  30
		networkoperator.New(),     // wave  40
		otelcollector.New(),       // wave  40
		gpuoperator.New(),         // wave  45
		longhorn.New(),            // wave  46
		metricsserver.New(),       // wave  47
		teleportkubeagent.New(teleportkubeagent.Config{}), // wave 50
	}
}

// Config carries the per-cluster values the catalog cannot be installed without.
type Config struct {
	// Teleport configures the teleport-kube-agent component. Its four fields are
	// the only values in the whole catalog that a chart genuinely requires: they
	// ship as the placeholders "<PROXY_ADDR>" and friends, and an agent installed
	// with them cannot connect.
	Teleport teleportkubeagent.Config
}

// Configure returns the catalog in rollout order with config applied, ready to convert
// with catalogs.ToComponentSpecs.
func Configure(config Config) []catalogs.Component {
	components := Components()
	for i, component := range components {
		if _, ok := component.(*teleportkubeagent.Component); ok {
			components[i] = teleportkubeagent.New(config.Teleport)
		}
	}
	return components
}
