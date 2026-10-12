/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package ip

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
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

// VerifyFunc is the signature of a cluster_base chart's check. The check itself is a
// plain function in the chart's package, conventionally named Verify; the chart package
// does not import this one, and Register checks the signature at compile time.
//
// A check reads, from live objects in the cluster the chart is installed in, whether its
// component is installed and configured for that cluster. It never consults Argo CD or
// Helm release state, so it holds whichever of the two installed the chart: the control
// plane through a CCLComponentSet, or a user with helm. ctx is the reconcile context
// every controller-runtime read needs; r is a cached, read-only client; cfg is the
// cluster's declared configuration, for checks such as "spire's trust domain is this
// cluster's domain".
//
// A check returns nil when the component is installed and configured, and otherwise an
// error saying why not. The error text is shown to users in the CCLClusterConfig's
// Verified condition next to the chart, so it should be short and specific, e.g.
// `trust domain is "a.example.com", want "b.example.com"`. A read that fails is
// returned as it is: a component the operator cannot check is not verified.
//
// The controller runs all checks concurrently, so one must not rely on another having
// run, and must be safe to call from its own goroutine; r is safe for concurrent reads.
// Each call's ctx carries a deadline: a check must return an error once ctx is done. A
// panic is recovered and reported as an error, but is a bug in the check.
type VerifyFunc func(ctx context.Context, r client.Reader, cfg cclusternvidiacomv1alpha1.CCLClusterConfigSpec) error

// alwaysVerified is registered for a chart without a check of its own yet: it reports
// the component as verified without looking.
func alwaysVerified(context.Context, client.Reader, cclusternvidiacomv1alpha1.CCLClusterConfigSpec) error {
	return nil
}

// Verifier is one registered check: the cluster_base chart it verifies, which is how
// the outcome is reported, and the chart package's check.
type Verifier struct {
	Chart  string
	Verify VerifyFunc
}

// Registry is the set of Verifiers the CCLClusterConfig controller runs, in registration
// order, which is the order failing charts are reported in. Each chart is registered at
// most once, so a chart identifies its result. The zero value is an empty registry.
//
// Registration happens while the operator is being wired up, before the manager starts;
// a Registry is not safe for Register calls concurrent with a running controller.
type Registry struct {
	verifiers []Verifier
}

// Register adds verify as the check for chart. It panics on an empty chart, a nil
// verify, or a chart registered twice: each is a wiring bug, caught at startup rather
// than reported as a verification result.
func (r *Registry) Register(chart string, verify VerifyFunc) {
	if chart == "" {
		panic("ip: Register with an empty chart")
	}
	if verify == nil {
		panic(fmt.Sprintf("ip: Register %s with a nil VerifyFunc", chart))
	}
	for _, v := range r.verifiers {
		if v.Chart == chart {
			panic(fmt.Sprintf("ip: %s registered twice", chart))
		}
	}
	r.verifiers = append(r.verifiers, Verifier{Chart: chart, Verify: verify})
}

// Verifiers returns the registered Verifiers in registration order. The slice is the
// caller's own.
func (r *Registry) Verifiers() []Verifier {
	return append([]Verifier(nil), r.verifiers...)
}

// ClusterBaseVerifiers returns the registry of every catalogs/cluster_base chart's
// check, sorted by chart as components.ClusterBase is. TestClusterBaseVerifiers fails
// when the two disagree. A chart without a check of its own registers alwaysVerified;
// one with a check registers its package's Verify. A check that reads a kind needs a
// +kubebuilder:rbac marker in this package.
func ClusterBaseVerifiers() *Registry {
	r := &Registry{}
	r.Register(argorollouts.Chart, alwaysVerified)
	r.Register(certmanager.Chart, alwaysVerified)
	r.Register(externaldns.Chart, alwaysVerified)
	r.Register(finalstack.Chart, alwaysVerified)
	r.Register(fluentbit.Chart, alwaysVerified)
	r.Register(gpuoperator.Chart, alwaysVerified)
	r.Register(ingressnginx.Chart, alwaysVerified)
	r.Register(istio.Chart, alwaysVerified)
	r.Register(keda.Chart, alwaysVerified)
	r.Register(kubeprometheusstack.Chart, alwaysVerified)
	r.Register(longhorn.Chart, alwaysVerified)
	r.Register(metricsserver.Chart, alwaysVerified)
	r.Register(networkoperator.Chart, alwaysVerified)
	r.Register(otelcollector.Chart, alwaysVerified)
	r.Register(prometheusadapter.Chart, alwaysVerified)
	r.Register(spire.Chart, alwaysVerified)
	r.Register(spirecrds.Chart, alwaysVerified)
	return r
}
