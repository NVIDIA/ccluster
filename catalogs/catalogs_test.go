/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package catalogs

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
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

// validatingValues implements catalogs.Validator, the way kai_gpu_queues' and the
// deployments charts' Values do. It is the only hook by which a chart's required values are ever checked.
type validatingValues struct {
	stubValues
	err error
}

func (v validatingValues) Validate() error { return v.err }

const (
	chart     = "catalogs/cluster_base/stub_chart"
	namespace = "stub"
)

func TestEntry(t *testing.T) {
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
			got, err := Entry(chart, namespace, test.values)
			if err != nil {
				t.Fatalf("Entry() error = %v, want nil", err)
			}

			if got.Chart != chart {
				t.Errorf("Chart = %q, want %q", got.Chart, chart)
			}
			if got.Namespace != namespace {
				t.Errorf("Namespace = %q, want %q", got.Namespace, namespace)
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

// TestEntryRejectsInvalidValues covers the only point at which a chart's required values
// are enforced: nothing rejects them at admission, and Helm only fails once the chart is
// already being installed.
func TestEntryRejectsInvalidValues(t *testing.T) {
	sentinel := errors.New("queues[0].name not set")

	_, err := Entry(chart, namespace, validatingValues{err: sentinel})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Entry() error = %v, want one wrapping %v", err, sentinel)
	}
}

// TestEntrySkipsValuesWithoutValidator is the other half: the components that have no
// required values must not be asked for a Validate they do not have.
func TestEntrySkipsValuesWithoutValidator(t *testing.T) {
	if _, err := Entry(chart, namespace, stubValues{}); err != nil {
		t.Fatalf("Entry() error = %v, want nil", err)
	}
}

// TestComponent checks that the caller's name and infra plane reach the component beside
// the entry, unchanged.
func TestComponent(t *testing.T) {
	entry := cclusternvidiacomv1alpha1.ComponentEntry{
		Chart:     chart,
		Namespace: namespace,
		Values:    &apiextensionsv1.JSON{Raw: []byte(`{"stub-chart":{"enabled":true}}`)},
	}
	got := Component("stub", "gpu-a", entry)
	want := cclusternvidiacomv1alpha1.ComponentSpec{
		Name:           "stub",
		InfraPlaneRef:  cclusternvidiacomv1alpha1.LocalObjectReference{Name: "gpu-a"},
		ComponentEntry: entry,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Component() = %+v, want %+v", got, want)
	}
}
