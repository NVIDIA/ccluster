/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package v1alpha1 contains a partial mirror of Argo CD's argoproj.io/v1alpha1 API,
// covering only what the ccluster operators write: the Application and AppProject
// fields the control-plane operator sets or reads.
//
// Argo CD owns these types and the CRDs behind them. This package exists because the
// alternatives are worse: importing github.com/argoproj/argo-cd pulls in a module that
// declares its own Go toolchain and pins every k8s.io/* dependency to versions this
// project does not use, while addressing the resources as unstructured.Unstructured
// costs compile-time checking of every field name.
//
// It lives under internal/api/ rather than api/ deliberately: `make manifests` runs
// controller-gen's CRD generator over ./api/... only, and a CRD generated from these
// types would be installed by the ccluster-cp chart - making the operator own Argo CD's
// CRDs, which it must never do.
//
// +kubebuilder:object:generate=true
// +groupName=argoproj.io
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is group version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: "argoproj.io", Version: "v1alpha1"}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion,
		&Application{}, &ApplicationList{},
		&AppProject{}, &AppProjectList{},
	)
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}
