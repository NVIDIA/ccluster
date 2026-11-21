/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package dynamodeployment

import (
	"errors"
	"strings"
	"testing"

	"github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
	"github.com/NVIDIA/ccluster/catalogs/deployments/deploymentstest"
)

func TestHealth(t *testing.T) {
	graph := catalogs.ObservedResource{ResourceRef: catalogs.ResourceRef{
		Group: "nvidia.com", Version: "v1beta1", Kind: "DynamoGraphDeployment", Name: "llm", UID: "g1"}}
	worker := catalogs.ObservedResource{
		ResourceRef: catalogs.ResourceRef{Version: "v1", Kind: "Pod", Name: "llm-worker-0", UID: "p1"},
		Parents:     []catalogs.ResourceRef{{Group: "apps", Kind: "ReplicaSet", Name: "llm-worker-1"}},
		Health:      "Degraded",
		Info:        map[string]string{catalogs.InfoStatusReason: "CrashLoopBackOff"},
	}
	dgdWithStatus := func(generation int64, status map[string]any) map[string]any {
		return map[string]any{
			"apiVersion": "nvidia.com/v1beta1", "kind": "DynamoGraphDeployment",
			"metadata": map[string]any{"name": "llm", "generation": generation},
			"status":   status,
		}
	}
	dgd := func(generation, observed int64, state, readyReason, readyMessage string) map[string]any {
		status := map[string]any{"observedGeneration": observed, "state": state}
		if readyReason != "" {
			status["conditions"] = []any{map[string]any{
				"type": "Ready", "status": "False", "reason": readyReason, "message": readyMessage,
			}}
		}
		return dgdWithStatus(generation, status)
	}
	// failedUpdate is a graph at generation 3 as a failed reconcile leaves it: state failed,
	// the Ready condition's observedGeneration at readyObserved, and status.observedGeneration
	// still at observed, the last generation that reconciled successfully.
	failedUpdate := func(observed, readyObserved int64) map[string]any {
		return dgdWithStatus(3, map[string]any{
			"observedGeneration": observed, "state": "failed",
			"conditions": []any{map[string]any{
				"type": "Ready", "status": "False", "observedGeneration": readyObserved,
				"reason": "failed_to_reconcile_the_resources", "message": "checkpoint is not compatible",
			}},
		})
	}

	for _, tc := range []struct {
		name        string
		aggregate   string
		resources   catalogs.ObservedResources
		reader      *deploymentstest.Reader
		wantHealth  v1alpha1.ComponentHealth
		wantReason  string
		wantMessage string
	}{
		{
			name:      "successful",
			aggregate: "Healthy",
			resources: catalogs.ObservedResources{graph},
			reader: &deploymentstest.Reader{Objects: map[string]map[string]any{
				"DynamoGraphDeployment/llm": dgd(2, 2, "successful", "", ""),
			}},
			wantHealth: v1alpha1.ComponentHealthHealthy,
		},
		{
			name:      "pending while Argo CD, which leaves the graph out, reports Healthy",
			aggregate: "Healthy",
			resources: catalogs.ObservedResources{graph},
			reader: &deploymentstest.Reader{Objects: map[string]map[string]any{
				"DynamoGraphDeployment/llm": dgd(2, 2, "pending", "insufficient_capacity", "worker: 0/1 scheduled"),
			}},
			wantHealth:  v1alpha1.ComponentHealthProgressing,
			wantReason:  "InsufficientCapacity",
			wantMessage: "DynamoGraphDeployment llm: worker: 0/1 scheduled",
		},
		{
			name:      "failed",
			aggregate: "Healthy",
			resources: catalogs.ObservedResources{graph},
			reader: &deploymentstest.Reader{Objects: map[string]map[string]any{
				"DynamoGraphDeployment/llm": dgd(2, 2, "failed", "pods_not_ready", "frontend: 0/1 ready"),
			}},
			wantHealth: v1alpha1.ComponentHealthDegraded,
			wantReason: "PodsNotReady",
		},
		{
			name:      "the operator has not seen the current spec",
			aggregate: "Healthy",
			resources: catalogs.ObservedResources{graph},
			reader: &deploymentstest.Reader{Objects: map[string]map[string]any{
				"DynamoGraphDeployment/llm": dgd(3, 2, "successful", "", ""),
			}},
			wantHealth: v1alpha1.ComponentHealthProgressing,
		},
		{
			name:      "the current spec failed, which only the Ready condition records",
			aggregate: "Healthy",
			resources: catalogs.ObservedResources{graph},
			reader: &deploymentstest.Reader{Objects: map[string]map[string]any{
				"DynamoGraphDeployment/llm": failedUpdate(2, 3),
			}},
			wantHealth:  v1alpha1.ComponentHealthDegraded,
			wantReason:  "FailedToReconcileTheResources",
			wantMessage: "DynamoGraphDeployment llm: checkpoint is not compatible",
		},
		{
			name:      "an earlier spec failed and the operator has not seen the current one",
			aggregate: "Healthy",
			resources: catalogs.ObservedResources{graph},
			reader: &deploymentstest.Reader{Objects: map[string]map[string]any{
				"DynamoGraphDeployment/llm": failedUpdate(2, 2),
			}},
			wantHealth:  v1alpha1.ComponentHealthProgressing,
			wantMessage: "DynamoGraphDeployment llm: waiting for the Dynamo operator to see the current spec",
		},
		{
			name:      "no spec has reconciled yet and the operator has not seen the current one",
			aggregate: "Healthy",
			resources: catalogs.ObservedResources{graph},
			reader: &deploymentstest.Reader{Objects: map[string]map[string]any{
				"DynamoGraphDeployment/llm": failedUpdate(0, 2),
			}},
			wantHealth:  v1alpha1.ComponentHealthProgressing,
			wantMessage: "DynamoGraphDeployment llm: waiting for the Dynamo operator to see the current spec",
		},
		{
			name:      "a crashing worker is worse than a pending graph",
			aggregate: "Healthy",
			resources: catalogs.ObservedResources{graph, worker},
			reader: &deploymentstest.Reader{Objects: map[string]map[string]any{
				"DynamoGraphDeployment/llm": dgd(2, 2, "pending", "pods_not_ready", ""),
			}},
			wantHealth: v1alpha1.ComponentHealthDegraded,
			wantReason: "CrashLoopBackOff",
		},
		{
			name:        "an unreadable graph keeps the default health",
			aggregate:   "Healthy",
			resources:   catalogs.ObservedResources{graph},
			reader:      &deploymentstest.Reader{Err: errors.New("unreachable")},
			wantHealth:  v1alpha1.ComponentHealthHealthy,
			wantReason:  ReasonGraphUnreadable,
			wantMessage: "DynamoGraphDeployment llm could not be read: unreachable",
		},
		{
			name:      "an unreadable graph leaves a Pod's reason",
			aggregate: "Healthy",
			resources: catalogs.ObservedResources{graph, worker},
			reader:    &deploymentstest.Reader{Err: errors.New("unreachable")},
			// The worker Pod cannot be read either, so its reason comes from the tree.
			wantHealth: v1alpha1.ComponentHealthDegraded,
			wantReason: "CrashLoopBackOff",
		},
		{
			name:      "an operator that does not set observedGeneration",
			aggregate: "Healthy",
			resources: catalogs.ObservedResources{graph},
			reader: &deploymentstest.Reader{Objects: map[string]map[string]any{
				"DynamoGraphDeployment/llm": dgd(2, 0, "successful", "", ""),
			}},
			wantHealth: v1alpha1.ComponentHealthHealthy,
		},
		{
			name:      "an operator that sets observedGeneration on neither the status nor Ready",
			aggregate: "Healthy",
			resources: catalogs.ObservedResources{graph},
			reader: &deploymentstest.Reader{Objects: map[string]map[string]any{
				"DynamoGraphDeployment/llm": dgd(2, 0, "failed", "pods_not_ready", ""),
			}},
			wantHealth: v1alpha1.ComponentHealthDegraded,
			wantReason: "PodsNotReady",
		},
		{
			name:        "a graph gone since the tree was cached keeps the default health",
			aggregate:   "Healthy",
			resources:   catalogs.ObservedResources{graph},
			reader:      &deploymentstest.Reader{},
			wantHealth:  v1alpha1.ComponentHealthHealthy,
			wantReason:  ReasonGraphUnreadable,
			wantMessage: "DynamoGraphDeployment llm could not be read: it no longer exists",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Health(t.Context(), v1alpha1.ComponentEntry{Chart: Chart},
				catalogs.Observation{ApplicationHealth: tc.aggregate, Resources: tc.resources, Reader: tc.reader})
			if got.Status != tc.wantHealth || got.Reason != tc.wantReason {
				t.Errorf("health = %q/%q, want %q/%q", got.Status, got.Reason, tc.wantHealth, tc.wantReason)
			}
			if !strings.HasPrefix(got.Message, tc.wantMessage) {
				t.Errorf("message = %q, want it to start with %q", got.Message, tc.wantMessage)
			}
		})
	}
}
