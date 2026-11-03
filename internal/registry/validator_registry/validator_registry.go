/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package validatorregistry is the registry of catalog charts' value checks: for each
// chart, a function that says whether a CCLComponentSet component's stored values are
// acceptable to it. It applies a chart's Values type and catalogs.Validator to values
// however they were written - by hand, from YAML or through the Go and Python types -
// so the control-plane operator and pkg/cclclient can reject a component before anything
// is deployed.
package validatorregistry

import (
	"fmt"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/json"

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

// ValidateFunc is the signature of a chart's value check. values is a component's
// spec.components[].values exactly as stored, and may be nil.
//
// A check is a pure function of values: it reads nothing else, so its answer is fixed
// for a given CCLComponentSet generation. It returns nil when the values are acceptable,
// and otherwise an error saying why not. The error is shown to users next to the
// component, so it should be short and specific.
//
// The checks are compiled into the binary, so they describe the catalog it was built
// from; a catalog revision the binary was not built from can disagree with them.
type ValidateFunc func(values *apiextensionsv1.JSON) error

// Values returns the ValidateFunc for a chart whose values are described by V, its
// catalog package's Values type. The values are decoded into V and, when V implements
// catalogs.Validator, validated by it: the same check catalogs.Entry makes for Go
// callers, made here for values however they were written.
//
// Decoding is lenient: keys V does not model pass, because a chart accepts more than its
// Go type describes. A modelled key holding the wrong JSON type fails. Keys match V's
// JSON names case-sensitively, as Helm templates read them: a key differing only in case,
// such as imageURL for imageUrl, is not the modelled key but another unmodelled one, so
// it neither sets the modelled value nor is type-checked against it. Absent values
// decode as the zero V, which is what catalogs.Entry validates when it drops empty
// values, so a chart with required values rejects a component that sets none.
func Values[V any]() ValidateFunc {
	return func(values *apiextensionsv1.JSON) error {
		var v V
		if values != nil && len(values.Raw) > 0 {
			if err := json.UnmarshalCaseSensitivePreserveInts(values.Raw, &v); err != nil {
				return fmt.Errorf("decoding values: %w", err)
			}
		}
		// A pointer covers Validate methods on either receiver.
		if validator, ok := any(&v).(catalogs.Validator); ok {
			return validator.Validate()
		}
		return nil
	}
}

// Registry maps a chart to its value check. A chart that is not registered is not a
// catalog chart. The zero value has no check for any chart.
type Registry struct {
	registry.Registry[ValidateFunc]
}

// Get returns the registry of every catalog chart's value check. TestGet fails when it
// and components.All disagree. A chart registers the Values adapter over its package's
// Values type; one whose check its Values type cannot express registers a ValidateFunc of
// its own.
func Get() *Registry {
	r := &Registry{}
	r.Register(namespacebase.Chart, Values[namespacebase.Values]())
	r.Register(argorollouts.Chart, Values[argorollouts.Values]())
	r.Register(certmanager.Chart, Values[certmanager.Values]())
	r.Register(externaldns.Chart, Values[externaldns.Values]())
	r.Register(finalstack.Chart, Values[finalstack.Values]())
	r.Register(fluentbit.Chart, Values[fluentbit.Values]())
	r.Register(gpuoperator.Chart, Values[gpuoperator.Values]())
	r.Register(ingressnginx.Chart, Values[ingressnginx.Values]())
	r.Register(istio.Chart, Values[istio.Values]())
	r.Register(keda.Chart, Values[keda.Values]())
	r.Register(kubeprometheusstack.Chart, Values[kubeprometheusstack.Values]())
	r.Register(longhorn.Chart, Values[longhorn.Values]())
	r.Register(metricsserver.Chart, Values[metricsserver.Values]())
	r.Register(networkoperator.Chart, Values[networkoperator.Values]())
	r.Register(otelcollector.Chart, Values[otelcollector.Values]())
	r.Register(prometheusadapter.Chart, Values[prometheusadapter.Values]())
	r.Register(spire.Chart, Values[spire.Values]())
	r.Register(spirecrds.Chart, Values[spirecrds.Values]())
	r.Register(computedeployment.Chart, Values[computedeployment.Values]())
	r.Register(dynamodeployment.Chart, Values[dynamodeployment.Values]())
	r.Register(inferencedeployment.Chart, Values[inferencedeployment.Values]())
	r.Register(jobdeployment.Chart, Values[jobdeployment.Values]())
	r.Register(kaigpuqueues.Chart, Values[kaigpuqueues.Values]())
	r.Register(persistentvolume.Chart, Values[persistentvolume.Values]())
	return r
}
