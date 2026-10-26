/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package clusterbase assembles the cluster_base catalog: the platform components every
// infra-plane cluster gets, in the order they have to be installed.
package clusterbase

import (
	"errors"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
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
)

func Components(
	infraPlane string,
) ([]cclusternvidiacomv1alpha1.ComponentSpec, error) {
	ref := cclusternvidiacomv1alpha1.LocalObjectReference{Name: infraPlane}

	// entry adapts a component package's New to a field of the literal below, so that
	// the catalog reads as the list it is rather than as seventeen error checks. None
	// of these components has required values, so every error is nil today; the error
	// return stays so that adding one which does is not a signature change, and
	// catalogs.Entry names the chart an error came from.
	var errs []error
	entry := func(
		e cclusternvidiacomv1alpha1.ComponentEntry,
		err error,
	) cclusternvidiacomv1alpha1.ComponentEntry {
		errs = append(errs, err)
		return e
	}

	components := []cclusternvidiacomv1alpha1.ComponentSpec{
		{
			Name:           "spire-crds",
			InfraPlaneRef:  ref,
			ComponentEntry: entry(spirecrds.New("spire-system", spirecrds.Values{})),
		}, // wave -70
		{
			Name:           "spire",
			InfraPlaneRef:  ref,
			ComponentEntry: entry(spire.New("spire-system", spire.Values{})),
		}, // wave -65
		{
			Name:           "kube-prometheus-stack",
			InfraPlaneRef:  ref,
			ComponentEntry: entry(kubeprometheusstack.New("prometheus", kubeprometheusstack.Values{})),
		}, // wave -60
		{
			Name:           "istio",
			InfraPlaneRef:  ref,
			ComponentEntry: entry(istio.New("istio-system", istio.Values{})),
		}, // wave -50
		{
			Name:           "cert-manager",
			InfraPlaneRef:  ref,
			ComponentEntry: entry(certmanager.New("cert-manager", certmanager.Values{})),
		}, // wave -45
		{
			Name:           "ingress-nginx",
			InfraPlaneRef:  ref,
			ComponentEntry: entry(ingressnginx.New("ingress-nginx", ingressnginx.Values{})),
		}, // wave -40
		{
			Name:           "external-dns",
			InfraPlaneRef:  ref,
			ComponentEntry: entry(externaldns.New("external-dns", externaldns.Values{})),
		}, // wave  10
		{
			Name:           "final-stack",
			InfraPlaneRef:  ref,
			ComponentEntry: entry(finalstack.New("final-stack", finalstack.Values{})),
		}, // wave  20
		{
			Name:           "prometheus-adapter",
			InfraPlaneRef:  ref,
			ComponentEntry: entry(prometheusadapter.New("prometheus", prometheusadapter.Values{})),
		}, // wave  30
		{
			Name:           "keda",
			InfraPlaneRef:  ref,
			ComponentEntry: entry(keda.New("keda", keda.Values{})),
		}, // wave  30
		{
			Name:           "fluent-bit",
			InfraPlaneRef:  ref,
			ComponentEntry: entry(fluentbit.New("fluent-bit", fluentbit.Values{})),
		}, // wave  30
		{
			Name:           "argo-rollouts",
			InfraPlaneRef:  ref,
			ComponentEntry: entry(argorollouts.New("argo-rollouts", argorollouts.Values{})),
		}, // wave  30
		{
			Name:           "network-operator",
			InfraPlaneRef:  ref,
			ComponentEntry: entry(networkoperator.New("network-operator", networkoperator.Values{})),
		}, // wave  40
		{
			Name:           "opentelemetry-collector",
			InfraPlaneRef:  ref,
			ComponentEntry: entry(otelcollector.New("opentelemetry", otelcollector.Values{})),
		}, // wave  40
		{
			Name:           "gpu-operator",
			InfraPlaneRef:  ref,
			ComponentEntry: entry(gpuoperator.New("gpu-operator", gpuoperator.Values{})),
		}, // wave  45
		{
			Name:           "longhorn",
			InfraPlaneRef:  ref,
			ComponentEntry: entry(longhorn.New("longhorn-system", longhorn.Values{})),
		}, // wave  46
		{
			Name:           "metrics-server",
			InfraPlaneRef:  ref,
			ComponentEntry: entry(metricsserver.New("kube-system", metricsserver.Values{})),
		}, // wave  47
	}

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return components, nil
}
