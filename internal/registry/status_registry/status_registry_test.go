/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package statusregistry

import (
	"slices"
	"testing"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs/components"
)

// TestGet: status functions are looked up by the chart a component names, so a catalog
// chart missing from the registry would never report a status, and a registered chart
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

// TestStatesAreValid: every registered function reports a state the CRD admits, even
// with nothing observed, so the operator's status write cannot be rejected.
func TestStatesAreValid(t *testing.T) {
	valid := []cclusternvidiacomv1alpha1.ComponentState{
		cclusternvidiacomv1alpha1.ComponentStatePending,
		cclusternvidiacomv1alpha1.ComponentStateStarting,
		cclusternvidiacomv1alpha1.ComponentStateRunning,
		cclusternvidiacomv1alpha1.ComponentStateFailing,
		cclusternvidiacomv1alpha1.ComponentStateSucceeded,
		cclusternvidiacomv1alpha1.ComponentStateFailed,
		cclusternvidiacomv1alpha1.ComponentStateUnknown,
	}
	statuses := Get()
	for _, chart := range statuses.Charts() {
		status, _ := statuses.Lookup(chart)
		got := status(cclusternvidiacomv1alpha1.ComponentEntry{Chart: chart}, nil)
		if !slices.Contains(valid, got.State) {
			t.Errorf("%s: state %q is not one the CRD admits", chart, got.State)
		}
	}
}
