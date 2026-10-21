/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package deployments

import (
	"errors"
	"fmt"
	"maps"
	"slices"
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

	// ImagePullSecretNames are Secrets in the release namespace, added to the pod's
	// imagePullSecrets after the chart's own; a name already listed is skipped. The
	// chart never creates or reads them. The kubelet reads them on each pull, so a
	// Secret refreshed in place needs no pod restart.
	ImagePullSecretNames []string `json:"imagePullSecretNames,omitempty"`

	// Resources are the container's limits and requests. Required.
	Resources ResourceRequirements `json:"resources"`

	// NodeAffinityLabels pins the pod to nodes carrying every one of these labels,
	// as a required node affinity.
	NodeAffinityLabels map[string]string `json:"nodeAffinityLabels,omitempty"`

	// EnvVars are set in the container, after the chart's own variables.
	EnvVars map[string]string `json:"envVars,omitempty"`

	// SecretEnvVars set variables in the container from keys of Secrets in the release
	// namespace, after EnvVars. The chart never creates or reads the Secrets. The
	// container does not start until each Secret has its key, and keeps the values it
	// started with. A variable may not also be in EnvVars, or be one the chart sets
	// itself.
	SecretEnvVars map[string]SecretKeyRef `json:"secretEnvVars,omitempty"`

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

	// PodLabels are added verbatim to the labels of every pod the chart creates. A key
	// the chart sets itself keeps the chart's value. They carry the caller's own
	// metadata, such as the keys its log pipeline or usage metering reads, and must
	// never carry tenant input: that belongs in UserAnnotations.
	PodLabels map[string]string `json:"podLabels,omitempty"`

	// PodAnnotations are added verbatim to the annotations of every pod the chart
	// creates, on the same terms as PodLabels.
	PodAnnotations map[string]string `json:"podAnnotations,omitempty"`
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
		ValidateImagePullSecretNames(w.ImagePullSecretNames),
		ValidateSecretEnvVars(w.SecretEnvVars, w.EnvVars),
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

// ValidateImagePullSecretNames reports empty names.
func ValidateImagePullSecretNames(names []string) error {
	var missing []string
	for i, name := range names {
		if name == "" {
			missing = append(missing, fmt.Sprintf("imagePullSecretNames[%d]", i))
		}
	}
	return MissingError(missing)
}

// SecretKeyRef selects one key of a Secret in the release namespace. Both fields are
// required.
type SecretKeyRef struct {
	// SecretName is the Secret's name.
	SecretName string `json:"secretName"`

	// Key is the key in the Secret's data.
	Key string `json:"key"`
}

// ValidateSecretEnvVars reports references without a Secret name or key, and variables
// envVars also sets: the pod would list the variable twice.
func ValidateSecretEnvVars(refs map[string]SecretKeyRef, envVars map[string]string) error {
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(refs)) {
		prefix := "secretEnvVars." + name
		errs = append(errs, MissingError(Missing(
			Field{prefix + ".secretName", refs[name].SecretName},
			Field{prefix + ".key", refs[name].Key},
		)))
		if _, ok := envVars[name]; ok {
			errs = append(errs, fmt.Errorf("%w: %s is also set in envVars", ErrInvalidValue, prefix))
		}
	}
	return errors.Join(errs...)
}

// ValidateChartEnv reports secretEnvVars entries that name a variable in chartEnv, the
// variables a chart sets itself. The container would list the variable twice, and the
// Application's server-side apply rejects a list with a repeated name.
func ValidateChartEnv(refs map[string]SecretKeyRef, chartEnv []string) error {
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(refs)) {
		if slices.Contains(chartEnv, name) {
			errs = append(errs, fmt.Errorf("%w: secretEnvVars.%s is set by the chart",
				ErrInvalidValue, name))
		}
	}
	return errors.Join(errs...)
}
