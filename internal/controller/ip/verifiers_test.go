/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package ip

import (
	"slices"
	"testing"

	"github.com/NVIDIA/ccluster/catalogs/components"
)

// TestClusterBaseVerifiers: the operator verifies every cluster_base component, so a
// chart missing from the registry would silently be left out of the check, and a
// registered chart outside components.ClusterBase would verify something the catalog
// does not ship. The order is checked too, since it is the order failures are reported in.
func TestClusterBaseVerifiers(t *testing.T) {
	want := make([]string, 0, len(components.ClusterBase))
	for _, c := range components.ClusterBase {
		want = append(want, c.Chart)
	}
	registered := ClusterBaseVerifiers().Verifiers()
	got := make([]string, 0, len(registered))
	for _, v := range registered {
		got = append(got, v.Chart)
	}
	if !slices.Equal(got, want) {
		t.Errorf("registered charts %v\nwant components.ClusterBase %v", got, want)
	}
}

// TestRegisterRejectsWiringBugs: a duplicate chart, an empty chart or a nil check panics
// at registration instead of surfacing as a verification result.
func TestRegisterRejectsWiringBugs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		chart  string
		verify VerifyFunc
	}{
		{"duplicate chart", "catalogs/cluster_base/a", alwaysVerified},
		{"empty chart", "", alwaysVerified},
		{"nil VerifyFunc", "catalogs/cluster_base/b", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Registry{}
			r.Register("catalogs/cluster_base/a", alwaysVerified)
			defer func() {
				if recover() == nil {
					t.Errorf("Register(%q) did not panic", tc.chart)
				}
			}()
			r.Register(tc.chart, tc.verify)
		})
	}
}
