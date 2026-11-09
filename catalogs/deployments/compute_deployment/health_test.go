/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package computedeployment

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
	"github.com/NVIDIA/ccluster/catalogs/deployments/deploymentstest"
)

func TestHealth(t *testing.T) {
	replicaSet := catalogs.ResourceRef{Group: "apps", Kind: "ReplicaSet", Name: "box-1", UID: "r1"}
	deployment := func(health string) catalogs.ObservedResource {
		return catalogs.ObservedResource{ResourceRef: catalogs.ResourceRef{Group: "apps", Kind: "Deployment", Name: "box"},
			Health: health}
	}
	service := func(health string) catalogs.ObservedResource {
		return catalogs.ObservedResource{ResourceRef: catalogs.ResourceRef{Version: "v1", Kind: "Service", Name: "box"},
			Health: health}
	}
	pod := func(health, statusReason string) catalogs.ObservedResource {
		return catalogs.ObservedResource{
			ResourceRef: catalogs.ResourceRef{Version: "v1", Kind: "Pod", Name: "box-1-a", UID: "p1"},
			Parents:     []catalogs.ResourceRef{replicaSet},
			Health:      health,
			Info:        map[string]string{catalogs.InfoStatusReason: statusReason},
		}
	}

	for _, tc := range []struct {
		name        string
		aggregate   string
		resources   catalogs.ObservedResources
		objects     map[string]map[string]any
		wantHealth  v1alpha1.ComponentHealth
		wantReason  string
		wantMessage string
	}{
		{
			name:        "waiting for a load balancer",
			aggregate:   "Progressing",
			resources:   catalogs.ObservedResources{deployment("Healthy"), service("Progressing"), pod("Healthy", "Running")},
			wantHealth:  v1alpha1.ComponentHealthProgressing,
			wantReason:  ReasonLoadBalancerPending,
			wantMessage: "Service box is waiting for a load balancer address",
		},
		{
			name:      "a Deployment that is not ready keeps its own message",
			aggregate: "Progressing",
			resources: catalogs.ObservedResources{
				{
					ResourceRef:   catalogs.ResourceRef{Group: "apps", Kind: "Deployment", Name: "box"},
					Health:        "Progressing",
					HealthMessage: "Waiting for rollout to finish: 0 of 1 updated replicas are available...",
				},
				service("Progressing"), pod("Progressing", "Running"),
			},
			wantHealth:  v1alpha1.ComponentHealthProgressing,
			wantMessage: "Deployment box: Waiting for rollout to finish",
		},
		{
			name:      "a Pod that is not running explains it first",
			aggregate: "Progressing",
			resources: catalogs.ObservedResources{
				deployment("Progressing"), service("Progressing"), pod("Progressing", "Pending"),
			},
			objects: map[string]map[string]any{"Pod/box-1-a": deploymentstest.Object(t, &corev1.Pod{Status: corev1.PodStatus{
				Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse,
					Reason: "Unschedulable", Message: "0/3 nodes are available"}},
			}})},
			wantHealth: v1alpha1.ComponentHealthProgressing,
			wantReason: "Unschedulable",
		},
		{
			name:       "ready to SSH to",
			aggregate:  "Healthy",
			resources:  catalogs.ObservedResources{deployment("Healthy"), service("Healthy"), pod("Healthy", "Running")},
			wantHealth: v1alpha1.ComponentHealthHealthy,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Health(t.Context(), v1alpha1.ComponentEntry{Chart: Chart},
				catalogs.Observation{
					ApplicationHealth: tc.aggregate,
					Resources:         tc.resources,
					Reader:            &deploymentstest.Reader{Objects: tc.objects},
				})
			if got.Status != tc.wantHealth || got.Reason != tc.wantReason {
				t.Errorf("health = %q/%q, want %q/%q", got.Status, got.Reason, tc.wantHealth, tc.wantReason)
			}
			if !strings.HasPrefix(got.Message, tc.wantMessage) {
				t.Errorf("message = %q, want it to start with %q", got.Message, tc.wantMessage)
			}
		})
	}
}
