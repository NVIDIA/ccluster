/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package catalogs

import (
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
)

func names(resources ObservedResources) []string {
	out := make([]string, 0, len(resources))
	for _, r := range resources {
		out = append(out, r.Name)
	}
	return out
}

func TestObservedResources(t *testing.T) {
	deployment := ResourceRef{
		Group: appsv1.GroupName, Version: "v1", Kind: "Deployment", Namespace: "ns", Name: "web", UID: "d1",
	}
	replicaSet := ResourceRef{
		Group: appsv1.GroupName, Version: "v1", Kind: "ReplicaSet", Namespace: "ns", Name: "web-1", UID: "r1",
	}
	// Argo CD gives a parent without its UID when it has not seen the parent itself.
	byName := replicaSet
	byName.Version, byName.UID = "", ""
	pod := func(name string) ResourceRef {
		return ResourceRef{Version: "v1", Kind: "Pod", Namespace: "ns", Name: name}
	}
	podA, podB, other := pod("web-1-a"), pod("web-1-b"), pod("other")
	otherReplicaSet := ResourceRef{Group: appsv1.GroupName, Kind: "ReplicaSet", Namespace: "ns", Name: "other", UID: "r2"}
	resources := ObservedResources{
		{ResourceRef: deployment},
		{ResourceRef: replicaSet, Parents: []ResourceRef{deployment}},
		{ResourceRef: podA, Parents: []ResourceRef{replicaSet}},
		{ResourceRef: podB, Parents: []ResourceRef{byName}},
		{ResourceRef: other, Parents: []ResourceRef{otherReplicaSet}},
	}

	for _, tc := range []struct {
		name string
		got  ObservedResources
		want []string
	}{
		{"pods", resources.OfKind("", "Pod"), []string{podA.Name, podB.Name, other.Name}},
		{"kind in another group", resources.OfKind("", "Deployment"), []string{}},
		{"children by UID or by name", resources.ChildrenOf(replicaSet), []string{podA.Name, podB.Name}},
		{"only direct children", resources.ChildrenOf(deployment), []string{"web-1"}},
		{"leaf", resources.ChildrenOf(podA), []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := names(tc.got); !slices.Equal(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
