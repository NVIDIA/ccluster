/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package validatorregistry

import (
	"errors"
	"slices"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"github.com/NVIDIA/ccluster/catalogs/cluster_base/keda"
	"github.com/NVIDIA/ccluster/catalogs/components"
	kaigpuqueues "github.com/NVIDIA/ccluster/catalogs/resources/kai_gpu_queues"
)

// TestGet: checks are looked up by the chart a component names, so a catalog chart
// missing from the registry would be rejected as unknown, and a registered chart outside
// components.All would accept components no chart can deploy. The registry keeps
// components.All order, so a missing or extra chart is easy to place.
func TestGet(t *testing.T) {
	want := make([]string, 0, len(components.All))
	for _, c := range components.All {
		want = append(want, c.Chart)
	}
	if got := Get().Charts(); !slices.Equal(got, want) {
		t.Errorf("registered charts %v\nwant components.All %v", got, want)
	}
}

// stubValues stands in for a chart's Values: one modelled key, and a Validate that
// requires it, on a value receiver as every catalog chart's is.
type stubValues struct {
	Name *string `json:"name,omitempty"`
}

var errNameMissing = errors.New("name not set")

func (v stubValues) Validate() error {
	if v.Name == nil {
		return errNameMissing
	}
	return nil
}

// pointerValues has its Validate on a pointer receiver, which the adapter must still find.
type pointerValues struct {
	Name string `json:"name"`
}

func (v *pointerValues) Validate() error {
	if v.Name == "" {
		return errNameMissing
	}
	return nil
}

// plainValues has no Validate: only its decoding is checked.
type plainValues struct {
	Replicas *int32 `json:"replicas,omitempty"`
}

func TestValues(t *testing.T) {
	tests := []struct {
		name     string
		validate ValidateFunc
		values   *apiextensionsv1.JSON
		// wantErr is a substring of the error, or empty for none.
		wantErr string
		// wantIs, when set, must be wrapped by the error.
		wantIs error
	}{
		{name: "valid values", validate: Values[stubValues](), values: raw(`{"name":"a"}`)},
		{
			name: "keys the type does not model pass", validate: Values[stubValues](),
			values: raw(`{"name":"a","upstream":{"anything":true}}`),
		},
		{
			name: "a failed Validate is returned", validate: Values[stubValues](),
			values: raw(`{"other":1}`), wantIs: errNameMissing,
		},
		{
			// A chart with required values cannot be installed without any.
			name: "absent values are validated as the zero type", validate: Values[stubValues](),
			values: nil, wantIs: errNameMissing,
		},
		{
			name: "empty values are validated as the zero type", validate: Values[stubValues](),
			values: &apiextensionsv1.JSON{}, wantIs: errNameMissing,
		},
		{
			name: "a modelled key of the wrong type fails to decode", validate: Values[stubValues](),
			values: raw(`{"name":3}`), wantErr: "decoding values",
		},
		{
			name: "values that are not an object fail to decode", validate: Values[stubValues](),
			values: raw(`["name"]`), wantErr: "decoding values",
		},
		{
			name: "a pointer-receiver Validate is called", validate: Values[pointerValues](),
			values: raw(`{}`), wantIs: errNameMissing,
		},
		{name: "a type without Validate only decodes", validate: Values[plainValues](), values: nil},
		{
			name: "a type without Validate still rejects a wrong type", validate: Values[plainValues](),
			values: raw(`{"replicas":"two"}`), wantErr: "decoding values",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.validate(test.values)
			switch {
			case test.wantErr == "" && test.wantIs == nil:
				if err != nil {
					t.Errorf("error = %v, want none", err)
				}
			case test.wantIs != nil && !errors.Is(err, test.wantIs):
				t.Errorf("error = %v, want one wrapping %v", err, test.wantIs)
			case test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)):
				t.Errorf("error = %v, want one containing %q", err, test.wantErr)
			}
		})
	}
}

// TestGetChecksChartValues: the registered checks are the charts' own, through their
// Values types and Validate methods.
func TestGetChecksChartValues(t *testing.T) {
	validators := Get()
	tests := []struct {
		name    string
		chart   string
		values  string
		wantErr bool
	}{
		{name: "keda accepts its values", chart: keda.Chart, values: `{"keda":{"operator":{"replicaCount":1}}}`},
		{
			name: "keda rejects a replica count of the wrong type", chart: keda.Chart,
			values: `{"keda":{"operator":{"replicaCount":"one"}}}`, wantErr: true,
		},
		{
			name: "kai_gpu_queues accepts distinct queues", chart: kaigpuqueues.Chart,
			values: `{"queues":[{"name":"a","gpuQuota":1},{"name":"b","gpuQuota":-1}]}`,
		},
		{
			name: "kai_gpu_queues runs its Validate", chart: kaigpuqueues.Chart,
			values: `{"queues":[{"name":"a","gpuQuota":1},{"name":"a","gpuQuota":2}]}`, wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			validate, ok := validators.Lookup(test.chart)
			if !ok {
				t.Fatalf("%s is not registered", test.chart)
			}
			if err := validate(raw(test.values)); (err != nil) != test.wantErr {
				t.Errorf("error = %v, want error %v", err, test.wantErr)
			}
		})
	}

	if _, ok := validators.Lookup("catalogs/cluster_base/not_a_chart"); ok {
		t.Error("an unknown chart is registered")
	}
}

func raw(s string) *apiextensionsv1.JSON {
	return &apiextensionsv1.JSON{Raw: []byte(s)}
}
