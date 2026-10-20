/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package catalogs

import (
	"slices"
	"testing"
)

func names(resources ObservedResources) []string {
	out := make([]string, 0, len(resources))
	for _, r := range resources {
		out = append(out, r.Name)
	}
	return out
}

func TestObservedResources(t *testing.T) {
	deployment := ResourceRef{Group: "apps", Version: "v1", Kind: "Deployment", Namespace: "ns", Name: "web", UID: "d1"}
	replicaSet := ResourceRef{Group: "apps", Version: "v1", Kind: "ReplicaSet", Namespace: "ns", Name: "web-1", UID: "r1"}
	// Argo CD gives a parent without its UID when it has not seen the parent itself.
	byName := ResourceRef{Group: "apps", Kind: "ReplicaSet", Namespace: "ns", Name: "web-1"}
	resources := ObservedResources{
		{ResourceRef: deployment},
		{ResourceRef: replicaSet, Parents: []ResourceRef{deployment}},
		{ResourceRef: ResourceRef{Version: "v1", Kind: "Pod", Namespace: "ns", Name: "web-1-a"},
			Parents: []ResourceRef{replicaSet}},
		{ResourceRef: ResourceRef{Version: "v1", Kind: "Pod", Namespace: "ns", Name: "web-1-b"},
			Parents: []ResourceRef{byName}},
		{ResourceRef: ResourceRef{Version: "v1", Kind: "Pod", Namespace: "ns", Name: "other"},
			Parents: []ResourceRef{{Group: "apps", Kind: "ReplicaSet", Namespace: "ns", Name: "other", UID: "r2"}}},
	}

	for _, tc := range []struct {
		name string
		got  ObservedResources
		want []string
	}{
		{"pods", resources.OfKind("", "Pod"), []string{"web-1-a", "web-1-b", "other"}},
		{"kind in another group", resources.OfKind("", "Deployment"), []string{}},
		{"children by UID or by name", resources.ChildrenOf(replicaSet), []string{"web-1-a", "web-1-b"}},
		{"only direct children", resources.ChildrenOf(deployment), []string{"web-1"}},
		{"leaf", resources.ChildrenOf(resources[2].ResourceRef), []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := names(tc.got); !slices.Equal(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
