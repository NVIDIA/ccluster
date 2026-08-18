/*
Copyright 2026 NVIDIA CORPORATION & AFFILIATES.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package v1alpha1 contains a partial mirror of Argo CD's argoproj.io/v1alpha1 API,
// covering only what the ccluster operators write.
//
// Argo CD owns these types and the CRDs behind them. This package exists because the
// alternatives are worse: importing github.com/argoproj/argo-cd pulls in a module that
// declares its own Go toolchain and pins every k8s.io/* dependency to versions this
// project does not use, while addressing the resources as unstructured.Unstructured
// costs compile-time checking of every field name.
//
// It lives under internal/ rather than api/ deliberately: `make manifests` runs
// controller-gen's CRD generator over ./api/... only, and a CRD generated from these
// types would be installed by the ccluster-cp chart - making the operator own Argo CD's
// CRDs, which it must never do.
//
// +kubebuilder:object:generate=true
// +groupName=argoproj.io
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is group version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: "argoproj.io", Version: "v1alpha1"}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)
