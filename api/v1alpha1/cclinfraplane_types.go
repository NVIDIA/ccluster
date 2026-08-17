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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// Keys expected in the Secret referenced by CCLInfraPlaneSpec.ClusterCredentialsRef.
const (
	// CredentialsKeyServer is the API server URL of the infra-plane cluster.
	CredentialsKeyServer = "server"
	// CredentialsKeyBearerToken is the bearer token used to authenticate against it.
	CredentialsKeyBearerToken = "bearer_token"
	// CredentialsKeyCACert is the PEM-encoded CA certificate that signs its serving cert.
	CredentialsKeyCACert = "ca_cert"
)

// LocalSecretReference references a Secret in the same namespace as the referring object.
type LocalSecretReference struct {
	// name is the name of the Secret.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

// CCLInfraPlaneSpec defines the desired state of CCLInfraPlane
type CCLInfraPlaneSpec struct {
	// clusterCredentialsRef references a Secret in the same namespace as this
	// CCLInfraPlane that holds the credentials for the infra-plane cluster's API
	// server. The Secret must contain the keys "server", "bearer_token" and
	// "ca_cert".
	// +required
	ClusterCredentialsRef LocalSecretReference `json:"clusterCredentialsRef"`
}

// CCLInfraPlaneStatus defines the observed state of CCLInfraPlane.
type CCLInfraPlaneStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	// conditions represent the current state of the CCLInfraPlane resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// observedGeneration is the .metadata.generation the conditions were computed from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Available",type=string,JSONPath=`.status.conditions[?(@.type=="Available")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// CCLInfraPlane is the Schema for the cclinfraplanes API
type CCLInfraPlane struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of CCLInfraPlane
	// +required
	Spec CCLInfraPlaneSpec `json:"spec"`

	// status defines the observed state of CCLInfraPlane
	// +optional
	Status CCLInfraPlaneStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// CCLInfraPlaneList contains a list of CCLInfraPlane
type CCLInfraPlaneList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []CCLInfraPlane `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CCLInfraPlane{}, &CCLInfraPlaneList{})
}
