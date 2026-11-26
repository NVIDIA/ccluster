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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
	"github.com/NVIDIA/ccluster/catalogs/deployments/deploymentstest"
)

// The health Argo CD reports for a resource, as catalogs.ObservedResource.Health holds it.
const (
	healthy     = string(v1alpha1.ComponentHealthHealthy)
	progressing = string(v1alpha1.ComponentHealthProgressing)
	degraded    = string(v1alpha1.ComponentHealthDegraded)
)

const (
	// podKey is how the reader addresses the Pod web-1-a.
	podKey = "Pod/web-1-a"
	// appContainer is the app container of the Pods the tests build.
	appContainer = "main"

	crashLoopBackOff = "CrashLoopBackOff"
	oomKilled        = "OOMKilled"
)

var (
	deployment = catalogs.ObservedResource{
		ResourceRef:   catalogs.ResourceRef{Group: appsv1.GroupName, Kind: "Deployment", Name: "web", UID: "d1"},
		Health:        progressing,
		HealthMessage: "Waiting for rollout to finish: 0 of 1 updated replicas are available...",
	}
	replicaSet = catalogs.ResourceRef{Group: appsv1.GroupName, Kind: "ReplicaSet", Name: "web-1", UID: "r1"}
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
			Name:  appContainer,
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: crashLoopBackOff}},
			LastTerminationState: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{Reason: lastReason, ExitCode: exitCode},
			},
		}},
	}})
}

func crashLoopingSidecar(t *testing.T) map[string]any {
	return deploymentstest.Object(t, &corev1.Pod{Status: corev1.PodStatus{
		InitContainerStatuses: []corev1.ContainerStatus{{
			Name:  "istio-proxy",
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: crashLoopBackOff}},
			LastTerminationState: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{Reason: oomKilled, ExitCode: 137},
			},
		}},
		ContainerStatuses: []corev1.ContainerStatus{{
			Name: appContainer, Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		}},
	}})
}

func crashLoopingAfterInitRetry(t *testing.T) map[string]any {
	return deploymentstest.Object(t, &corev1.Pod{Status: corev1.PodStatus{
		InitContainerStatuses: []corev1.ContainerStatus{{
			Name:  "istio-validation",
			Ready: true,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed"}},
			LastTerminationState: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1},
			},
		}},
		ContainerStatuses: []corev1.ContainerStatus{{
			Name:  appContainer,
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: crashLoopBackOff}},
			LastTerminationState: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{Reason: oomKilled, ExitCode: 137},
			},
		}},
	}})
}

func unschedulable(t *testing.T) map[string]any {
	return deploymentstest.Object(t, &corev1.Pod{Status: corev1.PodStatus{
		Phase: corev1.PodPending,
		Conditions: []corev1.PodCondition{{
			Type: corev1.PodScheduled, Status: corev1.ConditionFalse,
			Reason: corev1.PodReasonUnschedulable, Message: "0/3 nodes are available: 3 Insufficient nvidia.com/gpu.",
		}},
	}})
}

func TestDefaultHealth(t *testing.T) {
	crashing := pod("web-1-a", replicaSet, degraded, crashLoopBackOff)
	crashing.HealthMessage = "back-off 5m0s restarting failed container=main"
	// Argo CD judges a running Pod by its app containers, so a crashing sidecar leaves it Progressing.
	sidecarCrashing := pod("web-1-a", replicaSet, progressing, "Init:CrashLoopBackOff")
	pending := pod("web-1-a", replicaSet, progressing, "Pending")
	creating := pod("web-1-a", replicaSet, progressing, "ContainerCreating")
	failedJobPod := pod("run-a", job, degraded, "Error")
	evicted := pod("web-1-b", replicaSet, degraded, "Evicted")
	evicted.HealthMessage = "The node was low on resource: memory."
	oldReplicaSet := catalogs.ResourceRef{Group: appsv1.GroupName, Kind: "ReplicaSet", Name: "web-0", UID: "r0"}
	terminating := pod("web-0-a", oldReplicaSet, progressing, "Terminating")
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
			aggregate:  healthy,
			resources:  catalogs.ObservedResources{{ResourceRef: deployment.ResourceRef, Health: healthy}},
			wantHealth: v1alpha1.ComponentHealthHealthy,
		},
		{
			name:        "progressing names the rendered object",
			aggregate:   progressing,
			resources:   catalogs.ObservedResources{deployment},
			wantHealth:  v1alpha1.ComponentHealthProgressing,
			wantMessage: "Deployment web: Waiting for rollout to finish",
		},
		{
			name:      "an object with no message is named with its health",
			aggregate: progressing,
			resources: catalogs.ObservedResources{
				{ResourceRef: catalogs.ResourceRef{Version: "v1", Kind: "Service", Name: "web"}, Health: progressing},
				{ResourceRef: deployment.ResourceRef, Health: healthy},
			},
			wantHealth:  v1alpha1.ComponentHealthProgressing,
			wantMessage: "Service web is Progressing",
		},
		{
			name:      "a crashing Pod degrades a progressing Deployment, OOMKilled read from the Pod",
			aggregate: progressing,
			resources: catalogs.ObservedResources{deployment, crashing},
			reader: &deploymentstest.Reader{Objects: map[string]map[string]any{
				podKey: crashLooping(t, oomKilled, 137),
			}},
			wantHealth:  v1alpha1.ComponentHealthDegraded,
			wantReason:  oomKilled,
			wantMessage: `Pod web-1-a: container "main" last exited with code 137 (OOMKilled), now CrashLoopBackOff`,
			maxReads:    1,
		},
		{
			name:      "a crashing native sidecar degrades the component, OOMKilled read from its init container",
			aggregate: progressing,
			resources: catalogs.ObservedResources{deployment, sidecarCrashing},
			reader: &deploymentstest.Reader{Objects: map[string]map[string]any{
				podKey: crashLoopingSidecar(t),
			}},
			wantHealth:  v1alpha1.ComponentHealthDegraded,
			wantReason:  oomKilled,
			wantMessage: `Pod web-1-a: container "istio-proxy" last exited with code 137 (OOMKilled), now CrashLoopBackOff`,
			maxReads:    1,
		},
		{
			name:      "a finished init container does not explain a crashing app container",
			aggregate: progressing,
			resources: catalogs.ObservedResources{deployment, crashing},
			reader: &deploymentstest.Reader{Objects: map[string]map[string]any{
				podKey: crashLoopingAfterInitRetry(t),
			}},
			wantHealth:  v1alpha1.ComponentHealthDegraded,
			wantReason:  oomKilled,
			wantMessage: `Pod web-1-a: container "main" last exited with code 137 (OOMKilled), now CrashLoopBackOff`,
			maxReads:    1,
		},
		{
			name:      "a crashing Pod degrades what Argo CD calls healthy",
			aggregate: healthy,
			resources: catalogs.ObservedResources{{ResourceRef: deployment.ResourceRef, Health: healthy}, crashing},
			reader: &deploymentstest.Reader{Objects: map[string]map[string]any{
				podKey: crashLooping(t, "Error", 1),
			}},
			wantHealth: v1alpha1.ComponentHealthDegraded,
			wantReason: crashLoopBackOff,
			maxReads:   1,
		},
		{
			name:      "a crashing Pod explains a component Argo CD already calls Degraded",
			aggregate: degraded,
			resources: catalogs.ObservedResources{
				{
					ResourceRef:   deployment.ResourceRef,
					Health:        degraded,
					HealthMessage: `Deployment "web" exceeded its progress deadline`,
				},
				crashing,
			},
			reader: &deploymentstest.Reader{Objects: map[string]map[string]any{
				podKey: crashLooping(t, oomKilled, 137),
			}},
			wantHealth: v1alpha1.ComponentHealthDegraded,
			wantReason: oomKilled,
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
			aggregate:   progressing,
			resources:   catalogs.ObservedResources{deployment, crashing},
			reader:      &deploymentstest.Reader{Err: errors.New("unreachable")},
			wantHealth:  v1alpha1.ComponentHealthDegraded,
			wantReason:  crashLoopBackOff,
			wantMessage: "Pod web-1-a: back-off 5m0s restarting failed container=main",
			maxReads:    1,
		},
		{
			name:      "a Pending Pod explains a progressing component",
			aggregate: progressing,
			resources: catalogs.ObservedResources{deployment, pending},
			reader: &deploymentstest.Reader{Objects: map[string]map[string]any{
				podKey: unschedulable(t),
			}},
			wantHealth:  v1alpha1.ComponentHealthProgressing,
			wantReason:  corev1.PodReasonUnschedulable,
			wantMessage: "Pod web-1-a: 0/3 nodes are available: 3 Insufficient nvidia.com/gpu.",
			maxReads:    1,
		},
		{
			name:      "events explain a Pod the Pod itself does not",
			aggregate: progressing,
			resources: catalogs.ObservedResources{deployment, creating},
			reader: &deploymentstest.Reader{
				Objects: map[string]map[string]any{podKey: deploymentstest.Object(t, &corev1.Pod{})},
				EventsOf: map[string][]corev1.Event{podKey: {
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
			aggregate: healthy,
			resources: catalogs.ObservedResources{
				{ResourceRef: deployment.ResourceRef, Health: healthy},
				ownedByDeployment(replicaSet, healthy),
				evicted,
			},
			wantHealth: v1alpha1.ComponentHealthHealthy,
		},
		{
			name:      "a Pod of a Healthy old ReplicaSet does not explain a rollout",
			aggregate: progressing,
			resources: catalogs.ObservedResources{
				deployment,
				ownedByDeployment(oldReplicaSet, healthy),
				terminating,
				ownedByDeployment(replicaSet, progressing),
				pending,
			},
			reader: &deploymentstest.Reader{Objects: map[string]map[string]any{
				podKey: unschedulable(t),
			}},
			wantHealth: v1alpha1.ComponentHealthProgressing,
			wantReason: corev1.PodReasonUnschedulable,
			maxReads:   1,
		},
		{
			name:       "a Job's failed Pod is the Job's to judge",
			aggregate:  progressing,
			resources:  catalogs.ObservedResources{{ResourceRef: job, Health: progressing}, failedJobPod},
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
