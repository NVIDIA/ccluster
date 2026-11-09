/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package catalogs

import (
	"context"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
)

// This file holds the input and output of a chart's Health function. See
// healthregistry.HealthFunc for the contract and health.go for DefaultHealth.

// ResourceRef identifies one object of a deployed component.
type ResourceRef struct {
	Group     string
	Version   string
	Kind      string
	Namespace string
	Name      string
	UID       string
}

// ObservedResource is one node of an Application's resource tree in Argo CD.
// The tree holds the objects the chart rendered and the objects controllers created
// from them, such as a Deployment's ReplicaSets and Pods.
type ObservedResource struct {
	ResourceRef

	// Parents are the owners of the object. A rendered object has none.
	Parents []ResourceRef

	// Health is Argo CD's health of the object: Healthy, Progressing, Degraded,
	// Suspended, Missing or Unknown. It is empty for a kind with no health check.
	Health string
	// HealthMessage is Argo CD's reason for Health. It is often empty.
	HealthMessage string

	// Info is extra data Argo CD shows for the object, by name.
	// For a Pod it holds InfoStatusReason and InfoRestartCount.
	Info map[string]string
}

// Names of the Info entries Argo CD sets on a Pod.
const (
	// InfoStatusReason is the STATUS column of kubectl get pod, such as Running,
	// Pending, ContainerCreating or CrashLoopBackOff.
	InfoStatusReason = "Status Reason"
	// InfoRestartCount is the number of container restarts. It is absent at zero.
	InfoRestartCount = "Restart Count"
)

// Rendered reports whether the chart rendered the object.
// A controller-created object, such as a Pod, has parents and is not rendered.
func (r ObservedResource) Rendered() bool {
	return len(r.Parents) == 0
}

// ObservedResources is a component's resource tree, in Argo CD's order.
type ObservedResources []ObservedResource

// OfKind returns the resources of one group and kind, in tree order.
func (o ObservedResources) OfKind(group, kind string) ObservedResources {
	var out ObservedResources
	for _, r := range o {
		if r.Group == group && r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

// WithHealth returns the resources Argo CD reports with health, in tree order.
func (o ObservedResources) WithHealth(health v1alpha1.ComponentHealth) ObservedResources {
	var out ObservedResources
	for _, r := range o {
		if r.Health == string(health) {
			out = append(out, r)
		}
	}
	return out
}

// ChildrenOf returns the resources that parent owns, in tree order.
func (o ObservedResources) ChildrenOf(parent ResourceRef) ObservedResources {
	var out ObservedResources
	for _, r := range o {
		if slices.ContainsFunc(r.Parents, func(p ResourceRef) bool { return refersTo(p, parent) }) {
			out = append(out, r)
		}
	}
	return out
}

// refersTo reports whether ref, an owner reference from the tree, names obj. It compares
// UIDs when ref has one.
func refersTo(ref, obj ResourceRef) bool {
	if ref.UID != "" {
		return ref.UID == obj.UID
	}
	return ref.Group == obj.Group && ref.Kind == obj.Kind &&
		ref.Namespace == obj.Namespace && ref.Name == obj.Name
}

// ResourceReader reads live objects that the tree does not describe in full.
// Each read is a request to the infra-plane cluster, so read only the few objects
// the tree points at.
type ResourceReader interface {
	// Get returns the live object. It returns nil, nil if the object is gone.
	Get(ctx context.Context, ref ResourceRef) (*unstructured.Unstructured, error)
	// Events returns the events recorded for the object. It returns none if the object
	// is gone.
	Events(ctx context.Context, ref ResourceRef) ([]corev1.Event, error)
}

// Observation is the input of a chart's Health function.
type Observation struct {
	// ApplicationHealth is the health Argo CD reports on the component's Application.
	// It is the worst health of the rendered objects, not counting hooks or kinds with
	// no health check. It is empty before Argo CD first assesses the Application.
	ApplicationHealth string

	// Resources is the Application's resource tree.
	Resources ObservedResources

	// Reader reads live objects. The operator always sets it.
	Reader ResourceReader
}

// Health is the output of a chart's Health function. The operator writes it to
// status.componentHealthStatuses and gates the rollout on it; see
// healthregistry.HealthFunc.
type Health struct {
	Status v1alpha1.ComponentHealth
	// Reason is a CamelCase word, such as CrashLoopBackOff. It is empty when Argo CD's
	// health is the only explanation.
	Reason string
	// Message is one short sentence for users.
	Message string
}
