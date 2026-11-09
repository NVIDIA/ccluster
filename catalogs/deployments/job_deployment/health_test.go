/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package jobdeployment

import (
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
	"github.com/NVIDIA/ccluster/catalogs/deployments/deploymentstest"
)

func TestHealth(t *testing.T) {
	jobRef := catalogs.ResourceRef{Group: "batch", Version: "v1", Kind: "Job", Name: "train", UID: "j1"}
	job := func(health, message string) catalogs.ObservedResource {
		return catalogs.ObservedResource{ResourceRef: jobRef, Health: health, HealthMessage: message}
	}
	pod := func(name, health, statusReason string) catalogs.ObservedResource {
		return catalogs.ObservedResource{
			ResourceRef: catalogs.ResourceRef{Version: "v1", Kind: "Pod", Name: name, UID: name},
			Parents:     []catalogs.ResourceRef{jobRef},
			Health:      health,
			Info:        map[string]string{catalogs.InfoStatusReason: statusReason},
		}
	}
	failedJob := deploymentstest.Object(t, &batchv1.Job{Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{
		Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded",
	}}}})
	unschedulable := deploymentstest.Object(t, &corev1.Pod{Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{
		Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable", Message: "0/3 nodes are available",
	}}}})

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
			name:       "running while Argo CD reports Progressing",
			aggregate:  "Progressing",
			resources:  catalogs.ObservedResources{job("Progressing", ""), pod("train-a", "Progressing", "Running")},
			wantHealth: v1alpha1.ComponentHealthHealthy,
			wantReason: ReasonRunning,
		},
		{
			name:       "complete",
			aggregate:  "Healthy",
			resources:  catalogs.ObservedResources{job("Healthy", ""), pod("train-a", "Healthy", "Completed")},
			wantHealth: v1alpha1.ComponentHealthSucceeded,
			wantReason: ReasonComplete,
		},
		{
			name:        "given up",
			aggregate:   "Degraded",
			resources:   catalogs.ObservedResources{job("Degraded", "Job has reached the specified backoff limit")},
			objects:     map[string]map[string]any{"Job/train": failedJob},
			wantHealth:  v1alpha1.ComponentHealthFailed,
			wantReason:  "BackoffLimitExceeded",
			wantMessage: "Job train failed: Job has reached the specified backoff limit",
		},
		{
			name:        "waiting for a node",
			aggregate:   "Progressing",
			resources:   catalogs.ObservedResources{job("Progressing", ""), pod("train-a", "Progressing", "Pending")},
			objects:     map[string]map[string]any{"Pod/train-a": unschedulable},
			wantHealth:  v1alpha1.ComponentHealthProgressing,
			wantReason:  "Unschedulable",
			wantMessage: "Pod train-a: 0/3 nodes are available",
		},
		{
			name:      "retrying after a failed Pod is not a failed job",
			aggregate: "Progressing",
			resources: catalogs.ObservedResources{job("Progressing", ""), pod("train-a", "Degraded", "Error")},
			objects:   map[string]map[string]any{"Pod/train-a": deploymentstest.Object(t, &corev1.Pod{})},
			// The Pod itself says no more than the tree did.
			wantHealth: v1alpha1.ComponentHealthProgressing,
			wantReason: "Error",
		},
		{
			name:       "no Job yet",
			aggregate:  "Missing",
			wantHealth: v1alpha1.ComponentHealthMissing,
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
