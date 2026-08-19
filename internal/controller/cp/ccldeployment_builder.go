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
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
	argocdv1alpha1 "github.com/NVIDIA/ccluster/internal/argocd/v1alpha1"
)

const (
	// emptyValues is what a component with no overrides contributes to helm.values.
	// It cannot simply be omitted: the field is filled by a Go template, and a
	// template can substitute a key but not decide whether the key exists.
	emptyValues = "{}"

	// rollingSyncStrategy is the only ApplicationSet strategy the operator writes.
	rollingSyncStrategy = "RollingSync"

	// defaultProject is the Argo CD AppProject generated Applications belong to.
	defaultProject = "default"
)

// applicationSetSpec builds the ApplicationSet that deploys deployment's components.
//
// The shape is: one generator element per component, carrying everything that varies
// between them, and one Application template that references those values by name. The
// ordering comes from strategy.rollingSync, whose steps select generated Applications by
// the wave label the template stamps on them.
//
// Every placeholder is Go template syntax, and goTemplateOptions asks text/template to
// fail on an unknown key rather than render an empty string - which would otherwise turn
// a typo here into an Application pointed at the wrong chart.
func (r *CCLDeploymentReconciler) applicationSetSpec(
	deployment *cclusternvidiacomv1alpha1.CCLDeployment,
) (argocdv1alpha1.ApplicationSetSpec, error) {
	elements := make([]argocdv1alpha1.GeneratorElement, 0, len(deployment.Spec.Components))
	steps := make([]argocdv1alpha1.ApplicationSetRolloutStep, 0, len(deployment.Spec.Components))

	for i, component := range deployment.Spec.Components {
		values, err := componentValues(component)
		if err != nil {
			return argocdv1alpha1.ApplicationSetSpec{}, fmt.Errorf(
				"component %q: %w", component.Name, err)
		}

		// One wave per component, numbered from 1: the list order is the rollout
		// order. Grouping independent components into a shared wave is what a
		// dependsOn field would later buy.
		wave := strconv.Itoa(i + 1)

		elements = append(elements, argocdv1alpha1.GeneratorElement{
			Name:       component.Name,
			Path:       component.Chart,
			InfraPlane: component.InfraPlaneRef.Name,
			Namespace:  component.Namespace,
			Wave:       wave,
			Values:     values,
		})

		// A fresh value per step: these are pointers, and sharing one would make the
		// generated deepcopy alias every step's maxUpdate onto the same object.
		maxUpdate := intstr.FromString("100%")
		steps = append(steps, argocdv1alpha1.ApplicationSetRolloutStep{
			MatchExpressions: []argocdv1alpha1.ApplicationMatchExpression{{
				Key:      labelWave,
				Operator: "In",
				Values:   []string{wave},
			}},
			MaxUpdate: &maxUpdate,
		})
	}

	return argocdv1alpha1.ApplicationSetSpec{
		GoTemplate:        true,
		GoTemplateOptions: []string{"missingkey=error"},
		Strategy: &argocdv1alpha1.ApplicationSetStrategy{
			Type: rollingSyncStrategy,
			RollingSync: &argocdv1alpha1.ApplicationSetRolloutStrategy{
				Steps: steps,
			},
		},
		Generators: []argocdv1alpha1.ApplicationSetGenerator{{
			List: &argocdv1alpha1.ListGenerator{Elements: elements},
		}},
		Template: argocdv1alpha1.ApplicationSetTemplate{
			ApplicationSetTemplateMeta: argocdv1alpha1.ApplicationSetTemplateMeta{
				Name: deployment.Name + "-{{.name}}",
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
					// What the RollingSync steps select on.
					labelWave: "{{.wave}}",
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
					Automated: &argocdv1alpha1.SyncPolicyAutomated{
						Prune:    true,
						SelfHeal: true,
					},
					SyncOptions: []string{"CreateNamespace=true"},
				},
			},
		},
	}, nil
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
) (controllerutil.OperationResult, error) {
	spec, err := r.applicationSetSpec(deployment)
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

// rolloutStatus reports on the RollingSync by reading the generated Applications back.
//
// This exists because a RollingSync step waits for its Applications to become Healthy
// and Argo CD applies no timeout to that wait. A component that never reports healthy
// therefore stalls every later wave silently, and without this the CCLDeployment would
// look idle rather than stuck. The first component that is missing, unsynced or unhealthy
// is the one holding the rollout up, so it is the one named.
func (r *CCLDeploymentReconciler) rolloutStatus(
	ctx context.Context,
	deployment *cclusternvidiacomv1alpha1.CCLDeployment,
) (status metav1.ConditionStatus, reason, message string) {
	var list argocdv1alpha1.ApplicationList
	err := r.List(ctx, &list,
		client.InNamespace(r.ArgoCDNamespace),
		client.MatchingLabels{
			labelDeploymentName:      deployment.Name,
			labelDeploymentNamespace: deployment.Namespace,
		})
	if err != nil {
		return metav1.ConditionUnknown, reasonRolloutUnknown, fmt.Sprintf(
			"Could not read generated Applications: %v", err)
	}

	applications := make(map[string]*argocdv1alpha1.Application, len(list.Items))
	for i := range list.Items {
		applications[list.Items[i].Name] = &list.Items[i]
	}

	for i, component := range deployment.Spec.Components {
		wave := i + 1
		name := deployment.Name + "-" + component.Name

		application, generated := applications[name]
		if !generated {
			return metav1.ConditionTrue, reasonWaveSyncing, fmt.Sprintf(
				"Waiting for wave %d (component %q): Application %q not generated yet",
				wave, component.Name, name)
		}

		health := application.Status.Health.Status
		sync := application.Status.Sync.Status
		if health == argocdv1alpha1.HealthStatusHealthy && sync == argocdv1alpha1.SyncStatusSynced {
			continue
		}

		detail := fmt.Sprintf("health %q, sync %q", health, sync)
		if application.Status.Health.Message != "" {
			detail += ": " + application.Status.Health.Message
		}
		return metav1.ConditionTrue, reasonWaveSyncing, fmt.Sprintf(
			"Wave %d (component %q) has %s", wave, component.Name, detail)
	}

	return metav1.ConditionFalse, reasonRolloutComplete, fmt.Sprintf(
		"All %d components are healthy and synced", len(deployment.Spec.Components))
}
