/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package v1alpha1 contains a partial mirror of external-dns's externaldns.k8s.io/v1alpha1
// API, covering only the DNSEndpoint fields the control-plane operator writes or reads.
//
// external-dns owns the type and its CRD, which the external-dns dependency of the cp
// chart installs. As with internal/api/argocd/v1alpha1, importing sigs.k8s.io/external-dns
// would pull in every DNS provider's SDK, and unstructured.Unstructured would cost
// compile-time checking of the field names. It lives under internal/api/ rather than api/ for
// the same reason too: `make manifests` would otherwise generate a CRD the operator's own
// chart would then install.
//
// +kubebuilder:object:generate=true
// +groupName=externaldns.k8s.io
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is group version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: "externaldns.k8s.io", Version: "v1alpha1"}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion, &DNSEndpoint{}, &DNSEndpointList{})
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}
