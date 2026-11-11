/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ConfigName is the name of the one Config the KAI operator reconciles; it ignores a
// Config under any other name.
const ConfigName = "kai-config"

// DefaultSchedulerName is the scheduler name KAI uses when a Config sets none: the KAI
// operator starts every scheduler with it as --scheduler-name.
const DefaultSchedulerName = "kai-scheduler"

// Config is the cluster-wide KAI configuration, from which the KAI operator deploys the
// scheduler, binder, admission webhook and the rest.
//
// Partial and read-only: it declares only the fields the infra-plane operator reads, so
// writing a Config read into this type back would delete the rest of its spec.
//
// See https://github.com/NVIDIA/KAI-Scheduler/blob/main/pkg/apis/kai/v1/config_types.go
// for the full API.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
type Config struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ConfigSpec `json:"spec,omitempty"`
}

// ConfigSpec is the part of a Config's spec the infra-plane operator reads.
type ConfigSpec struct {
	// global holds the settings every KAI component shares.
	Global *GlobalConfig `json:"global,omitempty"`
}

// GlobalConfig is the part of a Config's spec.global the infra-plane operator reads.
type GlobalConfig struct {
	// schedulerName is the name pods set in spec.schedulerName to be scheduled by KAI.
	// Unset means DefaultSchedulerName.
	SchedulerName *string `json:"schedulerName,omitempty"`
}

// ConfigList contains a list of Config.
//
// +kubebuilder:object:root=true
type ConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Config `json:"items"`
}
