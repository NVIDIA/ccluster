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
// # Enforced at reconcile time, not admission time
//
// The CCLComponentSet CRD stores values as an untyped JSON blob with
// x-kubernetes-preserve-unknown-fields, so the API server accepts whatever is written.
// The control-plane operator checks every component against these types before it
// deploys anything: it decodes the values into the chart's Values type and runs its
// Validator, and a CCLComponentSet that fails is reported Validated=False rather than
// forwarded to Helm. Entry runs the same Validator for Go callers, earlier. Keys a
// Values type does not model pass both checks.
//
// # Conventions for adding a chart
//
// A component package exports a Chart constant, a Values struct and a New function. It
// does not name itself: the component's name within a CCLComponentSet is the caller's, and
// so is the namespace it installs into. Every chart is registered with the control-plane
// operator's validator registry (internal/controller/cp), which is how its Values type
// and Validator are applied to CCLComponentSets however they were written.
//
// A catalogs/cluster_base chart package may also export a Verify function, which checks
// from live objects that the component is installed and configured for the cluster it
// runs in. Its signature and contract are ip.VerifyFunc's; the infra-plane operator
// registers it in its verifier registry (internal/controller/ip). This package does not
// import controller code: the dependency runs from the operator to the catalog only.
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

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
)

// Validator is implemented by a chart's Values when the chart cannot be installed
// without some of them. Entry calls Validate on any values that implement it, and the
// control-plane operator calls it on every CCLComponentSet component before deploying
// it — the API server does not check them, and Helm only fails once the chart is
// already being installed.
type Validator interface {
	Validate() error
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
