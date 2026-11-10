/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package catalogs describes the Helm chart catalog this repository ships, as Go types.
//
// Each chart under catalogs/cluster_base/<component> has a Go package alongside it
// declaring that chart's values schema and a New function that builds a
// v1alpha1.ComponentEntry from it, so that a caller assembling a CCLComponentSet gets
// compile-time checking of the values it sets. There is no catalog-side mirror of the
// API types: New returns the API type itself.
//
// # Nothing here is enforced at admission time
//
// The CCLComponentSet CRD stores values as an untyped JSON blob with
// x-kubernetes-preserve-unknown-fields. A CCLComponentSet written by hand bypasses these
// types entirely and is forwarded to Helm verbatim, placeholders and all. These packages
// help the callers that go through them; they are not a guarantee about what is in the
// cluster.
//
// # Conventions for adding a chart
//
// A component package exports a Chart constant, a Values struct and a New function. It
// does not name itself: the component's name within a CCLComponentSet is the caller's, and
// so is the namespace it installs into.
//
// A catalogs/cluster_base chart package may also export a Verify function, which checks
// from live objects that the component is installed and configured for the cluster it
// runs in. Its signature and contract are verifierregistry.VerifyFunc's; the infra-plane
// operator registers it in its verifier registry (internal/registry/verifier_registry).
// This package does not import operator code: the dependency runs from the operator to
// the catalog only.
//
// A chart package may also export a Drift function returning []DriftSpec: fields of its
// objects that a controller on the infra-plane cluster owns by design. The control-plane
// operator registers it in its drift registry (internal/registry/drift_registry) and
// writes the rules into the chart's Argo CD Application.
//
// Every chart is also registered in the validator registry
// (internal/registry/validator_registry), which decodes a component's stored values into
// the chart's Values type and runs its Validator. It can apply these types to values
// however they were written, not only to those built through Entry; until the
// control-plane operator calls it, hand-written CCLComponentSets still bypass them.
//
// A chart package may also export a Health function, which judges the component's
// health from an Observation: Argo CD's aggregate health and the Application's resource
// tree, with the few live objects it asks for. A chart without one is judged by
// DefaultHealth. Its signature and contract are healthregistry.HealthFunc's; the
// control-plane operator registers it in its health registry
// (internal/registry/health_registry).
//
// Optional scalars must be pointers (*bool, *int32, *string) with omitempty. A plain
// bool with omitempty cannot express "explicitly false", because encoding/json drops
// the zero value — and several charts here need to set a flag to false against a chart
// default that may later flip to true. Required fields are non-pointer and carry no
// omitempty, so an unset one reaches Helm and fails loudly rather than silently
// inheriting a placeholder.
//
// Package names drop the directory's underscores: catalogs/cluster_base/gpu_operator
// is package gpuoperator. The directory name is load-bearing — it is the chart path
// Argo CD resolves — so it stays snake_case, and imports are written explicitly named.
package catalogs

import (
	"encoding/json"
	"fmt"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
)

// Validator is implemented by a chart's Values when the chart cannot be installed
// without some of them. Entry calls Validate on any values that implement it, which is
// the only point at which a missing required value is caught — the API server does not
// check them, and Helm only fails once the chart is already being installed.
type Validator interface {
	Validate() error
}

// DriftSpec names fields of a chart's objects that a controller on the infra-plane cluster
// owns by design, so the rendered value is never the live one. The control-plane
// operator writes each DriftSpec into the chart's Argo CD Application as an ignoreDifferences
// rule, which Argo CD applies to every object of the kind that the Application manages.
//
// A rule does not name objects: chart values can change their names. JQPaths narrows
// the rule to the right fields with a jq select on something the chart keeps stable
// whatever its values, such as a webhook's entry name.
type DriftSpec struct {
	// Group is the objects' API group; empty for the core group.
	Group string

	// Kind is the objects' kind.
	Kind string

	// JQPaths are jq paths to the ignored fields, evaluated against each object.
	JQPaths []string
}

// Entry builds the CRD's chart entry from a chart's typed values. It is what every
// component package's New delegates to.
//
// Values implementing Validator are validated first. Values that marshal to null or an
// empty object are dropped rather than stored: the builder omits the Application's helm
// block entirely when Values is nil or its Raw is empty (see applicationSpec in
// internal/controller/cp/cclcomponentset_builder.go), and storing an empty object would
// defeat that and hand Helm a meaningless override.
func Entry(
	chart, namespace string,
	values any,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	if validator, ok := values.(Validator); ok {
		if err := validator.Validate(); err != nil {
			return cclusternvidiacomv1alpha1.ComponentEntry{}, fmt.Errorf(
				"chart %q: %w", chart, err)
		}
	}

	out := cclusternvidiacomv1alpha1.ComponentEntry{Chart: chart, Namespace: namespace}

	raw, err := json.Marshal(values)
	if err != nil {
		return cclusternvidiacomv1alpha1.ComponentEntry{}, fmt.Errorf(
			"marshalling values for chart %q: %w", chart, err)
	}
	if s := string(raw); s != "null" && s != "{}" {
		out.Values = &apiextensionsv1.JSON{Raw: raw}
	}

	return out, nil
}

// Component places an entry, built by a chart package's New, in a CCLComponentSet
// under the given name and onto the given infra plane. Neither is a fact about the
// chart, so New leaves them to the caller.
func Component(
	name, infraPlane string,
	entry cclusternvidiacomv1alpha1.ComponentEntry,
) cclusternvidiacomv1alpha1.ComponentSpec {
	return cclusternvidiacomv1alpha1.ComponentSpec{
		Name:           name,
		InfraPlaneRef:  cclusternvidiacomv1alpha1.LocalObjectReference{Name: infraPlane},
		ComponentEntry: entry,
	}
}
