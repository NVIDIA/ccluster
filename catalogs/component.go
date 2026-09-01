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
// declaring that chart's values schema, so that a caller building a CCLDeployment gets
// compile-time checking of the values it sets and a place to record which of them the
// chart cannot be installed without.
//
// # Nothing here is enforced at admission time
//
// The CCLDeployment CRD stores values as an untyped JSON blob with
// x-kubernetes-preserve-unknown-fields, and this change does not alter that. A
// CCLDeployment written by hand bypasses these types entirely and is forwarded to Helm
// verbatim, placeholders and all. These packages help the callers that go through them;
// they are not a guarantee about what is in the cluster.
//
// # Conventions for adding a chart
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

// ComponentEntry is the chart metadata every catalog component carries. It is the Go
// analogue of the ClassVars on the legacy Python catalog's ComponentEntry: facts about
// the chart rather than values a user supplies.
type ComponentEntry struct {
	// Name is the component's name within a CCLDeployment. It is the chart's
	// kebab-case name, which is also the key its values nest under, and it must be
	// DNS-1123 because api/v1alpha1.ComponentSpec.Name requires it.
	Name string

	// Chart is the chart directory relative to the repository root, for example
	// "catalogs/cluster_base/gpu_operator". Note the snake_case: this is the
	// directory name, not Name.
	Chart string

	// Namespace is the destination namespace on the target cluster.
	Namespace string
}

// Component is one entry of a catalog: chart metadata, plus that chart's typed values.
type Component interface {
	// Entry returns the component's chart metadata.
	Entry() ComponentEntry

	// ValuesObject returns the chart's typed values struct. It is named for Argo CD's
	// helm.valuesObject, which is where the marshalled result ends up, and to leave
	// the field name Values free on the implementing types.
	ValuesObject() any
}

// Validator is implemented by components that have values the chart cannot be installed
// without. ToComponentSpecs calls Validate on any component that implements it, which is
// the only point at which a missing required value is caught — the API server does not
// check them.
type Validator interface {
	Validate() error
}
