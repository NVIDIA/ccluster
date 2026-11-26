/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SchedulingShard is one KAI scheduler instance: the KAI operator deploys a scheduler for
// each shard, over the nodes, queues and pod groups whose node-pool label matches the
// shard's partitionLabelValue, or that have none when it is empty. The KAI chart renders
// one, "default", with an empty partitionLabelValue.
//
// Partial and read-only: it declares no spec, so writing a SchedulingShard read into this
// type back would delete its configuration. The infra-plane operator only reads its
// status.
//
// See https://github.com/NVIDIA/KAI-Scheduler/blob/main/pkg/apis/kai/v1/schedulingshard_types.go
// for the full API.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
type SchedulingShard struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Status SchedulingShardStatus `json:"status,omitempty"`
}

// SchedulingShardStatus is what the KAI operator reports back on a SchedulingShard.
type SchedulingShardStatus struct {
	// conditions describe the state of the shard's scheduler Deployment.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// SchedulingShardList contains a list of SchedulingShard.
//
// +kubebuilder:object:root=true
type SchedulingShardList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SchedulingShard `json:"items"`
}
