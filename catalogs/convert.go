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

package catalogs

import (
	"encoding/json"
	"fmt"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
)

// ToComponentEntry converts a catalog component into the CRD's inline chart entry,
// marshalling its typed values into the untyped JSON the API stores.
//
// Values that marshal to null or an empty object are dropped rather than stored. The
// builder omits the Application's helm block entirely when Values is nil or its Raw is
// empty (see applicationSpec in internal/controller/cp/ccldeployment_builder.go), and
// storing an empty object would defeat that and hand Helm a meaningless override.
func ToComponentEntry(component Component) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	entry := component.Entry()
	out := cclusternvidiacomv1alpha1.ComponentEntry{
		Chart:     entry.Chart,
		Namespace: entry.Namespace,
	}

	values := component.ValuesObject()
	if values == nil {
		return out, nil
	}

	raw, err := json.Marshal(values)
	if err != nil {
		return cclusternvidiacomv1alpha1.ComponentEntry{}, fmt.Errorf(
			"marshalling values for component %q: %w", entry.Name, err)
	}
	if s := string(raw); s == "null" || s == "{}" {
		return out, nil
	}

	out.Values = &apiextensionsv1.JSON{Raw: raw}
	return out, nil
}

// ToComponentSpec converts a catalog component into a CCLDeployment component targeting
// the named CCLInfraPlane, which must be in the same namespace as the CCLDeployment.
//
// A component implementing Validator is validated first: this is where a chart's
// required values are caught, because nothing rejects them at admission.
func ToComponentSpec(
	component Component,
	infraPlane string,
) (cclusternvidiacomv1alpha1.ComponentSpec, error) {
	entry := component.Entry()

	if validator, ok := component.(Validator); ok {
		if err := validator.Validate(); err != nil {
			return cclusternvidiacomv1alpha1.ComponentSpec{}, fmt.Errorf(
				"component %q: %w", entry.Name, err)
		}
	}

	componentEntry, err := ToComponentEntry(component)
	if err != nil {
		return cclusternvidiacomv1alpha1.ComponentSpec{}, err
	}

	return cclusternvidiacomv1alpha1.ComponentSpec{
		Name:           entry.Name,
		InfraPlaneRef:  cclusternvidiacomv1alpha1.LocalObjectReference{Name: infraPlane},
		ComponentEntry: componentEntry,
	}, nil
}

// ToComponentSpecs converts an ordered catalog - clusterbase.Components(), for instance -
// into the spec.components of a CCLDeployment, preserving order. The order is the rollout
// order: the operator releases components one at a time, in the order given.
func ToComponentSpecs(
	components []Component,
	infraPlane string,
) ([]cclusternvidiacomv1alpha1.ComponentSpec, error) {
	specs := make([]cclusternvidiacomv1alpha1.ComponentSpec, 0, len(components))
	for _, component := range components {
		spec, err := ToComponentSpec(component, infraPlane)
		if err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, nil
}
