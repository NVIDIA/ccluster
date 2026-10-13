/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

import (
	"encoding/json"
	"fmt"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

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

// ValidateFunc checks one component's Helm values against its chart, before the operator
// writes anything for it. values is the component's spec.components[].values exactly as
// stored, and may be nil.
//
// A check is a pure function of values: it reads nothing else, so its answer is fixed
// for a given CCLComponentSet generation. It returns nil when the values are acceptable,
// and otherwise an error saying why not. The error text is shown to users in the
// CCLComponentSet's Validated condition next to the component, so it should be short and
// specific.
type ValidateFunc func(values *apiextensionsv1.JSON) error

// Values returns the ValidateFunc for a chart whose values are described by V, its
// catalog package's Values type. The values are decoded into V and, when V implements
// catalogs.Validator, validated by it - the same check catalogs.Entry makes for Go
// callers, made here for every CCLComponentSet however it was written.
//
// Decoding is lenient: keys V does not model pass, because a chart accepts more than its
// Go type describes. A modelled key holding the wrong JSON type fails. Absent values
// decode as the zero V, which is what a chart without required values accepts.
func Values[V any]() ValidateFunc {
	return func(values *apiextensionsv1.JSON) error {
		var v V
		if values != nil && len(values.Raw) > 0 {
			if err := json.Unmarshal(values.Raw, &v); err != nil {
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

// ValidatorRegistry is the set of value checks the CCLComponentSet controller runs, one
// per catalog chart. A chart that is not registered is not a catalog chart, and a
// component naming it fails validation. The zero value is an empty registry.
//
// Registration happens while the operator is being wired up, before the manager starts;
// a ValidatorRegistry is not safe for Register calls concurrent with a running controller.
type ValidatorRegistry struct {
	charts     []string
	validators map[string]ValidateFunc
}

// Register adds validate as the check for chart. It panics on an empty chart, a nil
// validate, or a chart registered twice: each is a wiring bug, caught at startup rather
// than reported as a validation result.
func (r *ValidatorRegistry) Register(chart string, validate ValidateFunc) {
	if chart == "" {
		panic("cp: Register with an empty chart")
	}
	if validate == nil {
		panic(fmt.Sprintf("cp: Register %s with a nil ValidateFunc", chart))
	}
	if _, dup := r.validators[chart]; dup {
		panic(fmt.Sprintf("cp: %s registered twice", chart))
	}
	if r.validators == nil {
		r.validators = map[string]ValidateFunc{}
	}
	r.charts = append(r.charts, chart)
	r.validators[chart] = validate
}

// Lookup returns the check registered for chart, and whether there is one.
func (r *ValidatorRegistry) Lookup(chart string) (ValidateFunc, bool) {
	validate, ok := r.validators[chart]
	return validate, ok
}

// Charts returns the registered charts in registration order. The slice is the
// caller's own.
func (r *ValidatorRegistry) Charts() []string {
	return append([]string(nil), r.charts...)
}

// CatalogValidators returns the registry of every catalog chart's value check, in
// components.All order. TestCatalogValidators fails when the two disagree, so a chart
// added to the catalog is not deployable until it is registered here. A chart whose
// check its Values type cannot express registers a ValidateFunc of its own instead of
// the Values adapter.
func CatalogValidators() *ValidatorRegistry {
	r := &ValidatorRegistry{}
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
