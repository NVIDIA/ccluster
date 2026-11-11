/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package v1 contains a partial mirror of cert-manager's cert-manager.io/v1 API, covering
// only the ClusterIssuer fields the infra-plane operator reads.
//
// cert-manager owns the type and its CRD, which catalogs/cluster_base/cert_manager
// installs. As with internal/argocd/v1alpha1, importing github.com/cert-manager/cert-manager
// would pull in its controllers' dependencies, and unstructured.Unstructured would cost
// compile-time checking of the field names. It lives under internal/ rather than api/ so
// that `make manifests` generates no CRD for it.
//
// +kubebuilder:object:generate=true
// +groupName=cert-manager.io
package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is group version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: "cert-manager.io", Version: "v1"}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion, &ClusterIssuer{}, &ClusterIssuerList{})
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}
