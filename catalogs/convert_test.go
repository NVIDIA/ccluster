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

package catalogs_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/NVIDIA/ccluster/catalogs"
)

// stubValues mirrors the shape every per-chart Values type has: one key named for the
// chart, holding the upstream chart's own values.
type stubValues struct {
	Chart *stubChartValues `json:"stub-chart,omitempty"`
}

type stubChartValues struct {
	// Enabled is a pointer for the reason every optional scalar in these packages
	// is: encoding/json drops a plain false under omitempty.
	Enabled  *bool  `json:"enabled,omitempty"`
	Replicas *int32 `json:"replicas,omitempty"`
}

type stubComponent struct {
	entry  catalogs.ComponentEntry
	values any
	err    error
}

func (c stubComponent) Entry() catalogs.ComponentEntry { return c.entry }
func (c stubComponent) ValuesObject() any              { return c.values }

// validatingComponent implements catalogs.Validator, like teleport-kube-agent does.
type validatingComponent struct {
	stubComponent
}

func (c validatingComponent) Validate() error { return c.err }

func entry() catalogs.ComponentEntry {
	return catalogs.ComponentEntry{
		Name:      "stub-chart",
		Chart:     "catalogs/cluster_base/stub_chart",
		Namespace: "stub",
	}
}

func TestToComponentEntry(t *testing.T) {
	enabled := true
	disabled := false
	replicas := int32(3)

	tests := []struct {
		name   string
		values any
		want   map[string]any // nil means "expect no values at all"
	}{
		{
			name:   "nil values are dropped",
			values: nil,
		},
		{
			// The builder omits the Application's helm block when Values is
			// nil or empty, so storing {} would defeat that and hand Helm a
			// meaningless override.
			name:   "values marshalling to an empty object are dropped",
			values: stubValues{},
		},
		{
			name:   "set values are marshalled under the chart key",
			values: stubValues{Chart: &stubChartValues{Replicas: &replicas}},
			want: map[string]any{
				"stub-chart": map[string]any{"replicas": float64(3)},
			},
		},
		{
			name:   "an explicit true survives",
			values: stubValues{Chart: &stubChartValues{Enabled: &enabled}},
			want: map[string]any{
				"stub-chart": map[string]any{"enabled": true},
			},
		},
		{
			// The whole reason optional scalars are pointers. With a plain
			// bool this case would be indistinguishable from unset, and the
			// chart's own default would silently win.
			name:   "an explicit false survives",
			values: stubValues{Chart: &stubChartValues{Enabled: &disabled}},
			want: map[string]any{
				"stub-chart": map[string]any{"enabled": false},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := catalogs.ToComponentEntry(stubComponent{
				entry:  entry(),
				values: test.values,
			})
			if err != nil {
				t.Fatalf("ToComponentEntry() error = %v, want nil", err)
			}

			if got.Chart != entry().Chart {
				t.Errorf("Chart = %q, want %q", got.Chart, entry().Chart)
			}
			if got.Namespace != entry().Namespace {
				t.Errorf("Namespace = %q, want %q", got.Namespace, entry().Namespace)
			}

			if test.want == nil {
				if got.Values != nil {
					t.Errorf("Values = %s, want nil", got.Values.Raw)
				}
				return
			}

			if got.Values == nil {
				t.Fatalf("Values = nil, want %v", test.want)
			}
			var decoded map[string]any
			if err := json.Unmarshal(got.Values.Raw, &decoded); err != nil {
				t.Fatalf("unmarshalling %s: %v", got.Values.Raw, err)
			}
			if !reflect.DeepEqual(decoded, test.want) {
				t.Errorf("Values = %v, want %v", decoded, test.want)
			}
		})
	}
}

func TestToComponentSpec(t *testing.T) {
	spec, err := catalogs.ToComponentSpec(stubComponent{entry: entry()}, "plane-a")
	if err != nil {
		t.Fatalf("ToComponentSpec() error = %v, want nil", err)
	}
	if spec.Name != "stub-chart" {
		t.Errorf("Name = %q, want %q", spec.Name, "stub-chart")
	}
	if spec.InfraPlaneRef.Name != "plane-a" {
		t.Errorf("InfraPlaneRef.Name = %q, want %q", spec.InfraPlaneRef.Name, "plane-a")
	}
	if spec.Chart != entry().Chart {
		t.Errorf("Chart = %q, want %q", spec.Chart, entry().Chart)
	}
}

// TestToComponentSpecRejectsInvalid covers the only point at which a chart's required
// values are enforced: nothing rejects them at admission.
func TestToComponentSpecRejectsInvalid(t *testing.T) {
	sentinel := errors.New("proxyAddr not set")
	component := validatingComponent{stubComponent{entry: entry(), err: sentinel}}

	_, err := catalogs.ToComponentSpec(component, "plane-a")
	if !errors.Is(err, sentinel) {
		t.Fatalf("ToComponentSpec() error = %v, want one wrapping %v", err, sentinel)
	}
}

func TestToComponentSpecsPreservesOrder(t *testing.T) {
	names := []string{"first", "second", "third"}

	components := make([]catalogs.Component, 0, len(names))
	for _, name := range names {
		components = append(components, stubComponent{
			entry: catalogs.ComponentEntry{
				Name:      name,
				Chart:     "catalogs/cluster_base/" + name,
				Namespace: name,
			},
		})
	}

	specs, err := catalogs.ToComponentSpecs(components, "plane-a")
	if err != nil {
		t.Fatalf("ToComponentSpecs() error = %v, want nil", err)
	}
	if len(specs) != len(names) {
		t.Fatalf("len(specs) = %d, want %d", len(specs), len(names))
	}
	for i, name := range names {
		if specs[i].Name != name {
			t.Errorf("specs[%d].Name = %q, want %q", i, specs[i].Name, name)
		}
	}
}
