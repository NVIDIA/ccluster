/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package catalogs_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
	"github.com/NVIDIA/ccluster/catalogs/deployments/deploymentstest"
)

var (
	deployment = catalogs.ObservedResource{
		ResourceRef:   catalogs.ResourceRef{Group: "apps", Kind: "Deployment", Name: "web", UID: "d1"},
		Health:        "Progressing",
		HealthMessage: "Waiting for rollout to finish: 0 of 1 updated replicas are available...",
	}
	replicaSet = catalogs.ResourceRef{Group: "apps", Kind: "ReplicaSet", Name: "web-1", UID: "r1"}
	job        = catalogs.ResourceRef{Group: "batch", Kind: "Job", Name: "run", UID: "j1"}
)

func pod(name string, owner catalogs.ResourceRef, health, statusReason string) catalogs.ObservedResource {
	return catalogs.ObservedResource{
		ResourceRef: catalogs.ResourceRef{Version: "v1", Kind: "Pod", Name: name, UID: name},
		Parents:     []catalogs.ResourceRef{owner},
		Health:      health,
		Info:        map[string]string{catalogs.InfoStatusReason: statusReason},
	}
}

func crashLooping(t *testing.T, lastReason string, exitCode int32) map[string]any {
	return deploymentstest.Object(t, &corev1.Pod{Status: corev1.PodStatus{
		ContainerStatuses: []corev1.ContainerStatus{{
			Name:  "main",
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			LastTerminationState: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{Reason: lastReason, ExitCode: exitCode},
			},
		}},
	}})
}

func unschedulable(t *testing.T) map[string]any {
	return deploymentstest.Object(t, &corev1.Pod{Status: corev1.PodStatus{
		Phase: corev1.PodPending,
		Conditions: []corev1.PodCondition{{
			Type: corev1.PodScheduled, Status: corev1.ConditionFalse,
			Reason: "Unschedulable", Message: "0/3 nodes are available: 3 Insufficient nvidia.com/gpu.",
		}},
	}})
}

func TestDefaultHealth(t *testing.T) {
	crashing := pod("web-1-a", replicaSet, "Degraded", "CrashLoopBackOff")
	crashing.HealthMessage = "back-off 5m0s restarting failed container=main"
	pending := pod("web-1-a", replicaSet, "Progressing", "Pending")
	creating := pod("web-1-a", replicaSet, "Progressing", "ContainerCreating")
	failedJobPod := pod("run-a", job, "Degraded", "Error")
	evicted := pod("web-1-b", replicaSet, "Degraded", "Evicted")
	evicted.HealthMessage = "The node was low on resource: memory."
	oldReplicaSet := catalogs.ResourceRef{Group: "apps", Kind: "ReplicaSet", Name: "web-0", UID: "r0"}
	terminating := pod("web-0-a", oldReplicaSet, "Progressing", "Terminating")
	ownedByDeployment := func(ref catalogs.ResourceRef, health string) catalogs.ObservedResource {
		return catalogs.ObservedResource{
			ResourceRef: ref, Parents: []catalogs.ResourceRef{deployment.ResourceRef}, Health: health,
		}
	}

	for _, tc := range []struct {
		name        string
		aggregate   string
		resources   catalogs.ObservedResources
		reader      *deploymentstest.Reader
		wantHealth  v1alpha1.ComponentHealth
		wantReason  string
		wantMessage string
		maxReads    int
	}{
		{
			name:       "healthy as Argo CD says",
			aggregate:  "Healthy",
			resources:  catalogs.ObservedResources{{ResourceRef: deployment.ResourceRef, Health: "Healthy"}},
			wantHealth: v1alpha1.ComponentHealthHealthy,
		},
		{
			name:        "progressing names the rendered object",
			aggregate:   "Progressing",
			resources:   catalogs.ObservedResources{deployment},
			wantHealth:  v1alpha1.ComponentHealthProgressing,
			wantMessage: "Deployment web: Waiting for rollout to finish",
		},
		{
			name:      "an object with no message is named with its health",
			aggregate: "Progressing",
			resources: catalogs.ObservedResources{
				{ResourceRef: catalogs.ResourceRef{Version: "v1", Kind: "Service", Name: "web"}, Health: "Progressing"},
				{ResourceRef: deployment.ResourceRef, Health: "Healthy"},
			},
			wantHealth:  v1alpha1.ComponentHealthProgressing,
			wantMessage: "Service web is Progressing",
		},
		{
			name:      "a crashing Pod degrades a progressing Deployment, OOMKilled read from the Pod",
			aggregate: "Progressing",
			resources: catalogs.ObservedResources{deployment, crashing},
			reader: &deploymentstest.Reader{Objects: map[string]map[string]any{
				"Pod/web-1-a": crashLooping(t, "OOMKilled", 137),
			}},
			wantHealth:  v1alpha1.ComponentHealthDegraded,
			wantReason:  "OOMKilled",
			wantMessage: `Pod web-1-a: container "main" last exited with code 137 (OOMKilled), now CrashLoopBackOff`,
			maxReads:    1,
		},
		{
			name:      "a crashing Pod degrades what Argo CD calls healthy",
			aggregate: "Healthy",
			resources: catalogs.ObservedResources{{ResourceRef: deployment.ResourceRef, Health: "Healthy"}, crashing},
			reader: &deploymentstest.Reader{Objects: map[string]map[string]any{
				"Pod/web-1-a": crashLooping(t, "Error", 1),
			}},
			wantHealth: v1alpha1.ComponentHealthDegraded,
			wantReason: "CrashLoopBackOff",
			maxReads:   1,
		},
		{
			name:      "a crashing Pod explains a component Argo CD already calls Degraded",
			aggregate: "Degraded",
			resources: catalogs.ObservedResources{
				{
					ResourceRef:   deployment.ResourceRef,
					Health:        "Degraded",
					HealthMessage: `Deployment "web" exceeded its progress deadline`,
				},
				crashing,
			},
			reader: &deploymentstest.Reader{Objects: map[string]map[string]any{
				"Pod/web-1-a": crashLooping(t, "OOMKilled", 137),
			}},
			wantHealth: v1alpha1.ComponentHealthDegraded,
			wantReason: "OOMKilled",
			maxReads:   1,
		},
		{
			name:       "a crashing Pod leaves Unknown as it is",
			aggregate:  "Unknown",
			resources:  catalogs.ObservedResources{crashing},
			wantHealth: v1alpha1.ComponentHealthUnknown,
		},
		{
			name:        "an unreadable Pod keeps the tree's explanation",
			aggregate:   "Progressing",
			resources:   catalogs.ObservedResources{deployment, crashing},
			reader:      &deploymentstest.Reader{Err: errors.New("unreachable")},
			wantHealth:  v1alpha1.ComponentHealthDegraded,
			wantReason:  "CrashLoopBackOff",
			wantMessage: "Pod web-1-a: back-off 5m0s restarting failed container=main",
			maxReads:    1,
		},
		{
			name:      "a Pending Pod explains a progressing component",
			aggregate: "Progressing",
			resources: catalogs.ObservedResources{deployment, pending},
			reader: &deploymentstest.Reader{Objects: map[string]map[string]any{
				"Pod/web-1-a": unschedulable(t),
			}},
			wantHealth:  v1alpha1.ComponentHealthProgressing,
			wantReason:  "Unschedulable",
			wantMessage: "Pod web-1-a: 0/3 nodes are available: 3 Insufficient nvidia.com/gpu.",
			maxReads:    1,
		},
		{
			name:      "events explain a Pod the Pod itself does not",
			aggregate: "Progressing",
			resources: catalogs.ObservedResources{deployment, creating},
			reader: &deploymentstest.Reader{
				Objects: map[string]map[string]any{"Pod/web-1-a": deploymentstest.Object(t, &corev1.Pod{})},
				EventsOf: map[string][]corev1.Event{"Pod/web-1-a": {
					{Type: corev1.EventTypeWarning, Reason: "FailedMount", Message: "old",
						LastTimestamp: metav1.NewTime(time.Unix(100, 0))},
					{Type: corev1.EventTypeWarning, Reason: "FailedAttachVolume", Message: "volume is attached elsewhere",
						LastTimestamp: metav1.NewTime(time.Unix(200, 0))},
					{Type: corev1.EventTypeNormal, Reason: "Scheduled", Message: "assigned",
						LastTimestamp: metav1.NewTime(time.Unix(300, 0))},
				}},
			},
			wantHealth:  v1alpha1.ComponentHealthProgressing,
			wantReason:  "FailedAttachVolume",
			wantMessage: "Pod web-1-a: volume is attached elsewhere",
			maxReads:    2,
		},
		{
			name:      "an evicted Pod of a Healthy ReplicaSet says nothing",
			aggregate: "Healthy",
			resources: catalogs.ObservedResources{
				{ResourceRef: deployment.ResourceRef, Health: "Healthy"},
				ownedByDeployment(replicaSet, "Healthy"),
				evicted,
			},
			wantHealth: v1alpha1.ComponentHealthHealthy,
		},
		{
			name:      "a Pod of a Healthy old ReplicaSet does not explain a rollout",
			aggregate: "Progressing",
			resources: catalogs.ObservedResources{
				deployment,
				ownedByDeployment(oldReplicaSet, "Healthy"),
				terminating,
				ownedByDeployment(replicaSet, "Progressing"),
				pending,
			},
			reader: &deploymentstest.Reader{Objects: map[string]map[string]any{
				"Pod/web-1-a": unschedulable(t),
			}},
			wantHealth: v1alpha1.ComponentHealthProgressing,
			wantReason: "Unschedulable",
			maxReads:   1,
		},
		{
			name:       "a Job's failed Pod is the Job's to judge",
			aggregate:  "Progressing",
			resources:  catalogs.ObservedResources{{ResourceRef: job, Health: "Progressing"}, failedJobPod},
			wantHealth: v1alpha1.ComponentHealthProgressing,
		},
		{
			name:       "not yet assessed",
			aggregate:  "",
			wantHealth: v1alpha1.ComponentHealthUnknown,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := tc.reader
			if reader == nil {
				reader = &deploymentstest.Reader{}
			}
			got := catalogs.DefaultHealth(t.Context(), v1alpha1.ComponentEntry{},
				catalogs.Observation{ApplicationHealth: tc.aggregate, Resources: tc.resources, Reader: reader})
			if got.Status != tc.wantHealth || got.Reason != tc.wantReason {
				t.Errorf("health = %q/%q, want %q/%q", got.Status, got.Reason, tc.wantHealth, tc.wantReason)
			}
			if !strings.HasPrefix(got.Message, tc.wantMessage) {
				t.Errorf("message = %q, want it to start with %q", got.Message, tc.wantMessage)
			}
			if reader.Reads > tc.maxReads {
				t.Errorf("%d reads, want at most %d", reader.Reads, tc.maxReads)
			}
		})
	}
}

func TestValidHealth(t *testing.T) {
	for _, health := range []v1alpha1.ComponentHealth{
		v1alpha1.ComponentHealthHealthy,
		v1alpha1.ComponentHealthProgressing,
		v1alpha1.ComponentHealthDegraded,
		v1alpha1.ComponentHealthSuspended,
		v1alpha1.ComponentHealthMissing,
		v1alpha1.ComponentHealthUnknown,
		v1alpha1.ComponentHealthSucceeded,
		v1alpha1.ComponentHealthFailed,
	} {
		if !catalogs.ValidHealth(health) {
			t.Errorf("%s is not valid", health)
		}
	}
	for _, health := range []v1alpha1.ComponentHealth{"", "Running"} {
		if catalogs.ValidHealth(health) {
			t.Errorf("%q is valid", health)
		}
	}
}

func TestWorse(t *testing.T) {
	order := []v1alpha1.ComponentHealth{
		v1alpha1.ComponentHealthHealthy,
		v1alpha1.ComponentHealthSuspended,
		v1alpha1.ComponentHealthProgressing,
		v1alpha1.ComponentHealthMissing,
		v1alpha1.ComponentHealthDegraded,
		v1alpha1.ComponentHealthFailed,
		v1alpha1.ComponentHealthUnknown,
	}
	for i := 1; i < len(order); i++ {
		if !catalogs.Worse(order[i], order[i-1]) || catalogs.Worse(order[i-1], order[i]) {
			t.Errorf("%s should be worse than %s", order[i], order[i-1])
		}
	}
	succeeded := v1alpha1.ComponentHealthSucceeded
	if catalogs.Worse(succeeded, v1alpha1.ComponentHealthHealthy) {
		t.Error("Succeeded is worse than Healthy")
	}
}
