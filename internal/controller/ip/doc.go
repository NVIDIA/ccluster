/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package ip holds the controllers owned by ccluster-ip-operator, which runs
// in an infra-plane cluster. It is empty for now; the operator is a
// placeholder. Reconcilers added here must be wired into
// cmd/ccluster-ip-operator/main.go by hand, and their +kubebuilder:rbac
// markers drive config/ip/rbac/role.yaml.
package ip
