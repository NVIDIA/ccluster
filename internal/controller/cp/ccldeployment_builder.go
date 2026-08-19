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
	"context"
	"errors"
	"fmt"
	"sort"
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

// errApplicationConflict reports that an Argo CD Application name is already taken by another
// CCLDeployment, for the same reason errClusterSecretConflict exists: two resources of
// the same name in different namespaces map onto one object name in the Argo CD
// namespace.
var errApplicationConflict = errors.New("application belongs to another CCLDeployment")

// defaultProject is the Argo CD AppProject generated Applications belong to.
const defaultProject = "default"

// applicationName is the name Argo CD gives the Application for a component.
func applicationName(deploymentName, componentName string) string {
	return deploymentName + "-" + componentName
}

// healthy reports whether an Application has finished converging.
func healthy(application *argocdv1alpha1.Application) bool {
	return application.HealthStatus() == argocdv1alpha1.HealthStatusHealthy &&
		application.SyncStatus() == argocdv1alpha1.SyncStatusSynced
}

// applicationSpec builds the Argo CD Application for one component.
//
// The operator writes Applications directly rather than generating them from an
// ApplicationSet. An ApplicationSet would earn its keep if the set were derived from
// something - clusters, Git directories - but a CCLDeployment enumerates its components
// explicitly, so the generator would only add a Go-template layer between the operator
// and an object it already knows in full. Writing them directly also means values pass
// through as structured data instead of being marshalled into a templated string.
func (r *CCLDeploymentReconciler) applicationSpec(
	component cclusternvidiacomv1alpha1.ComponentSpec,
) argocdv1alpha1.ApplicationSpec {
	var helm *argocdv1alpha1.ApplicationSourceHelm
	if component.Values != nil && len(component.Values.Raw) > 0 {
		// Copied through untouched: what the chart accepts is its business.
		helm = &argocdv1alpha1.ApplicationSourceHelm{ValuesObject: component.Values}
	}

	return argocdv1alpha1.ApplicationSpec{
		Project: defaultProject,
		Source: argocdv1alpha1.ApplicationSource{
			// Repository and revision are operator configuration, not per component.
			RepoURL:        r.CatalogRepoURL,
			TargetRevision: r.CatalogRevision,
			// path, not chart: the catalog is a Git repository, and source.chart
			// would make Argo CD look for a chart of that name in a Helm registry.
			Path: component.Chart,
			Helm: helm,
		},
		Destination: argocdv1alpha1.ApplicationDestination{
			// By name, not by server: the name is the CCLInfraPlane's, which its
			// mirrored cluster Secret defines.
			Name:      component.InfraPlaneRef.Name,
			Namespace: component.Namespace,
		},
		SyncPolicy: &argocdv1alpha1.SyncPolicy{
			// Left entirely to Argo CD. The operator decides *whether* a component
			// is deployed, never *how* it syncs - which is what keeps selfHeal
			// working. Argo CD's own RollingSync strategy is deliberately unused
			// because it gates Applications by switching automated sync off and, as
			// of v3.5.1, never switches it back on.
			Automated: &argocdv1alpha1.SyncPolicyAutomated{
				Prune:    true,
				SelfHeal: true,
			},
			// ServerSideApply is not optional for this catalog. Argo CD's default
			// client-side apply records the manifest in the
			// kubectl.kubernetes.io/last-applied-configuration annotation and diffs
			// against it, but an annotation cannot hold a large object -
			// argo-rollouts' rollouts.argoproj.io CRD is ~600KB - so the copy is
			// truncated and the diff never converges. The resource then sits
			// OutOfSync forever despite syncing successfully, which stalls every
			// later component. It needs controller.diff.server.side on the
			// application controller to match; the cp chart sets that.
			SyncOptions: []string{"CreateNamespace=true", "ServerSideApply=true"},
		},
	}
}

// reconcileApplication creates or updates the Application for one component.
func (r *CCLDeploymentReconciler) reconcileApplication(
	ctx context.Context,
	deployment *cclusternvidiacomv1alpha1.CCLDeployment,
	component cclusternvidiacomv1alpha1.ComponentSpec,
	wave int,
) (controllerutil.OperationResult, error) {
	application := &argocdv1alpha1.Application{
		ObjectMeta: metav1.ObjectMeta{
			Name:      applicationName(deployment.Name, component.Name),
			Namespace: r.ArgoCDNamespace,
		},
	}

	return controllerutil.CreateOrUpdate(ctx, r.Client, application, func() error {
		if !deploymentOwner.ownedBy(application, deployment) {
			return errApplicationConflict
		}
		deploymentOwner.stamp(application, deployment)

		// The back-reference as labels as well as annotations. stamp writes the
		// annotations, which is what the ownership check reads; these are what
		// listApplications selects on, and selecting is how the operator knows which
		// components it has already released.
		application.Labels[labelDeploymentName] = deployment.Name
		application.Labels[labelDeploymentNamespace] = deployment.Namespace

		// Position in the rollout. Nothing acts on it - it is a label so that
		// `kubectl get applications -L ccluster.nvidia.com/wave` shows the order the
		// operator is working through.
		application.Labels[labelWave] = strconv.Itoa(wave)

		// Without this an Application delete is non-cascading: Argo CD's record goes
		// away and the chart stays installed and unmanaged on the infra-plane cluster.
		if !controllerutil.ContainsFinalizer(application, argocdv1alpha1.ResourcesFinalizer) {
			controllerutil.AddFinalizer(application, argocdv1alpha1.ResourcesFinalizer)
		}

		// Owned outright: rebuilt from scratch every reconcile, so hand edits are
		// reverted rather than merged.
		application.Spec = r.applicationSpec(component)
		return nil
	})
}

// reconcileApplications writes an Application for each of the first `released`
// components, and removes any Application left over from a component that is no longer in
// the spec.
//
// Pruning is the operator's job now that nothing generates these for it. It is done after
// the writes so that a component being renamed does not briefly leave the cluster with
// neither the old nor the new Application.
func (r *CCLDeploymentReconciler) reconcileApplications(
	ctx context.Context,
	deployment *cclusternvidiacomv1alpha1.CCLDeployment,
	released int,
	existing map[string]*argocdv1alpha1.Application,
) (changed bool, err error) {
	for i, component := range deployment.Spec.Components[:released] {
		op, err := r.reconcileApplication(ctx, deployment, component, i+1)
		if err != nil {
			return changed, err
		}
		if op != controllerutil.OperationResultNone {
			changed = true
		}
	}

	pruned, err := r.pruneApplications(ctx, deployment, existing)
	return changed || pruned, err
}

// pruneApplications deletes Applications whose component has been removed from the spec.
func (r *CCLDeploymentReconciler) pruneApplications(
	ctx context.Context,
	deployment *cclusternvidiacomv1alpha1.CCLDeployment,
	existing map[string]*argocdv1alpha1.Application,
) (bool, error) {
	log := logf.FromContext(ctx)

	wanted := make(map[string]struct{}, len(deployment.Spec.Components))
	for _, component := range deployment.Spec.Components {
		wanted[applicationName(deployment.Name, component.Name)] = struct{}{}
	}

	pruned := false
	// Sorted so the deletions, and the log lines they produce, are deterministic.
	for _, name := range sortedNames(existing) {
		if _, keep := wanted[name]; keep {
			continue
		}
		if !deploymentOwner.ownedBy(existing[name], deployment) {
			continue
		}
		if err := r.Delete(ctx, existing[name]); client.IgnoreNotFound(err) != nil {
			return pruned, err
		}
		pruned = true
		log.Info("Deleted Argo CD Application for a removed component",
			"application", name, "namespace", r.ArgoCDNamespace)
	}
	return pruned, nil
}

// teardownNext removes one Application, highest wave first, and reports how many of
// deployment's Applications are still present.
//
// Components are deployed in order because a later one may depend on an earlier one, so
// they have to come down the other way round: a component that owns CRDs must outlive
// whatever is still holding custom resources of that kind. Issuing the deletes in reverse
// is not enough on its own - Argo CD uninstalls them concurrently and the order is lost -
// so this deletes exactly one per call and waits for it to be gone before starting the
// next.
//
// A zero return means everything is gone and the finalizer can be dropped.
func (r *CCLDeploymentReconciler) teardownNext(
	ctx context.Context,
	deployment *cclusternvidiacomv1alpha1.CCLDeployment,
) (remaining int, err error) {
	log := logf.FromContext(ctx)

	all, err := r.listApplications(ctx, deployment)
	if err != nil {
		return 0, err
	}

	owned := make(map[string]*argocdv1alpha1.Application, len(all))
	for name, application := range all {
		if deploymentOwner.ownedBy(application, deployment) {
			owned[name] = application
		}
	}

	if len(owned) == 0 {
		// Logged here rather than per pass, so a foreign Application does not
		// produce a line on every requeue while teardown runs.
		for _, name := range sortedNames(all) {
			log.Info("Left Application owned by another CCLDeployment in place",
				"application", name, "namespace", r.ArgoCDNamespace)
		}
		return 0, nil
	}

	// Something is already on its way out. Wait for it: starting the next now would
	// collapse the ordering back into a concurrent teardown.
	for _, name := range sortedNames(owned) {
		if !owned[name].DeletionTimestamp.IsZero() {
			log.V(1).Info("Waiting for Application to finish terminating",
				"application", name, "namespace", r.ArgoCDNamespace)
			return len(owned), nil
		}
	}

	// Highest wave first.
	components := deployment.Spec.Components
	for i := len(components) - 1; i >= 0; i-- {
		name := applicationName(deployment.Name, components[i].Name)
		application, present := owned[name]
		if !present {
			continue
		}
		if err := r.Delete(ctx, application); client.IgnoreNotFound(err) != nil {
			return len(owned), err
		}
		log.Info("Deleted Argo CD Application", "application", name,
			"namespace", r.ArgoCDNamespace, "wave", i+1)
		return len(owned), nil
	}

	// Owned, but no longer named by any component - the spec was edited while the
	// CCLDeployment was being deleted. There is no wave to order these by, so they go
	// in a deterministic order instead.
	name := sortedNames(owned)[0]
	if err := r.Delete(ctx, owned[name]); client.IgnoreNotFound(err) != nil {
		return len(owned), err
	}
	log.Info("Deleted Argo CD Application for a component no longer in the spec",
		"application", name, "namespace", r.ArgoCDNamespace)
	return len(owned), nil
}

// sortedNames returns the keys of an Application map in a deterministic order.
func sortedNames(applications map[string]*argocdv1alpha1.Application) []string {
	names := make([]string, 0, len(applications))
	for name := range applications {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// listApplications returns every Application created for deployment, keyed by name,
// including ones that are terminating. Callers that reason about what is deployed want
// liveApplications instead; teardown needs to see the terminating ones.
//
// A missing CRD is reported as an empty set rather than an error: Argo CD may not be
// installed, and reconcileApplications raises that properly a moment later.
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

// liveApplications drops the Applications that are terminating.
//
// One of those is not a live release: its cascade finalizer keeps the object around while
// Argo CD uninstalls the chart, and counting it would let a pruned component hold the
// rollout open, or make a re-added one look already deployed.
func liveApplications(
	applications map[string]*argocdv1alpha1.Application,
) map[string]*argocdv1alpha1.Application {
	live := make(map[string]*argocdv1alpha1.Application, len(applications))
	for name, application := range applications {
		if application.DeletionTimestamp.IsZero() {
			live[name] = application
		}
	}
	return live
}

// releaseCount decides how many of deployment's components may be deployed right now.
//
// This is the ordering mechanism: a component's Application is not created until every
// component before it is Healthy and Synced. Releasing is monotonic and derived entirely
// from which Applications exist, so it needs no state of its own and survives an operator
// restart. A component that has been released is never withdrawn just because it became
// unhealthy - that would delete a running Application and, through the cascade finalizer,
// uninstall its chart.
func releaseCount(
	deployment *cclusternvidiacomv1alpha1.CCLDeployment,
	applications map[string]*argocdv1alpha1.Application,
) int {
	components := deployment.Spec.Components

	// Already released: the leading run of components that have an Application.
	released := 0
	for i := range components {
		if _, created := applications[applicationName(deployment.Name, components[i].Name)]; !created {
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
		if !healthy(applications[applicationName(deployment.Name, components[i].Name)]) {
			return released
		}
	}

	if released < len(components) {
		released++
	}
	return released
}

// applicationsCondition turns the outcome of reconcileApplications into the
// ComponentsApplied condition. A missing CRD and a name collision are reported rather
// than returned: neither is fixed by retrying, and both are resolved by an event the
// controller already watches for.
func (r *CCLDeploymentReconciler) applicationsCondition(
	deployment *cclusternvidiacomv1alpha1.CCLDeployment,
	released int,
	err error,
) (status metav1.ConditionStatus, reason, message string, retryErr error) {
	switch {
	case err == nil:
		return metav1.ConditionTrue, reasonApplicationsCreated, fmt.Sprintf(
			"Applied %d of %d components in namespace %q",
			released, len(deployment.Spec.Components), r.ArgoCDNamespace), nil
	case apimeta.IsNoMatchError(err) || apierrors.IsNotFound(err):
		// Argo CD is not installed - `make deploy-cp ARGOCD=false`, or the operator
		// started first.
		return metav1.ConditionFalse, reasonApplicationCRDMissing, fmt.Sprintf(
			"Argo CD Application CRD not installed: %v", err), nil
	case errors.Is(err, errApplicationConflict):
		return metav1.ConditionFalse, reasonApplicationConflict, fmt.Sprintf(
			"An Application in namespace %q belongs to another CCLDeployment",
			r.ArgoCDNamespace), nil
	default:
		return metav1.ConditionFalse, reasonApplicationFailed, err.Error(), err
	}
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
			application.HealthStatus(), application.SyncStatus())
		if application.HealthMessage() != "" {
			detail += ": " + application.HealthMessage()
		}
		return metav1.ConditionTrue, reasonWaveSyncing, fmt.Sprintf(
			"Wave %d (component %q) has %s", wave, component.Name, detail)
	}

	return metav1.ConditionFalse, reasonRolloutComplete, fmt.Sprintf(
		"All %d components are healthy and synced", len(deployment.Spec.Components))
}
