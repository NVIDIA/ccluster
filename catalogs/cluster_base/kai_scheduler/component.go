/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package kaischeduler describes the values of catalogs/cluster_base/kai_scheduler: the
// KAI scheduler that the deployments charts name as their schedulerName by default, and
// whose Queue CRD catalogs/resources/kai_gpu_queues needs. Install it before either.
package kaischeduler

import (
	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
)

const (
	// Name is the component's name in a CCLComponentSet, and the key its upstream
	// values nest under.
	Name = "kai-scheduler"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/kai_scheduler"
)

// Placement strategies for Scheduler.PlacementStrategy.
const (
	// PlacementBinpack fills a node's GPUs before using another. The chart's default.
	PlacementBinpack = "binpack"
	// PlacementSpread places pods on the least used nodes.
	PlacementSpread = "spread"
)

// GPUPodRuntimeClassNVIDIA is the RuntimeClass gpu-operator creates. Set
// Admission.GPUPodRuntimeClassName to it on clusters where every GPU node's containerd
// has an "nvidia" runtime handler, as gpu-operator's container toolkit configures when
// it is enabled.
const GPUPodRuntimeClassNVIDIA = "nvidia"

// Values is the top-level values document for catalogs/cluster_base/kai_scheduler.
type Values struct {
	KaiScheduler *ChartValues `json:"kai-scheduler,omitempty"`

	// DefaultPriorities is the ConfigMap of per-kind preemptibility defaults. The
	// chart ships it enabled, marking Jobs and ReplicaSets preemptible. KAI does not
	// read it yet: the kai-config Config CR must name it, and the upstream chart
	// cannot.
	DefaultPriorities *DefaultPriorities `json:"defaultPriorities,omitempty"`
}

// ChartValues are the upstream kai-scheduler values worth setting per cluster.
//
// Scheduler.Resources, Binder and Admission reach the KAI components through the
// kai-config Config CR, which the upstream chart applies from a hook Job rather than as
// an Application resource. Argo CD does not diff hooks, so the chart also renders a
// ConfigMap holding a checksum of these values: changing them marks the Application
// OutOfSync, and the sync reruns the hook. Scheduler.PlacementStrategy goes to the
// SchedulingShard, Operator to the KAI operator Deployment and DefaultQueue to the
// default Queues, all ordinary Application resources.
type ChartValues struct {
	Scheduler *Scheduler `json:"scheduler,omitempty"`
	Binder    *Workload  `json:"binder,omitempty"`
	Operator  *Operator  `json:"operator,omitempty"`
	Admission *Admission `json:"admission,omitempty"`

	// DefaultQueue is the queue that pods without a kai.scheduler/queue label join.
	DefaultQueue *DefaultQueue `json:"defaultQueue,omitempty"`
}

// Scheduler configures the scheduler.
type Scheduler struct {
	// PlacementStrategy is how GPU and CPU pods are placed: PlacementBinpack or
	// PlacementSpread. The chart ships binpack.
	PlacementStrategy *string `json:"placementStrategy,omitempty"`

	// Resources are the scheduler container's requests and limits. The chart ships
	// 500m CPU and 1.5Gi memory requested, limited to 3 CPUs and 3Gi.
	Resources *ResourceRequirements `json:"resources,omitempty"`
}

// Workload is a KAI component's resources.
type Workload struct {
	// Resources are the container's requests and limits.
	Resources *ResourceRequirements `json:"resources,omitempty"`
}

// Operator configures the KAI operator, which deploys every other KAI component.
type Operator struct {
	// LogLevel is the controller-runtime log level: debug, info, error or panic. The
	// chart ships error.
	LogLevel *string `json:"logLevel,omitempty"`

	// Resources are the operator container's requests and limits.
	Resources *ResourceRequirements `json:"resources,omitempty"`
}

// Admission configures KAI's admission webhook.
type Admission struct {
	// GPUPodRuntimeClassName is the RuntimeClass the webhook sets on GPU pods that
	// name none; "" turns that off. The chart ships "": gpu-operator creates the
	// class, but this catalog runs it with its container toolkit off, so a node's
	// containerd may have no "nvidia" runtime handler, and a GPU pod given the class
	// there would not start. Set GPUPodRuntimeClassNVIDIA where every GPU node has
	// one.
	GPUPodRuntimeClassName *string `json:"gpuPodRuntimeClassName,omitempty"`
}

// DefaultQueue configures the queue for unlabelled pods.
type DefaultQueue struct {
	// CreateDefaultQueue creates the queue and its parent, both with unlimited quota.
	// The chart ships it on, so that pods without a kai.scheduler/queue label schedule.
	// Their fair share is then the whole cluster, so KAI does not reclaim what they
	// hold for other queues.
	CreateDefaultQueue *bool `json:"createDefaultQueue,omitempty"`
}

// ResourceRequirements are a container's requests and limits.
type ResourceRequirements struct {
	Requests *ResourceList `json:"requests,omitempty"`
	Limits   *ResourceList `json:"limits,omitempty"`
}

// ResourceList is one side of ResourceRequirements, as Kubernetes quantities.
type ResourceList struct {
	CPU    *string `json:"cpu,omitempty"`
	Memory *string `json:"memory,omitempty"`
}

// DefaultPriorities configures the per-kind preemptibility ConfigMap.
type DefaultPriorities struct {
	// Enabled renders the ConfigMap. The chart ships it on.
	Enabled *bool `json:"enabled,omitempty"`
}

// New returns the kai_scheduler chart entry, installed into namespace with values.
// The zero Values means "chart defaults".
func New(
	namespace string,
	values Values,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	return catalogs.Entry(Chart, namespace, values)
}
