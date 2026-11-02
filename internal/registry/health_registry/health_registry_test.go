/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package healthregistry

import (
	"slices"
	"testing"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
	"github.com/NVIDIA/ccluster/catalogs/components"
)

// TestGet: health functions are looked up by the chart a component names, so a catalog
// chart missing from the registry would never report its health, and a registered chart
// outside components.All would never match a component. The registry keeps
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

// TestHealthsAreValid: every registered function reports a health the CRD admits, even
// with nothing observed and nothing readable, so the operator's status write cannot be
// rejected and its fallback is not what reports the chart.
func TestHealthsAreValid(t *testing.T) {
	healthFuncs := Get()
	for _, chart := range healthFuncs.Charts() {
		healthFunc, _ := healthFuncs.Lookup(chart)
		for _, aggregate := range []string{"", "Healthy", "Progressing", "Degraded"} {
			got := healthFunc(t.Context(), cclusternvidiacomv1alpha1.ComponentEntry{Chart: chart},
				catalogs.Observation{ApplicationHealth: aggregate})
			if !catalogs.ValidHealth(got.Status) {
				t.Errorf("%s with aggregate %q: health %q is not one the CRD admits", chart, aggregate, got.Status)
			}
		}
	}
}
