/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

import (
	"slices"
	"testing"

	"github.com/NVIDIA/ccluster/catalogs/components"
)

// TestDrift: rules are looked up by the chart a component names, so a
// catalog chart missing from the registry would silently be diffed in full, and a
// registered chart outside components.All would never match a component. The registry
// keeps components.All order, so a missing or extra chart is easy to place.
func TestDrift(t *testing.T) {
	want := make([]string, 0, len(components.All))
	for _, c := range components.All {
		want = append(want, c.Chart)
	}
	if got := Drift().Charts(); !slices.Equal(got, want) {
		t.Errorf("registered charts %v\nwant components.All %v", got, want)
	}
}

// TestDriftSelectsFields: a rule without a kind or a field ignores nothing,
// and the chart it was written for never converges.
func TestDriftSelectsFields(t *testing.T) {
	drift := Drift()
	for _, chart := range drift.Charts() {
		rules, _ := drift.Lookup(chart)
		for _, rule := range rules() {
			if rule.Kind == "" ||
				len(rule.JSONPointers)+len(rule.JQPathExpressions)+len(rule.ManagedFieldsManagers) == 0 {
				t.Errorf("%s: rule %+v selects no kind or no field", chart, rule)
			}
		}
	}
}
