/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package catalogs

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
)

// ResourceRef identifies an object in a deployed component.
type ResourceRef struct {
	Group     string
	Version   string
	Kind      string
	Namespace string
	Name      string
	UID       string
}

// ObservedResource is one object of a deployed component as the control plane last
// observed it. It covers the objects the chart rendered and the objects controllers
// created from them in turn, such as a Deployment's ReplicaSets and Pods.
type ObservedResource struct {
	ResourceRef

	// Parents are the objects this one is owned by. A rendered object has none.
	Parents []ResourceRef

	// Health is Argo CD's assessment of the object: Healthy, Progressing, Degraded,
	// Suspended, Missing or Unknown. Empty for a kind Argo CD has no health check for.
	Health string
	// HealthMessage explains Health, when Argo CD gives a reason.
	HealthMessage string

	// Manifest is the live object. Nil if it no longer existed when it was read, as
	// happens to a Pod replaced since the component was observed.
	Manifest *unstructured.Unstructured

	// Events are the Kubernetes events recorded for the object.
	Events []corev1.Event
}

// ObservedResources is everything observed about one deployed component at one moment.
type ObservedResources []ObservedResource

// OfKind returns the resources of the given group and kind, in observed order.
func (o ObservedResources) OfKind(group, kind string) ObservedResources {
	var out ObservedResources
	for _, r := range o {
		if r.Group == group && r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

// ChildrenOf returns the resources owned by parent, in observed order.
func (o ObservedResources) ChildrenOf(parent ResourceRef) ObservedResources {
	var out ObservedResources
	for _, r := range o {
		for _, p := range r.Parents {
			if p.UID != "" && p.UID == parent.UID ||
				p.UID == "" && p.Group == parent.Group && p.Kind == parent.Kind &&
					p.Namespace == parent.Namespace && p.Name == parent.Name {
				out = append(out, r)
				break
			}
		}
	}
	return out
}

// Status is a component's state as its chart reports it: what a chart package's Status
// function returns. The control-plane operator writes it into the CCLComponentSet's
// status.componentStatuses.
type Status struct {
	State cclusternvidiacomv1alpha1.ComponentState
	// Reason is a CamelCase, machine-readable refinement of State, such as
	// CrashLoopBackOff.
	Reason string
	// Message is a short explanation of State, shown to users.
	Message string
}
