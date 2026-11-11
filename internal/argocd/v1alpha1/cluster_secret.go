/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package v1alpha1

// Argo CD registers a cluster through a labelled Secret rather than a custom resource.
// These constants are the parts of that format ccluster relies on: pkg/cclclient writes
// the credentials Secret, internal/remote reads it, and the control-plane operator
// mirrors it into the Argo CD namespace. See
// https://argo-cd.readthedocs.io/en/stable/operator-manual/declarative-setup/#clusters
const (
	// SecretTypeLabel is how Argo CD discovers the Secrets it should treat as cluster
	// registrations; SecretTypeCluster is its value for those.
	SecretTypeLabel   = "argocd.argoproj.io/secret-type"
	SecretTypeCluster = "cluster"

	// ClusterSecretServerKey is the data key holding the cluster's API server URL.
	ClusterSecretServerKey = "server"
	// ClusterSecretConfigKey is the data key holding the JSON connection config: bearer
	// token, TLS settings and similar.
	ClusterSecretConfigKey = "config"

	// InClusterServer is the server URL Argo CD treats as its own cluster. A Secret
	// naming it with no credentials of its own registers the cluster Argo CD and the
	// control-plane operator run in: Argo CD connects with its own ServiceAccount. See
	// examples/quickstart/kubectl/v1alpha1_cclinfraplane.yaml.
	InClusterServer = "https://kubernetes.default.svc"
)
