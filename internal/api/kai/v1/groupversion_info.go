/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package v1 contains a partial mirror of the KAI scheduler's kai.scheduler/v1 API,
// covering only the Config fields the infra-plane operator reads.
//
// KAI owns the type and its CRD, which catalogs/cluster_base/kai_scheduler installs. As
// with internal/api/certmanager/v1, importing github.com/NVIDIA/KAI-Scheduler would pull
// in the scheduler's dependencies, and unstructured.Unstructured would cost compile-time
// checking of the field names. It lives under internal/api/ rather than api/ so that no
// generator of ./api/... picks it up: `make manifests` would otherwise generate a CRD the
// operator's own chart would then install.
//
// +kubebuilder:object:generate=true
// +groupName=kai.scheduler
package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is group version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: "kai.scheduler", Version: "v1"}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion, &Config{}, &ConfigList{})
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}
