/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AppProject is an Argo CD AppProject: the policy boundary a set of Applications and
// clusters belong to. The control-plane operator creates one per ccluster namespace.
//
// Partial, with the same caveat as Application: only the fields the operator writes are
// modelled, and writing an AppProject read into this type back **deletes** every field not
// declared here. The operator owns the AppProjects it creates outright and rewrites their
// spec on every reconcile; the ownership check in internal/controller/cp keeps that
// confined to them.
//
// There is no Status: Argo CD's AppProject CRD has no status subresource, and the operator
// reads nothing back from a project.
//
// See https://argo-cd.readthedocs.io/en/stable/user-guide/projects/ for the full API.
//
// +kubebuilder:object:root=true
type AppProject struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec AppProjectSpec `json:"spec"`
}

// AppProjectSpec is the policy an AppProject enforces on its Applications.
type AppProjectSpec struct {
	// sourceRepos is the set of repository URLs Applications in the project may deploy
	// from. Argo CD rejects an Application whose source repository matches none of them.
	SourceRepos []string `json:"sourceRepos,omitempty"`

	// sourceNamespaces is the set of namespaces, outside Argo CD's own, in which an
	// Application may live and still reference this project. It is the project-side half
	// of Argo CD's "Applications in any namespace" feature; the controller-side half is
	// the application.namespaces parameter the cp chart sets. Argo CD's own namespace is
	// always permitted and must not be listed.
	SourceNamespaces []string `json:"sourceNamespaces,omitempty"`

	// destinations are the clusters and namespaces Applications may deploy to.
	Destinations []ApplicationDestination `json:"destinations,omitempty"`

	// clusterResourceWhitelist lists the cluster-scoped kinds Applications may create.
	// Argo CD denies every cluster-scoped resource when it is empty.
	ClusterResourceWhitelist []metav1.GroupKind `json:"clusterResourceWhitelist,omitempty"`

	// permitOnlyProjectScopedClusters restricts destinations to clusters whose Secret
	// carries a "project" data key naming this project, over and above `destinations`.
	PermitOnlyProjectScopedClusters bool `json:"permitOnlyProjectScopedClusters,omitempty"`
}

// AppProjectList contains a list of AppProject.
//
// +kubebuilder:object:root=true
type AppProjectList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []AppProject `json:"items"`
}
