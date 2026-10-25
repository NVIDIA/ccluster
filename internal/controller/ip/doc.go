/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package ip holds the controllers owned by ccluster-ip-operator, which runs in an
// infra-plane cluster. Reconcilers here are wired into cmd/ccluster-ip-operator/main.go
// by hand, and their +kubebuilder:rbac markers drive config/ip/rbac/role.yaml.
//
// The operator's job is verification: it runs every catalogs/cluster_base component's
// Verifier against the CCLClusterConfig singleton and reports the outcome there. It also
// reports the cluster's ingress LoadBalancer there, for the control plane's DNS. It never
// installs, upgrades or deletes anything.
package ip
