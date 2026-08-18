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

// ApplicationSet generates Argo CD Applications from a set of generators.
//
// Only the fields the control-plane operator writes are modelled, so this type is not a
// faithful copy of Argo CD's: it has no status, and its spec omits most of what Argo CD
// accepts. That is safe only because the operator owns the ApplicationSets it creates
// outright and rewrites their spec on every reconcile - reading one of these into this
// type and writing it back **deletes** any field not declared below. Do not use it to
// modify an ApplicationSet the operator did not create.
//
// See https://argo-cd.readthedocs.io/en/stable/operator-manual/applicationset/ for the
// full API.
//
// +kubebuilder:object:root=true
type ApplicationSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ApplicationSetSpec `json:"spec,omitempty"`
}

// ApplicationSetSpec is the desired state of an ApplicationSet: what to generate, and
// what to generate it from.
type ApplicationSetSpec struct {
	// goTemplate selects Go text/template syntax ({{.name}}) for the placeholders in
	// template, over Argo CD's older fasttemplate syntax.
	GoTemplate bool `json:"goTemplate,omitempty"`

	// generators produce the parameter sets template is rendered once per.
	Generators []ApplicationSetGenerator `json:"generators"`

	// template is the Application rendered for each generated parameter set.
	Template ApplicationSetTemplate `json:"template"`
}

// ApplicationSetGenerator is one source of parameter sets. Argo CD offers many kinds -
// cluster, git, matrix and so on; only the list generator is modelled here.
type ApplicationSetGenerator struct {
	List *ListGenerator `json:"list,omitempty"`
}

// ListGenerator yields one parameter set per literal element.
type ListGenerator struct {
	Elements []GeneratorElement `json:"elements"`
}

// GeneratorElement is one element of a ListGenerator. Argo CD declares these as
// arbitrary JSON objects; the operator authors them, so the shape it writes is typed.
type GeneratorElement struct {
	Name string `json:"name"`
}

// ApplicationSetTemplate is the Application an ApplicationSet generates. Its metadata is
// not an ObjectMeta: Argo CD accepts only a subset of the fields there.
type ApplicationSetTemplate struct {
	ApplicationSetTemplateMeta `json:"metadata"`

	Spec ApplicationSpec `json:"spec"`
}

// ApplicationSetTemplateMeta is the metadata Argo CD stamps onto a generated Application.
type ApplicationSetTemplateMeta struct {
	Name string `json:"name,omitempty"`
}

// ApplicationSpec is the desired state of a generated Argo CD Application.
type ApplicationSpec struct {
	// project is the Argo CD AppProject the Application belongs to.
	Project string `json:"project"`

	// source is where the Application's manifests come from.
	Source ApplicationSource `json:"source"`

	// destination is the cluster and namespace they are applied to.
	Destination ApplicationDestination `json:"destination"`

	SyncPolicy *SyncPolicy `json:"syncPolicy,omitempty"`
}

// ApplicationSource locates a chart in a Git repository. Argo CD also supports Helm and
// OCI repositories, which need different fields and are not modelled.
type ApplicationSource struct {
	// repoURL is the Git repository Argo CD's repo-server clones.
	RepoURL string `json:"repoURL"`

	// targetRevision is a branch, tag or commit SHA within it.
	TargetRevision string `json:"targetRevision,omitempty"`

	// path is the chart's directory, relative to the repository root.
	Path string `json:"path,omitempty"`
}

// ApplicationDestination is where a generated Application is deployed.
type ApplicationDestination struct {
	// name is the cluster's name as Argo CD knows it, which is what the operator's
	// mirrored cluster Secret defines. Argo CD also accepts a "server" URL instead;
	// addressing the cluster by name keeps the CCLInfraPlane's name the only identifier
	// the operator has to agree with itself on.
	Name string `json:"name,omitempty"`

	// namespace is the namespace on that cluster.
	Namespace string `json:"namespace,omitempty"`
}

// SyncPolicy controls when and how Argo CD applies an Application.
type SyncPolicy struct {
	// automated enables syncing without human approval. Nil means manual sync only.
	Automated *SyncPolicyAutomated `json:"automated,omitempty"`

	// syncOptions are Argo CD's per-Application sync flags, e.g. "CreateNamespace=true".
	SyncOptions []string `json:"syncOptions,omitempty"`
}

// SyncPolicyAutomated configures automated syncing.
type SyncPolicyAutomated struct {
	// prune deletes resources that have disappeared from the source.
	Prune bool `json:"prune,omitempty"`

	// selfHeal reverts changes made to live resources outside Argo CD.
	SelfHeal bool `json:"selfHeal,omitempty"`
}

// ApplicationSetList contains a list of ApplicationSet.
//
// +kubebuilder:object:root=true
type ApplicationSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []ApplicationSet `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ApplicationSet{}, &ApplicationSetList{})
}
