/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package verifierregistry

import (
	"slices"
	"testing"

	"github.com/NVIDIA/ccluster/catalogs/components"
)

// TestGet: a registered chart outside components.ClusterBase would verify something
// the catalog does not ship, and could never pass. The registry is sorted by chart, the
// order failures are reported in.
func TestGet(t *testing.T) {
	charts := make([]string, 0, len(components.ClusterBase))
	for _, c := range components.ClusterBase {
		charts = append(charts, c.Chart)
	}
	got := Get().Charts()
	for _, chart := range got {
		if !slices.Contains(charts, chart) {
			t.Errorf("registered chart %s is not in components.ClusterBase", chart)
		}
	}
	if !slices.IsSorted(got) {
		t.Errorf("registered charts %v are not sorted", got)
	}
}
