/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package jobdeployment

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
)

// The reasons Health sets. A failed Job reports Kubernetes' reason, such as
// BackoffLimitExceeded, and ReasonFailed only when that cannot be read.
const (
	ReasonRunning  = "Running"
	ReasonComplete = "Complete"
	ReasonFailed   = "Failed"
)

// Health judges a job by its Job. Argo CD's health does not fit a Job: it says
// Progressing while the Job runs and Healthy only when it ends, but the next component
// may start as soon as the Job runs. So Health returns Healthy while a Pod runs,
// Succeeded or Failed once the Job ends, and Progressing while no Pod runs.
func Health(ctx context.Context, entry v1alpha1.ComponentEntry, observed catalogs.Observation) catalogs.Health {
	i := slices.IndexFunc(observed.Resources, func(r catalogs.ObservedResource) bool {
		return r.Group == "batch" && r.Kind == "Job" && r.Rendered()
	})
	if i < 0 {
		return catalogs.DefaultHealth(ctx, entry, observed)
	}
	job := observed.Resources[i]

	// The Job has ended, or is suspended.
	switch catalogs.FromArgoCD(job.Health) {
	case v1alpha1.ComponentHealthHealthy:
		// Succeeded, not Healthy, keeps a finished job apart from a running one.
		return catalogs.Health{
			Status:  v1alpha1.ComponentHealthSucceeded,
			Reason:  ReasonComplete,
			Message: fmt.Sprintf("Job %s completed", job.Name),
		}
	case v1alpha1.ComponentHealthDegraded:
		// Failed, not Degraded: a failed Job does not recover.
		reason := ReasonFailed
		if r := failedReason(ctx, observed, job.ResourceRef); r != "" {
			reason = r
		}
		return catalogs.Health{
			Status:  v1alpha1.ComponentHealthFailed,
			Reason:  reason,
			Message: fmt.Sprintf("Job %s failed: %s", job.Name, job.HealthMessage),
		}
	case v1alpha1.ComponentHealthSuspended:
		return catalogs.Health{
			Status:  v1alpha1.ComponentHealthSuspended,
			Message: fmt.Sprintf("Job %s is suspended", job.Name),
		}
	}

	// The Job has not ended. It is healthy if one of its Pods runs. Argo CD calls a
	// running Job Pod Progressing, because its restartPolicy is Never, so read the Pod's
	// status reason instead.
	pods := observed.Resources.ChildrenOf(job.ResourceRef).OfKind("", "Pod")
	slices.SortFunc(pods, func(a, b catalogs.ObservedResource) int { return cmp.Compare(a.Name, b.Name) })
	if slices.ContainsFunc(pods, func(p catalogs.ObservedResource) bool {
		return p.Info[catalogs.InfoStatusReason] == string(corev1.PodRunning)
	}) {
		return catalogs.Health{
			Status:  v1alpha1.ComponentHealthHealthy,
			Reason:  ReasonRunning,
			Message: fmt.Sprintf("Job %s is running", job.Name),
		}
	}

	health := catalogs.Health{
		Status:  v1alpha1.ComponentHealthProgressing,
		Message: fmt.Sprintf("Job %s has no running Pod yet", job.Name),
	}
	// No Pod runs. A Pod that waits to start gives the reason. If no Pod waits, a failed
	// Pod gives the reason the Job retries.
	explain := pods.WithHealth(v1alpha1.ComponentHealthProgressing)
	if len(explain) == 0 {
		explain = pods.WithHealth(v1alpha1.ComponentHealthDegraded)
	}
	if len(explain) > 0 {
		health.Reason, health.Message = catalogs.ExplainPod(ctx, observed, explain[0])
	}
	return health
}

// failedReason returns the reason Kubernetes gave up on the Job, such as
// BackoffLimitExceeded or DeadlineExceeded. It returns "" if it cannot read the Job.
func failedReason(ctx context.Context, observed catalogs.Observation, ref catalogs.ResourceRef) string {
	obj, err := observed.Reader.Get(ctx, ref)
	if err != nil || obj == nil {
		return ""
	}
	var job batchv1.Job
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &job); err != nil {
		return ""
	}
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			return c.Reason
		}
	}
	return ""
}
