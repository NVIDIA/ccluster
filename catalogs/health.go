/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package catalogs

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
)

// healthRank orders every health the CRD admits from best to worst. It follows Argo CD's
// order and adds Succeeded next to Healthy and Failed after Degraded.
var healthRank = map[v1alpha1.ComponentHealth]int{
	v1alpha1.ComponentHealthHealthy:     0,
	v1alpha1.ComponentHealthSucceeded:   0,
	v1alpha1.ComponentHealthSuspended:   1,
	v1alpha1.ComponentHealthProgressing: 2,
	v1alpha1.ComponentHealthMissing:     3,
	v1alpha1.ComponentHealthDegraded:    4,
	v1alpha1.ComponentHealthFailed:      5,
	v1alpha1.ComponentHealthUnknown:     6,
}

// Worse reports whether a is worse than b.
func Worse(a, b v1alpha1.ComponentHealth) bool {
	return healthRank[a] > healthRank[b]
}

// ValidHealth reports whether the CRD admits health.
func ValidHealth(health v1alpha1.ComponentHealth) bool {
	_, ok := healthRank[health]
	return ok
}

// FromArgoCD converts an Argo CD health status to a ComponentHealth.
//
// Argo CD has six statuses, and each converts as it is. Argo CD has no Succeeded or
// Failed; only a chart's own Health function reports them.
func FromArgoCD(status string) v1alpha1.ComponentHealth {
	switch health := v1alpha1.ComponentHealth(status); health {
	case v1alpha1.ComponentHealthHealthy, v1alpha1.ComponentHealthProgressing, v1alpha1.ComponentHealthDegraded,
		v1alpha1.ComponentHealthSuspended, v1alpha1.ComponentHealthMissing, v1alpha1.ComponentHealthUnknown:
		return health
	default:
		// Empty before Argo CD assesses the object, or a status a later Argo CD adds.
		return v1alpha1.ComponentHealthUnknown
	}
}

// DefaultHealth judges a component whose chart has no Health function of its own.
// It starts from Argo CD's health of the Application, then lets the Pods correct and
// explain it. It reads at most one Pod and that Pod's events.
func DefaultHealth(ctx context.Context, _ v1alpha1.ComponentEntry, observed Observation) Health {
	health := fromApplication(observed)
	pods := workloadPods(observed.Resources)

	// A Degraded Pod makes the component Degraded: Argo CD keeps a Deployment
	// Progressing until its progress deadline passes. The Pod also gives a better
	// reason, such as OOMKilled. Unknown stays, because it ranks worse than Degraded.
	if health.Status != v1alpha1.ComponentHealthUnknown {
		if degraded := pods.WithHealth(v1alpha1.ComponentHealthDegraded); len(degraded) > 0 {
			reason, message := ExplainPod(ctx, observed, degraded[0])
			return Health{Status: v1alpha1.ComponentHealthDegraded, Reason: reason, Message: message}
		}
	}

	// The first Pod that is not running explains a Progressing component.
	if health.Status == v1alpha1.ComponentHealthProgressing {
		if i := slices.IndexFunc(pods, isWaiting); i >= 0 {
			health.Reason, health.Message = ExplainPod(ctx, observed, pods[i])
		}
	}
	return health
}

// fromApplication returns Argo CD's health of the Application.
//
// The Application's health has no message, so it borrows one from a rendered object with
// the same health, which is what set it: the first such object in kind and name order
// that has a message, or else the first one.
func fromApplication(observed Observation) Health {
	health := Health{Status: FromArgoCD(observed.ApplicationHealth)}
	if health.Status == v1alpha1.ComponentHealthHealthy {
		return health
	}

	var causes ObservedResources
	for _, r := range observed.Resources {
		if r.Rendered() && r.Health == observed.ApplicationHealth {
			causes = append(causes, r)
		}
	}
	if len(causes) == 0 {
		return health
	}
	slices.SortFunc(causes, func(a, b ObservedResource) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Name, b.Name))
	})

	cause := causes[0]
	if i := slices.IndexFunc(causes, func(r ObservedResource) bool { return r.HealthMessage != "" }); i >= 0 {
		cause = causes[i]
	}
	if cause.HealthMessage == "" {
		health.Message = fmt.Sprintf("%s %s is %s", cause.Kind, cause.Name, cause.Health)
	} else {
		health.Message = fmt.Sprintf("%s %s: %s", cause.Kind, cause.Name, cause.HealthMessage)
	}
	return health
}

// workloadPods returns the Pods that controllers created, except:
//   - Pods a Job owns: a Job retries a failed Pod, so job_deployment judges those.
//   - Pods whose owner Argo CD reports Healthy: the owner has the replicas it wants, so a
//     leftover Pod, such as an evicted one that a ReplicaSet does not delete, says nothing.
//
// They are sorted by name, so the Pod that explains the component is always the same.
func workloadPods(resources ObservedResources) ObservedResources {
	var pods ObservedResources
	for _, r := range resources.OfKind("", "Pod") {
		ownedByJob := slices.ContainsFunc(r.Parents, func(p ResourceRef) bool {
			return p.Group == "batch" && p.Kind == "Job"
		})
		if !r.Rendered() && !ownedByJob && !ownerHealthy(resources, r) {
			pods = append(pods, r)
		}
	}
	slices.SortFunc(pods, func(a, b ObservedResource) int { return cmp.Compare(a.Name, b.Name) })
	return pods
}

// ownerHealthy reports whether Argo CD reports an owner of the Pod Healthy.
func ownerHealthy(resources ObservedResources, pod ObservedResource) bool {
	for _, parent := range pod.Parents {
		for _, r := range resources {
			if refersTo(parent, r.ResourceRef) && r.Health == string(v1alpha1.ComponentHealthHealthy) {
				return true
			}
		}
	}
	return false
}

// isWaiting reports whether a Pod has not started running.
func isWaiting(pod ObservedResource) bool {
	return pod.Health == string(v1alpha1.ComponentHealthProgressing) && pod.Info[InfoStatusReason] != "Running"
}

// ExplainPod returns a reason and a message for a Pod that is not running as it should.
// The tree has only kubectl's STATUS column, so ExplainPod looks for the cause in the Pod
// (PodScheduled, or a container's last exit such as OOMKilled), then in the Pod's latest
// warning event (such as FailedMount), then falls back to the tree. A failed read is
// skipped.
func ExplainPod(ctx context.Context, observed Observation, pod ObservedResource) (reason, message string) {
	reason, message = pod.Info[InfoStatusReason], pod.HealthMessage

	if p := readPod(ctx, observed, pod.ResourceRef); p != nil {
		if r, m := podReason(p); r != "" {
			reason, message = r, m
		}
	}
	if message == "" {
		if events, err := observed.Reader.Events(ctx, pod.ResourceRef); err == nil {
			if event := latestWarning(events); event != nil {
				reason, message = event.Reason, event.Message
			}
		}
	}

	if message == "" {
		return reason, fmt.Sprintf("Pod %s is %s", pod.Name, reason)
	}
	return reason, fmt.Sprintf("Pod %s: %s", pod.Name, message)
}

// readPod reads a live Pod. It returns nil if the read fails or the Pod is gone.
func readPod(ctx context.Context, observed Observation, ref ResourceRef) *corev1.Pod {
	obj, err := observed.Reader.Get(ctx, ref)
	if err != nil || obj == nil {
		return nil
	}
	var pod corev1.Pod
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &pod); err != nil {
		return nil
	}
	return &pod
}

// podReason reads why a Pod is not running from the Pod itself. It checks the
// PodScheduled condition first, then the last exit of each container that is not ready.
func podReason(pod *corev1.Pod) (reason, message string) {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason != "" {
			return c.Reason, c.Message
		}
	}
	for _, c := range pod.Status.ContainerStatuses {
		last := c.LastTerminationState.Terminated
		if c.Ready || last == nil {
			continue
		}
		reason = last.Reason
		message = fmt.Sprintf("container %q last exited with code %d (%s)", c.Name, last.ExitCode, last.Reason)
		if c.State.Waiting != nil && c.State.Waiting.Reason != "" {
			message += ", now " + c.State.Waiting.Reason
			// OOMKilled says more than CrashLoopBackOff, so it stays the reason.
			if last.Reason != "OOMKilled" {
				reason = c.State.Waiting.Reason
			}
		}
		return reason, message
	}
	return "", ""
}

// latestWarning returns the latest warning event, or nil if there is none.
func latestWarning(events []corev1.Event) *corev1.Event {
	var latest *corev1.Event
	for i := range events {
		event := &events[i]
		if event.Type != corev1.EventTypeWarning {
			continue
		}
		if latest == nil || eventTime(event).After(eventTime(latest)) {
			latest = event
		}
	}
	return latest
}

// eventTime is when an event was last seen. A core event sets lastTimestamp. An event
// from events.k8s.io sets eventTime, and its series sets lastObservedTime.
func eventTime(event *corev1.Event) time.Time {
	t := event.LastTimestamp.Time
	if event.EventTime.After(t) {
		t = event.EventTime.Time
	}
	if event.Series != nil && event.Series.LastObservedTime.After(t) {
		t = event.Series.LastObservedTime.Time
	}
	return t
}
