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
	"k8s.io/apimachinery/pkg/util/intstr"
)

// ResourcesFinalizer makes deleting an Argo CD Application also delete the resources it
// deployed. Argo CD treats an Application without it as a record to drop rather than a
// deployment to tear down, so removing one silently orphans its workloads on the
// destination cluster.
const ResourcesFinalizer = "resources-finalizer.argocd.argoproj.io"

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

	// goTemplateOptions are passed to text/template. The operator sets
	// "missingkey=error" so a placeholder with no matching generator key fails the
	// render instead of silently producing an empty string.
	GoTemplateOptions []string `json:"goTemplateOptions,omitempty"`

	// strategy orders the rollout of the generated Applications. Nil means Argo CD
	// syncs them all at once.
	Strategy *ApplicationSetStrategy `json:"strategy,omitempty"`

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
//
// Every field is a string because these are template inputs: Argo CD renders them into
// an Application manifest with text/template, and a non-string would have to be
// stringified there anyway.
type GeneratorElement struct {
	// name identifies the component and is templated into the Application's name.
	Name string `json:"name"`

	// path is the component's chart directory, relative to the catalog repository
	// root. The repository and revision themselves are operator-level configuration
	// and live in the template, not here.
	Path string `json:"path,omitempty"`

	// infraPlane is the CCLInfraPlane name, which is also the Argo CD cluster name the
	// generated Application is destined for.
	InfraPlane string `json:"infraPlane,omitempty"`

	// namespace is where the chart is installed on that cluster.
	Namespace string `json:"namespace,omitempty"`

	// wave is the RollingSync step this component belongs to. It is templated into a
	// label on the generated Application, which is what the step's matchExpressions
	// select on.
	Wave string `json:"wave,omitempty"`

	// values is the component's Helm values, marshalled to single-line JSON. JSON is
	// valid YAML, so it can be templated straight into helm.values, which is a string.
	// Components with no overrides get "{}" rather than an omitted field: a Go
	// template cannot conditionally drop a key.
	Values string `json:"values,omitempty"`
}

// ApplicationSetStrategy orders the rollout of generated Applications.
//
// This is what orders one Application against another. The more familiar
// "argocd.argoproj.io/sync-wave" annotation cannot: it orders resources *within* a
// single Application.
//
// Argo CD gates this behind a flag. The applicationset-controller only honours it when
// ARGOCD_APPLICATIONSET_CONTROLLER_ENABLE_PROGRESSIVE_SYNCS is set, which the ccluster-cp
// chart does via the argocd-cmd-params-cm key
// "applicationsetcontroller.enable.progressive.syncs". With the flag off this field is
// silently ignored and every Application syncs at once.
type ApplicationSetStrategy struct {
	// type is the strategy name. Only "RollingSync" is modelled.
	Type string `json:"type,omitempty"`

	RollingSync *ApplicationSetRolloutStrategy `json:"rollingSync,omitempty"`
}

// ApplicationSetRolloutStrategy is the ordered list of RollingSync steps.
type ApplicationSetRolloutStrategy struct {
	// steps run in order. Argo CD does not begin a step until every Application
	// selected by the previous one reports Healthy - and it applies no timeout, so a
	// component that never becomes Healthy stalls the remaining steps indefinitely.
	Steps []ApplicationSetRolloutStep `json:"steps,omitempty"`
}

// ApplicationSetRolloutStep is one step of a RollingSync.
type ApplicationSetRolloutStep struct {
	// matchExpressions select the generated Applications this step covers, by label.
	MatchExpressions []ApplicationMatchExpression `json:"matchExpressions,omitempty"`

	// maxUpdate caps how many of them Argo CD syncs concurrently.
	MaxUpdate *intstr.IntOrString `json:"maxUpdate,omitempty"`
}

// ApplicationMatchExpression is a label selector requirement over generated
// Applications. It mirrors metav1.LabelSelectorRequirement, which Argo CD does not reuse
// here.
type ApplicationMatchExpression struct {
	Key      string   `json:"key,omitempty"`
	Operator string   `json:"operator,omitempty"`
	Values   []string `json:"values,omitempty"`
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

	// labels are stamped onto each generated Application. RollingSync steps select on
	// them, which is how a component's wave reaches the strategy.
	Labels map[string]string `json:"labels,omitempty"`

	// finalizers are stamped onto each generated Application. Deleting an Argo CD
	// Application is non-cascading unless it carries
	// ResourcesFinalizer: the Application record goes away and everything it
	// installed keeps running, unmanaged.
	Finalizers []string `json:"finalizers,omitempty"`
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

	// helm carries per-Application chart overrides.
	Helm *ApplicationSourceHelm `json:"helm,omitempty"`
}

// ApplicationSourceHelm holds Helm-specific overrides for an Application's source.
type ApplicationSourceHelm struct {
	// values is a YAML document of overrides, as a string. Argo CD also offers
	// valuesObject, which takes structured YAML; a string is used here because the
	// value is templated in from a generator element, and templating a string is
	// unambiguous where templating a nested object is not.
	Values string `json:"values,omitempty"`
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
