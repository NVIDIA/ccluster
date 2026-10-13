/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

import (
	"errors"
	"slices"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"github.com/NVIDIA/ccluster/catalogs/components"
)

// TestCatalogValidators: a component naming a chart missing from the registry fails
// validation, so a catalog chart left out could never be deployed, and a registered chart
// outside components.All would admit a chart the catalog does not ship.
func TestCatalogValidators(t *testing.T) {
	want := make([]string, 0, len(components.All))
	for _, c := range components.All {
		want = append(want, c.Chart)
	}
	if got := CatalogValidators().Charts(); !slices.Equal(got, want) {
		t.Errorf("registered charts %v\nwant components.All %v", got, want)
	}
}

// TestRegisterValidatorRejectsWiringBugs: a duplicate chart, an empty chart or a nil
// check panics at registration instead of surfacing as a validation result.
func TestRegisterValidatorRejectsWiringBugs(t *testing.T) {
	accept := func(*apiextensionsv1.JSON) error { return nil }
	for _, tc := range []struct {
		name     string
		chart    string
		validate ValidateFunc
	}{
		{"duplicate chart", "catalogs/a", accept},
		{"empty chart", "", accept},
		{"nil ValidateFunc", "catalogs/b", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &ValidatorRegistry{}
			r.Register("catalogs/a", accept)
			defer func() {
				if recover() == nil {
					t.Errorf("Register(%q) did not panic", tc.chart)
				}
			}()
			r.Register(tc.chart, tc.validate)
		})
	}
}

func TestLookupUnknownChart(t *testing.T) {
	if _, ok := (&ValidatorRegistry{}).Lookup("catalogs/a"); ok {
		t.Error("Lookup on an empty registry found a check")
	}
}

var errNoName = errors.New("name is required")

// testValues stands in for a chart's Values type with required values.
type testValues struct {
	Name     string `json:"name"`
	Replicas *int32 `json:"replicas,omitempty"`
}

func (v testValues) Validate() error {
	if v.Name == "" {
		return errNoName
	}
	return nil
}

// pointerValues has its Validate on the pointer receiver, which the adapter must still
// find.
type pointerValues struct {
	Name string `json:"name"`
}

func (v *pointerValues) Validate() error {
	if v.Name == "" {
		return errNoName
	}
	return nil
}

// plainValues does not implement catalogs.Validator.
type plainValues struct {
	Replicas *int32 `json:"replicas,omitempty"`
}

func TestValuesAdapter(t *testing.T) {
	raw := func(s string) *apiextensionsv1.JSON { return &apiextensionsv1.JSON{Raw: []byte(s)} }

	for _, tc := range []struct {
		name     string
		validate ValidateFunc
		values   *apiextensionsv1.JSON
		wantErr  error // nil: no error; errAny: some error
	}{
		{"valid values pass", Values[testValues](), raw(`{"name":"a"}`), nil},
		{"unmodelled keys pass", Values[testValues](), raw(`{"name":"a","extra":{"x":1}}`), nil},
		{"Validate's error is returned", Values[testValues](), raw(`{"replicas":2}`), errNoName},
		{"nil values are the zero Values", Values[testValues](), nil, errNoName},
		{"empty values are the zero Values", Values[testValues](), raw(``), errNoName},
		{"a pointer-receiver Validate runs", Values[pointerValues](), raw(`{}`), errNoName},
		{"a modelled key of the wrong type fails", Values[plainValues](), raw(`{"replicas":"two"}`), errAny},
		{"values that are not an object fail", Values[plainValues](), raw(`[1]`), errAny},
		{"no Validator, nothing to check", Values[plainValues](), nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.validate(tc.values)
			switch {
			case tc.wantErr == nil && err != nil:
				t.Errorf("got error %v, want none", err)
			case tc.wantErr == errAny && err == nil:
				t.Error("got no error, want one")
			case tc.wantErr != nil && tc.wantErr != errAny && !errors.Is(err, tc.wantErr):
				t.Errorf("got error %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// errAny marks a case expecting some error, whichever it is.
var errAny = errors.New("any error")
