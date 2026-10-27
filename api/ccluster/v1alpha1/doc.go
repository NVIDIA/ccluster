/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// The package markers live in doc.go because client-gen, lister-gen and informer-gen read
// the groupName marker from no other file; without it they would take the group to be
// ccluster, the directory's name, rather than ccluster.nvidia.com. They also parse every
// comment line here that starts with a plus sign.

// Package v1alpha1 contains API Schema definitions for the v1alpha1 API group.
// +kubebuilder:object:generate=true
// +groupName=ccluster.nvidia.com
package v1alpha1
