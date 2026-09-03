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
// without some of them. Entry calls Validate on any values that implement it, which is
// the only point at which a missing required value is caught — the API server does not
// check them, and Helm only fails once the chart is already being installed.
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
