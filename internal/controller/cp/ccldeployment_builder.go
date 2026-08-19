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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
	argocdv1alpha1 "github.com/NVIDIA/ccluster/internal/argocd/v1alpha1"
)

// errApplicationSetConflict reports that the ApplicationSet name is already taken, for
// the same reason errClusterSecretConflict exists: two resources of the same name in
// different namespaces map onto one object in the Argo CD namespace.
var errApplicationSetConflict = errors.New("ApplicationSet belongs to another resource")

const (
	// emptyValues is what a component with no overrides contributes to helm.values.
	// It cannot simply be omitted: the field is filled by a Go template, and a
	// template can substitute a key but not decide whether the key exists.
	emptyValues = "{}"

	// defaultProject is the Argo CD AppProject generated Applications belong to.
	defaultProject = "default"
)

// applicationSetSpec builds the ApplicationSet that deploys deployment's components.
//
// Only the first `released` components appear in the generator. That slice is how
// ordering is enforced: Argo CD generates an Application for a component only once the
// operator has released it, and releaseCount releases the next one when everything
// already released is healthy.
//
// Argo CD's own RollingSync strategy would be the obvious way to do this, and it is not
// used deliberately. It gates Applications by setting spec.syncPolicy.automated.enabled
// to false, and as of Argo CD v3.5.1 it never clears that flag once the rollout reports
// complete - which leaves automated sync and selfHeal switched off for good. Drift then
// goes unrepaired and edits to a CCLDeployment never reach the cluster. Gating on which
// Applications exist keeps their sync policy untouched.
func (r *CCLDeploymentReconciler) applicationSetSpec(
	deployment *cclusternvidiacomv1alpha1.CCLDeployment,
	released int,
) (argocdv1alpha1.ApplicationSetSpec, error) {
	components := deployment.Spec.Components[:released]
	elements := make([]argocdv1alpha1.GeneratorElement, 0, len(components))

	for i, component := range components {
		values, err := componentValues(component)
		if err != nil {
			return argocdv1alpha1.ApplicationSetSpec{}, fmt.Errorf(
				"component %q: %w", component.Name, err)
		}

		elements = append(elements, argocdv1alpha1.GeneratorElement{
			Name:       component.Name,
			Path:       component.Chart,
			InfraPlane: component.InfraPlaneRef.Name,
			Namespace:  component.Namespace,
			// Position in the rollout. Nothing acts on it - it is stamped as a
			// label so `kubectl get applications -L ccluster.nvidia.com/wave`
			// shows the order the operator is working through.
			Wave:   strconv.Itoa(i + 1),
			Values: values,
		})
	}

	return argocdv1alpha1.ApplicationSetSpec{
		GoTemplate:        true,
		GoTemplateOptions: []string{"missingkey=error"},
		Generators: []argocdv1alpha1.ApplicationSetGenerator{{
			List: &argocdv1alpha1.ListGenerator{Elements: elements},
		}},
		Template: argocdv1alpha1.ApplicationSetTemplate{
			ApplicationSetTemplateMeta: argocdv1alpha1.ApplicationSetTemplateMeta{
				Name: deployment.Name + "-{{.name}}",
				// Without this an Application delete is non-cascading, so
				// dropping a component from the spec - or deleting the whole
				// CCLDeployment - would remove Argo CD's record of the chart and
				// leave it installed and unmanaged on the infra-plane cluster.
				//
				// Teardown is asynchronous: deleting the ApplicationSet returns
				// as soon as the API server accepts it, while Argo CD works
				// through the finalizers afterwards. The CCLDeployment is
				// therefore gone before the charts are, and an Application whose
				// cluster is unreachable stays Terminating until it is not.
				Finalizers: []string{argocdv1alpha1.ResourcesFinalizer},
				Labels: map[string]string{
					labelName:      labelNameValue,
					labelManagedBy: labelManagedByValue,
					// How a generated Application is traced back to the
					// CCLDeployment that asked for it. Argo CD copies only
					// template.metadata onto the Application, so the
					// back-reference annotations on the ApplicationSet itself do
					// not reach it.
					labelDeploymentName:      deployment.Name,
					labelDeploymentNamespace: deployment.Namespace,
					labelWave:                "{{.wave}}",
				},
			},
			Spec: argocdv1alpha1.ApplicationSpec{
				Project: defaultProject,
				Source: argocdv1alpha1.ApplicationSource{
					// Repository and revision are operator configuration, so they
					// are fixed here rather than templated per element.
					RepoURL:        r.CatalogRepoURL,
					TargetRevision: r.CatalogRevision,
					// path, not chart: the catalog is a Git repository, and
					// source.chart would make Argo CD look for a chart of that
					// name in a Helm registry instead.
					Path: "{{.path}}",
					Helm: &argocdv1alpha1.ApplicationSourceHelm{
						Values: "{{.values}}",
					},
				},
				Destination: argocdv1alpha1.ApplicationDestination{
					// By name, not by server: the name is the CCLInfraPlane's,
					// which its mirrored cluster Secret defines.
					Name:      "{{.infraPlane}}",
					Namespace: "{{.namespace}}",
				},
				SyncPolicy: &argocdv1alpha1.SyncPolicy{
					// Left entirely to Argo CD. The operator decides *whether* a
					// component is deployed, never *how* it syncs - which is what
					// keeps selfHeal working.
					Automated: &argocdv1alpha1.SyncPolicyAutomated{
						Prune:    true,
						SelfHeal: true,
					},
					// ServerSideApply is not optional for this catalog. Argo CD's
					// default client-side apply records the manifest in the
					// kubectl.kubernetes.io/last-applied-configuration annotation
					// and diffs against it, but an annotation cannot hold a large
					// object - argo-rollouts' rollouts.argoproj.io CRD is ~600KB -
					// so the copy is truncated and the diff never converges. The
					// resource then sits OutOfSync forever despite syncing
					// successfully. Several cluster_base charts ship CRDs that big.
					SyncOptions: []string{"CreateNamespace=true", "ServerSideApply=true"},
				},
			},
		},
	}, nil
}

// applicationName is the name Argo CD gives the Application generated for a component.
func applicationName(deploymentName, componentName string) string {
	return deploymentName + "-" + componentName
}

// healthy reports whether an Application has finished converging.
func healthy(application *argocdv1alpha1.Application) bool {
	return application.Status.Health.Status == argocdv1alpha1.HealthStatusHealthy &&
		application.Status.Sync.Status == argocdv1alpha1.SyncStatusSynced
}

// releaseCount decides how many of deployment's components may be deployed right now.
//
// Releasing is monotonic and derived entirely from which Applications exist, so it needs
// no state of its own and survives an operator restart. A component that has been
// released is never withdrawn just because it became unhealthy - that would delete a
// running Application, and with the cascade finalizer, uninstall its chart.
func releaseCount(
	deployment *cclusternvidiacomv1alpha1.CCLDeployment,
	applications map[string]*argocdv1alpha1.Application,
) int {
	components := deployment.Spec.Components

	// Already released: the leading run of components that have an Application.
	released := 0
	for i := range components {
		if _, generated := applications[applicationName(deployment.Name, components[i].Name)]; !generated {
			break
		}
		released = i + 1
	}
	if released == 0 {
		// Nothing deployed yet. The first component is always releasable; there is
		// nothing for it to wait on.
		return 1
	}

	// Hold if anything released is still converging. Checking all of them rather than
	// just the most recent means a component that degrades later stops the rollout,
	// which is what a sequential dependency implies.
	for i := 0; i < released; i++ {
		application := applications[applicationName(deployment.Name, components[i].Name)]
		if !healthy(application) {
			return released
		}
	}

	if released < len(components) {
		released++
	}
	return released
}

// componentValues renders a component's values as the single-line JSON string that goes
// into helm.values.
//
// helm.values takes a YAML document as a string, and JSON is valid YAML, so no
// conversion is needed. It is compacted onto one line because the alternative - a block
// scalar - is indentation-sensitive, and a multi-line value templated into one would
// produce a manifest that no longer parses.
func componentValues(component cclusternvidiacomv1alpha1.ComponentSpec) (string, error) {
	if component.Values == nil || len(component.Values.Raw) == 0 {
		return emptyValues, nil
	}

	var compact bytes.Buffer
	if err := json.Compact(&compact, component.Values.Raw); err != nil {
		return "", fmt.Errorf("values are not valid JSON: %w", err)
	}
	return compact.String(), nil
}

// reconcileApplicationSet creates or updates the ApplicationSet for deployment.
//
// It must run only after every component's CCLInfraPlane is Available, for the same
// reason the CCLInfraPlane controller mirrors the cluster Secret before writing its own
// ApplicationSet: the destinations here name clusters rather than addressing them.
func (r *CCLDeploymentReconciler) reconcileApplicationSet(
	ctx context.Context,
	deployment *cclusternvidiacomv1alpha1.CCLDeployment,
	released int,
) (controllerutil.OperationResult, error) {
	spec, err := r.applicationSetSpec(deployment, released)
	if err != nil {
		return controllerutil.OperationResultNone, err
	}

	appSet := &argocdv1alpha1.ApplicationSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deployment.Name,
			Namespace: r.ArgoCDNamespace,
		},
	}

	return controllerutil.CreateOrUpdate(ctx, r.Client, appSet, func() error {
		if !deploymentOwner.ownedBy(appSet, deployment) {
			return errApplicationSetConflict
		}
		deploymentOwner.stamp(appSet, deployment)

		// Owned outright: rebuilt from scratch every reconcile, so hand edits are
		// reverted rather than merged.
		appSet.Spec = spec
		return nil
	})
}

// deleteApplicationSet removes the ApplicationSet created for deployment, unless it
// belongs to something else. Deleting it takes the generated Applications with it, and
// with them the components on the infra-plane clusters.
func (r *CCLDeploymentReconciler) deleteApplicationSet(
	ctx context.Context,
	deployment *cclusternvidiacomv1alpha1.CCLDeployment,
) error {
	log := logf.FromContext(ctx)

	appSet := &argocdv1alpha1.ApplicationSet{}
	key := client.ObjectKey{Namespace: r.ArgoCDNamespace, Name: deployment.Name}
	switch err := r.Get(ctx, key, appSet); {
	case apierrors.IsNotFound(err) || apimeta.IsNoMatchError(err):
		// Already gone, or Argo CD was never installed. Nothing to clean up.
		return nil
	case err != nil:
		return err
	case !deploymentOwner.ownedBy(appSet, deployment):
		log.Info("Left ApplicationSet owned by another resource in place",
			"applicationSet", deployment.Name, "namespace", r.ArgoCDNamespace)
		return nil
	}

	if err := r.Delete(ctx, appSet); client.IgnoreNotFound(err) != nil {
		return err
	}
	log.Info("Deleted Argo CD ApplicationSet", "applicationSet", deployment.Name,
		"namespace", r.ArgoCDNamespace)
	return nil
}

// applicationSetCondition turns the outcome of reconcileApplicationSet into the
// ComponentsApplied condition. A missing CRD and a name collision are reported rather
// than returned: neither is fixed by retrying, and both are resolved by an event the
// controller already watches for.
func (r *CCLDeploymentReconciler) applicationSetCondition(
	deployment *cclusternvidiacomv1alpha1.CCLDeployment,
	err error,
) (status metav1.ConditionStatus, reason, message string, retryErr error) {
	name := deployment.Name

	switch {
	case err == nil:
		return metav1.ConditionTrue, reasonApplicationSetCreated, fmt.Sprintf(
			"Applied ApplicationSet %q in namespace %q", name, r.ArgoCDNamespace), nil
	case apimeta.IsNoMatchError(err):
		// Argo CD is not installed - `make deploy-cp ARGOCD=false`, or the operator
		// started first.
		return metav1.ConditionFalse, reasonApplicationSetCRDMissing, fmt.Sprintf(
			"Argo CD ApplicationSet CRD not installed: %v", err), nil
	case errors.Is(err, errApplicationSetConflict):
		return metav1.ConditionFalse, reasonApplicationSetConflict, fmt.Sprintf(
			"ApplicationSet %q in namespace %q belongs to another resource",
			name, r.ArgoCDNamespace), nil
	default:
		return metav1.ConditionFalse, reasonApplicationSetFailed, err.Error(), err
	}
}

// listApplications returns the Applications generated for deployment, keyed by name.
//
// A missing CRD is reported as an empty set rather than an error: Argo CD may not be
// installed, and reconcileApplicationSet raises that properly a moment later.
func (r *CCLDeploymentReconciler) listApplications(
	ctx context.Context,
	deployment *cclusternvidiacomv1alpha1.CCLDeployment,
) (map[string]*argocdv1alpha1.Application, error) {
	var list argocdv1alpha1.ApplicationList
	err := r.List(ctx, &list,
		client.InNamespace(r.ArgoCDNamespace),
		client.MatchingLabels{
			labelDeploymentName:      deployment.Name,
			labelDeploymentNamespace: deployment.Namespace,
		})
	switch {
	case apimeta.IsNoMatchError(err):
		return map[string]*argocdv1alpha1.Application{}, nil
	case err != nil:
		return nil, err
	}

	applications := make(map[string]*argocdv1alpha1.Application, len(list.Items))
	for i := range list.Items {
		applications[list.Items[i].Name] = &list.Items[i]
	}
	return applications, nil
}

// rolloutStatus reports how far through the components the rollout has got.
//
// The first component that is not yet released, or is released but still converging, is
// the one holding everything after it up. There is no timeout on that wait, so naming it
// is the only way a stuck rollout is visible.
func (r *CCLDeploymentReconciler) rolloutStatus(
	deployment *cclusternvidiacomv1alpha1.CCLDeployment,
	applications map[string]*argocdv1alpha1.Application,
) (status metav1.ConditionStatus, reason, message string) {
	for i, component := range deployment.Spec.Components {
		wave := i + 1
		name := applicationName(deployment.Name, component.Name)

		application, released := applications[name]
		if !released {
			return metav1.ConditionTrue, reasonWaveSyncing, fmt.Sprintf(
				"Waiting to deploy wave %d (component %q)", wave, component.Name)
		}
		if healthy(application) {
			continue
		}

		detail := fmt.Sprintf("health %q, sync %q",
			application.Status.Health.Status, application.Status.Sync.Status)
		if application.Status.Health.Message != "" {
			detail += ": " + application.Status.Health.Message
		}
		return metav1.ConditionTrue, reasonWaveSyncing, fmt.Sprintf(
			"Wave %d (component %q) has %s", wave, component.Name, detail)
	}

	return metav1.ConditionFalse, reasonRolloutComplete, fmt.Sprintf(
		"All %d components are healthy and synced", len(deployment.Spec.Components))
}
