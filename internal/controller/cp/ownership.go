/*
Copyright 2026 NVIDIA CORPORATION & AFFILIATES.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
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

// The key pairs in use. They apply to disjoint kinds - a CCLInfraPlane only ever stamps
// the mirrored cluster Secret, and a CCLDeployment only ever stamps its ApplicationSet -
// so one object never carries both, and ownedBy does not have to tell them apart.
var (
	infraPlaneOwner = ownerAnnotations{
		nameKey:      annotationInfraPlaneName,
		namespaceKey: annotationInfraPlaneNamespace,
	}

	deploymentOwner = ownerAnnotations{
		nameKey:      annotationDeploymentName,
		namespaceKey: annotationDeploymentNamespace,
	}
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
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[labelName] = labelNameValue
	labels[labelManagedBy] = labelManagedByValue
	obj.SetLabels(labels)

	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[a.nameKey] = owner.GetName()
	annotations[a.namespaceKey] = owner.GetNamespace()
	obj.SetAnnotations(annotations)
}
