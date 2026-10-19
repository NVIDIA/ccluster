/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package registry

import "testing"

type testFunc func() string

func a() string { return "a" }

// TestRegisterRejectsWiringBugs: a duplicate chart, an empty chart or a nil function
// panics at registration instead of surfacing at reconcile time.
func TestRegisterRejectsWiringBugs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		chart string
		f     testFunc
	}{
		{"duplicate chart", "catalogs/cluster_base/a", a},
		{"empty chart", "", a},
		{"nil function", "catalogs/cluster_base/b", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var r Registry[testFunc]
			r.Register("catalogs/cluster_base/a", a)
			defer func() {
				if recover() == nil {
					t.Errorf("Register(%q) did not panic", tc.chart)
				}
			}()
			r.Register(tc.chart, tc.f)
		})
	}
}

func TestLookup(t *testing.T) {
	var r Registry[testFunc]
	if _, ok := r.Lookup("catalogs/cluster_base/a"); ok {
		t.Error("the zero Registry found a chart")
	}

	r.Register("catalogs/cluster_base/a", a)
	f, ok := r.Lookup("catalogs/cluster_base/a")
	if !ok || f() != "a" {
		t.Errorf("Lookup of a registered chart = %v, %v", f, ok)
	}
	if _, ok := r.Lookup("catalogs/cluster_base/b"); ok {
		t.Error("Lookup found an unregistered chart")
	}
}
