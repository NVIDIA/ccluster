/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package apiclient

// ApplicationRef names the Application a request reads.
//
// Every request carries the Application's namespace and project as well as its name.
// The operator writes Applications outside the Argo CD namespace ("Applications in any
// namespace"), so argocd-server cannot find one by name alone. And naming the project
// makes argocd-server answer an Application it cannot find with 404 rather than 403,
// which is what lets ErrApplicationNotFound be told apart from ErrUnauthorized.
type ApplicationRef struct {
	Namespace string
	Name      string
	Project   string
}

// ResourceRef identifies one object in an Application's resource tree. Its fields are
// the ones argocd-server takes back to address that object.
type ResourceRef struct {
	Group     string `json:"group,omitempty"`
	Version   string `json:"version,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
	UID       string `json:"uid,omitempty"`
}

// ResourceTree is a partial mirror of Argo CD's ApplicationTree: every object an
// Application deployed, and the objects those created in turn, such as a Deployment's
// ReplicaSets and Pods. argocd-server serves it from the application controller's cache
// of the destination cluster, not from the Application object, which records only the
// objects the chart rendered.
type ResourceTree struct {
	Nodes []ResourceNode `json:"nodes,omitempty"`
}

// ResourceNode is one object in a ResourceTree.
type ResourceNode struct {
	ResourceRef `json:",inline"`

	// ParentRefs are the objects this one is owned by. A rendered object has none.
	ParentRefs []ResourceRef `json:"parentRefs,omitempty"`

	// Health is Argo CD's assessment of the object. Nil for a kind Argo CD has no health
	// check for.
	Health *HealthStatus `json:"health,omitempty"`
}

// HealthStatus is Argo CD's assessment of one object's health.
type HealthStatus struct {
	// Status is Healthy, Progressing, Degraded, Suspended, Missing or Unknown.
	Status  string `json:"status,omitempty"`
	Message string `json:"message,omitempty"`
}

// resourceResponse is argocd-server's answer to a request for one object: the live
// object, as a JSON document inside a JSON string.
type resourceResponse struct {
	Manifest *string `json:"manifest,omitempty"`
}

// errorResponse is the body grpc-gateway writes for a failed request.
type errorResponse struct {
	Message string `json:"message"`
	Error   string `json:"error"`
}
