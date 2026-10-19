/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package verifierregistry

import (
	"maps"
	"slices"
	"testing"

	"github.com/NVIDIA/ccluster/catalogs/components"
)

// TestGet: the operator verifies every cluster_base component, so a
// chart missing from the registry would silently be left out of the check, and a
// registered chart outside components.ClusterBase would verify something the catalog
// does not ship.
func TestGet(t *testing.T) {
	want := make([]string, 0, len(components.ClusterBase))
	for _, c := range components.ClusterBase {
		want = append(want, c.Chart)
	}
	slices.Sort(want)
	if got := slices.Sorted(maps.Keys(Get().Registry)); !slices.Equal(got, want) {
		t.Errorf("registered charts %v\nwant components.ClusterBase %v", got, want)
	}
}
