/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package v1alpha1

// LocalObjectReference references an object in the same namespace as the referring
// object. The kind of the target is fixed by the field that uses it.
// +kubebuilder:title=LocalObjectReference
type LocalObjectReference struct {
	// name is the name of the referenced object.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}
