/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

import (
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Labels stamped on everything the control-plane operator creates, so an operator-owned
// object is recognisable without reading its annotations.
const (
	labelName      = "app.kubernetes.io/name"
	labelManagedBy = "app.kubernetes.io/managed-by"

	labelNameValue      = "ccluster"
	labelManagedByValue = "ccluster-cp-operator"
)

// ownerAnnotations is a pair of annotation keys that record which custom resource an
// object in the Argo CD namespace was created for.
//
// Owner references cannot do this job: they may not cross namespaces, and the garbage
// collector deletes objects whose owner it cannot find in their own namespace. Every
// custom resource kind that projects into the Argo CD namespace therefore declares its
// own key pair, and both the finalizer and the reconcile path consult it so neither ever
// touches an object belonging to a different resource.
type ownerAnnotations struct {
	nameKey      string
	namespaceKey string
}

// The owners in use. Each kind is stamped by exactly one of them - a mirrored cluster
// Secret by infraPlaneOwner, an Application by componentSetOwner, an AppProject by
// projectOwner - so one object never carries two, and ownedBy does not have to tell
// them apart.
var (
	infraPlaneOwner = ownerAnnotations{
		nameKey:      annotationInfraPlaneName,
		namespaceKey: annotationInfraPlaneNamespace,
	}

	componentSetOwner = ownerAnnotations{
		nameKey:      annotationComponentSetName,
		namespaceKey: annotationComponentSetNamespace,
	}

	projectOwner = namespaceOwner{key: annotationInfraPlaneNamespace}
)

// ownedBy reports whether obj is unclaimed or already back-references owner.
//
// An object created by hand carries no back-reference and is therefore adoptable; one
// pointing at a different resource is not. The asymmetry is deliberate: adopting an
// unclaimed object lets an operator recover from a partial write, while refusing a
// claimed one is what stops two same-named resources in different namespaces from
// fighting over a single object in the Argo CD namespace.
func (a ownerAnnotations) ownedBy(obj client.Object, owner client.Object) bool {
	annotations := obj.GetAnnotations()
	name, hasName := annotations[a.nameKey]
	namespace, hasNamespace := annotations[a.namespaceKey]
	if !hasName && !hasNamespace {
		return true
	}
	return name == owner.GetName() && namespace == owner.GetNamespace()
}

// stamp writes the back-reference annotations and the operator's labels onto obj. Call
// it only after ownedBy has agreed the object is ours to write.
func (a ownerAnnotations) stamp(obj client.Object, owner client.Object) {
	stampLabels(obj)

	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[a.nameKey] = owner.GetName()
	annotations[a.namespaceKey] = owner.GetNamespace()
	obj.SetAnnotations(annotations)
}

// namespaceOwner is a single annotation key recording which ccluster namespace an object
// in the Argo CD namespace was created for. It is for objects shared by every custom
// resource in that namespace - the AppProject - where naming one resource as the owner
// would make the second CCLInfraPlane in the namespace look like a conflict.
type namespaceOwner struct {
	key string
}

// ownedBy reports whether obj is unclaimed or already back-references namespace. The
// adopt-if-unclaimed rule is the same as ownerAnnotations.ownedBy, and for the same
// reason: it is what lets the operator take over Argo CD's built-in "default" project
// when the CCLInfraPlanes live in the "default" namespace.
func (o namespaceOwner) ownedBy(obj client.Object, namespace string) bool {
	claimed, has := obj.GetAnnotations()[o.key]
	return !has || claimed == namespace
}

// stamp writes the back-reference annotation and the operator's labels onto obj.
func (o namespaceOwner) stamp(obj client.Object, namespace string) {
	stampLabels(obj)

	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[o.key] = namespace
	obj.SetAnnotations(annotations)
}

// stampLabels writes the operator's labels onto obj, allocating the map if needed.
func stampLabels(obj client.Object) {
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[labelName] = labelNameValue
	labels[labelManagedBy] = labelManagedByValue
	obj.SetLabels(labels)
}
