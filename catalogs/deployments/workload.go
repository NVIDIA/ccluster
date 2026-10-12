/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package deployments

import (
	"errors"
	"fmt"
)

// Workload is the pod-level configuration shared by the inference, compute and job
// charts. Each chart embeds it at the top level of its Values, so on the wire these
// are the chart's own keys.
type Workload struct {
	// Name names the objects the chart creates, and is the Rollout, Deployment or Job
	// name. Required.
	Name string `json:"name"`

	// DeploymentID identifies the deployment across revisions. It labels every object
	// the chart creates as platform.centml.ai/deployment-id and is the pod selector,
	// so it must be stable for the deployment's lifetime and unique in the namespace.
	// Required.
	DeploymentID string `json:"deploymentId"`

	// ImageURL is the container image to run. Required.
	ImageURL string `json:"imageUrl"`

	// ImagePullSecret, when set, creates a dockerconfigjson Secret for ImageURL's
	// registry and adds it to the pod's imagePullSecrets, next to the namespace's
	// ecr-image-pull secret.
	ImagePullSecret *ImagePullSecret `json:"imagePullSecret,omitempty"`

	// Resources are the container's limits and requests. Required.
	Resources ResourceRequirements `json:"resources"`

	// NodeAffinityLabels pins the pod to nodes carrying every one of these labels,
	// as a required node affinity.
	NodeAffinityLabels map[string]string `json:"nodeAffinityLabels,omitempty"`

	// EnvVars are set in the container, after the chart's own variables.
	EnvVars map[string]string `json:"envVars,omitempty"`

	// DevShmSize is the size of the /dev/shm tmpfs, as a Kubernetes quantity. The
	// chart ships 512Mi.
	DevShmSize *string `json:"devShmSize,omitempty"`

	// SchedulerName is the pod's scheduler. The chart ships kai-scheduler, which
	// bin-packs GPUs; an explicit "" falls back to the cluster's default scheduler.
	SchedulerName *string `json:"schedulerName,omitempty"`

	// KaiQueue is the KAI scheduler queue the pods are labelled kai.scheduler/queue
	// with, one of the leaf queues catalogs/resources/kai_gpu_queues creates. Unset
	// leaves the pods on the scheduler's default queue.
	KaiQueue *string `json:"kaiQueue,omitempty"`

	// PriorityClassName is the pod's PriorityClass. Unset leaves it to the cluster.
	PriorityClassName *string `json:"priorityClassName,omitempty"`

	// EnableLogging ships the container's logs to the cluster's log pipeline. The
	// chart ships true; false marks the pod platform.centml.ai/log-exclude.
	EnableLogging *bool `json:"enableLogging,omitempty"`

	// UserAnnotations are copied onto the workload and its pods as both labels and
	// annotations, each key prefixed ccluster.nvidia.com/user-.
	UserAnnotations map[string]string `json:"userAnnotations,omitempty"`
}

// Validate reports the required values that are unset and the nested values that are
// malformed.
func (w Workload) Validate() error {
	return errors.Join(
		MissingError(Missing(
			Field{"name", w.Name},
			Field{"deploymentId", w.DeploymentID},
			Field{"imageUrl", w.ImageURL},
		)),
		w.Resources.Validate(),
		w.ImagePullSecret.Validate(),
	)
}

// ResourceRequirements are a container's limits and requests, both required.
type ResourceRequirements struct {
	Limits   Resources `json:"limits"`
	Requests Resources `json:"requests"`
}

// Validate reports the required quantities that are unset.
func (r ResourceRequirements) Validate() error {
	return errors.Join(
		MissingError(Missing(
			Field{"resources.limits.cpu", r.Limits.CPU},
			Field{"resources.limits.memory", r.Limits.Memory},
			Field{"resources.requests.cpu", r.Requests.CPU},
			Field{"resources.requests.memory", r.Requests.Memory},
		)),
		r.Limits.validate("resources.limits"),
		r.Requests.validate("resources.requests"),
	)
}

// Resources are one side of a container's resource requirements. CPU and memory are
// Kubernetes quantities ("500m", "2", "4Gi"); accelerators are an extended resource.
type Resources struct {
	// CPU is the CPU quantity. Required.
	CPU string `json:"cpu"`

	// Memory is the memory quantity. Required.
	Memory string `json:"memory"`

	// AcceleratorKey is the extended resource name of the accelerator. The chart
	// ships nvidia.com/gpu.
	AcceleratorKey *string `json:"acceleratorKey,omitempty"`

	// AcceleratorCount is the number of accelerators. Unset or zero requests none;
	// Kubernetes requires limits and requests of an extended resource to agree.
	AcceleratorCount *int32 `json:"acceleratorCount,omitempty"`
}

func (r Resources) validate(prefix string) error {
	if r.AcceleratorCount != nil && *r.AcceleratorCount < 0 {
		return fmt.Errorf("%w: %s.acceleratorCount %d is negative",
			ErrInvalidValue, prefix, *r.AcceleratorCount)
	}
	return nil
}

// VolumeMount mounts an existing PersistentVolumeClaim into the container. The chart
// does not own the claim; catalogs/resources/persistent_volume creates one.
type VolumeMount struct {
	// Name is the claim's name in the release namespace. Required.
	Name string `json:"name"`

	// MountPath is the absolute directory it is mounted at. Required.
	MountPath string `json:"mountPath"`

	// ReadOnly mounts it read-only in this container. The chart ships false.
	ReadOnly *bool `json:"readOnly,omitempty"`
}

// ValidateVolumeMounts reports mounts missing a claim name or a mount path, and
// mount paths that are not absolute.
func ValidateVolumeMounts(mounts []VolumeMount) error {
	var errs []error
	for i, m := range mounts {
		prefix := fmt.Sprintf("volumeMounts[%d]", i)
		errs = append(errs, MissingError(Missing(
			Field{prefix + ".name", m.Name},
			Field{prefix + ".mountPath", m.MountPath},
		)))
		if m.MountPath != "" && m.MountPath[0] != '/' {
			errs = append(errs, fmt.Errorf("%w: %s.mountPath %q is not absolute",
				ErrInvalidValue, prefix, m.MountPath))
		}
	}
	return errors.Join(errs...)
}

// ImagePullSecret is a registry credential the chart turns into a dockerconfigjson
// Secret. All three fields are required.
type ImagePullSecret struct {
	// Server is the registry host, as it appears in the image URL.
	Server string `json:"server"`

	// Username is the registry user.
	Username string `json:"username"`

	// Password is the registry password or token.
	Password string `json:"password"`
}

// Validate reports the fields that are unset. A nil receiver is valid: the secret is
// optional.
func (s *ImagePullSecret) Validate() error {
	if s == nil {
		return nil
	}
	return MissingError(Missing(
		Field{"imagePullSecret.server", s.Server},
		Field{"imagePullSecret.username", s.Username},
		Field{"imagePullSecret.password", s.Password},
	))
}
