/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
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
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
	argocdv1alpha1 "github.com/NVIDIA/ccluster/internal/argocd/v1alpha1"
)

// errApplicationConflict reports that an Argo CD Application of the wanted name exists
// but back-references a different CCLComponentSet. Applications live in their
// CCLComponentSet's namespace, so the operator's own naming cannot produce this; it means
// an Application manifest was copied from another namespace, and it is refused rather
// than overwritten.
var errApplicationConflict = errors.New("application belongs to another CCLComponentSet")

// applicationName is the name Argo CD gives the Application for a component.
func applicationName(componentSetName, componentName string) string {
	return componentSetName + "-" + componentName
}

// healthy reports whether an Application has finished converging.
//
// This is Argo CD's verdict on the spec it last compared, which is not always the spec
// the operator wants now. DESIGN.md explains why status.sync.comparedTo cannot close
// that gap, and what would.
func healthy(application *argocdv1alpha1.Application) bool {
	return application.HealthStatus() == argocdv1alpha1.HealthStatusHealthy &&
		application.SyncStatus() == argocdv1alpha1.SyncStatusSynced
}

// applicationSpec builds the Argo CD Application for one component.
//
// The operator writes Applications directly rather than generating them from an
// ApplicationSet. An ApplicationSet would earn its keep if the set were derived from
// something - clusters, Git directories - but a CCLComponentSet enumerates its components
// explicitly, so the generator would only add a Go-template layer between the operator
// and an object it already knows in full. Writing them directly also means values pass
// through as structured data instead of being marshalled into a templated string.
func (r *CCLComponentSetReconciler) applicationSpec(
	componentSet *cclusternvidiacomv1alpha1.CCLComponentSet,
	component cclusternvidiacomv1alpha1.ComponentSpec,
) argocdv1alpha1.ApplicationSpec {
	var helm *argocdv1alpha1.ApplicationSourceHelm
	if component.Values != nil && len(component.Values.Raw) > 0 {
		// Copied through untouched: what the chart accepts is its business.
		helm = &argocdv1alpha1.ApplicationSourceHelm{ValuesObject: component.Values}
	}

	return argocdv1alpha1.ApplicationSpec{
		// The namespace's project, which the CCLInfraPlane controller created before it
		// reported the plane Available. It is what permits an Application in this
		// namespace at all, and what confines it to the namespace's own clusters.
		Project: projectName(componentSet.Namespace),
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
			// By name, not by server: the name is the one the CCLInfraPlane's mirrored
			// cluster Secret registered, and the ref is same-namespace by definition.
			Name:      argoCDClusterName(componentSet.Namespace, component.InfraPlaneRef.Name),
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

// createOrPatchApplication brings one component's Application in line with the spec.
//
// Read and write are retried together when the read turns out to be stale. Reads come
// from the informer cache, and an Application has no status subresource, so Argo CD's
// status writes keep moving the resource version. Letting the lost race out would cost a
// whole reconcile and a spurious ApplicationFailed.
func (r *CCLComponentSetReconciler) createOrPatchApplication(
	ctx context.Context,
	componentSet *cclusternvidiacomv1alpha1.CCLComponentSet,
	component cclusternvidiacomv1alpha1.ComponentSpec,
	wave int,
) (controllerutil.OperationResult, error) {
	result := controllerutil.OperationResultNone

	// DefaultBackoff, not DefaultRetry: the interval grows (10ms, 50ms, 250ms, 1250ms),
	// which gives the informer time to catch up. DefaultRetry stays at 10ms.
	err := retry.OnError(retry.DefaultBackoff, staleRead, func() error {
		// A fresh object every attempt: Get decodes into whatever it is given, so
		// reusing the last one would leave its stamp behind for before to copy.
		application := &argocdv1alpha1.Application{
			ObjectMeta: metav1.ObjectMeta{
				Name: applicationName(componentSet.Name, component.Name),
				// Alongside the CCLComponentSet, not in the Argo CD namespace: Argo CD
				// watches every namespace (the cp chart's application.namespaces), and
				// the namespace's AppProject admits Applications from here.
				Namespace: componentSet.Namespace,
			},
		}

		if err := r.Get(ctx, client.ObjectKeyFromObject(application), application); client.IgnoreNotFound(err) != nil {
			return err
		}
		before := application.DeepCopy()
		if !componentSetOwner.ownedBy(application, componentSet) {
			return errApplicationConflict
		}
		componentSetOwner.stamp(application, componentSet)

		// listApplications uses this label to find the components already released.
		application.Labels[labelComponentSetName] = componentSet.Name

		// Informational only: kubectl can display the rollout position with -L.
		application.Labels[labelWave] = strconv.Itoa(wave)

		// Deleting the Application must also uninstall its chart.
		controllerutil.AddFinalizer(application, argocdv1alpha1.ResourcesFinalizer)

		application.Spec = r.applicationSpec(componentSet, component)

		var err error
		result, err = createOrPatchArgoCDObject(ctx, r.Client, before, application, application.Spec)
		return err
	})
	return result, err
}

// reconcileApplications writes an Application for each of the first `released`
// components, and removes any Application left over from a component that is no longer in
// the spec.
//
// Pruning is the operator's job now that nothing generates these for it. It is done after
// the writes so that a component being renamed does not briefly leave the cluster with
// neither the old nor the new Application.
func (r *CCLComponentSetReconciler) reconcileApplications(
	ctx context.Context,
	componentSet *cclusternvidiacomv1alpha1.CCLComponentSet,
	released int,
	existing map[string]*argocdv1alpha1.Application,
) (changed bool, err error) {
	for i, component := range componentSet.Spec.Components[:released] {
		op, err := r.createOrPatchApplication(ctx, componentSet, component, i+1)
		if err != nil {
			return changed, err
		}
		if op != controllerutil.OperationResultNone {
			changed = true
		}
	}

	pruned, err := r.pruneApplications(ctx, componentSet, existing)
	return changed || pruned, err
}

// pruneApplications deletes Applications whose component has been removed from the spec.
func (r *CCLComponentSetReconciler) pruneApplications(
	ctx context.Context,
	componentSet *cclusternvidiacomv1alpha1.CCLComponentSet,
	existing map[string]*argocdv1alpha1.Application,
) (bool, error) {
	log := logf.FromContext(ctx)

	wanted := make(map[string]struct{}, len(componentSet.Spec.Components))
	for _, component := range componentSet.Spec.Components {
		wanted[applicationName(componentSet.Name, component.Name)] = struct{}{}
	}

	pruned := false
	// Sorted so the deletions, and the log lines they produce, are deterministic.
	for _, name := range sortedNames(existing) {
		if _, keep := wanted[name]; keep {
			continue
		}
		if !componentSetOwner.ownedBy(existing[name], componentSet) {
			continue
		}
		if err := r.Delete(ctx, existing[name]); client.IgnoreNotFound(err) != nil {
			return pruned, err
		}
		pruned = true
		log.Info("Deleted Argo CD Application for a removed component",
			"application", name, "namespace", componentSet.Namespace)
	}
	return pruned, nil
}

// teardownNext removes one Application, highest wave first, and reports how many of
// componentSet's Applications are still present.
//
// Components are deployed in order because a later one may depend on an earlier one, so
// they have to come down the other way round: a component that owns CRDs must outlive
// whatever is still holding custom resources of that kind. Issuing the deletes in reverse
// is not enough on its own - Argo CD uninstalls them concurrently and the order is lost -
// so this deletes exactly one per call and waits for it to be gone before starting the
// next.
//
// A zero return means everything is gone and the finalizer can be dropped.
func (r *CCLComponentSetReconciler) teardownNext(
	ctx context.Context,
	componentSet *cclusternvidiacomv1alpha1.CCLComponentSet,
) (remaining int, err error) {
	log := logf.FromContext(ctx)

	all, err := r.listApplications(ctx, componentSet)
	if err != nil {
		return 0, err
	}

	owned := make(map[string]*argocdv1alpha1.Application, len(all))
	for name, application := range all {
		if componentSetOwner.ownedBy(application, componentSet) {
			owned[name] = application
		}
	}

	if len(owned) == 0 {
		// Logged here rather than per pass, so a foreign Application does not
		// produce a line on every requeue while teardown runs.
		for _, name := range sortedNames(all) {
			log.Info("Left Application owned by another CCLComponentSet in place",
				"application", name, "namespace", componentSet.Namespace)
		}
		return 0, nil
	}

	// Something is already on its way out. Wait for it: starting the next now would
	// collapse the ordering back into a concurrent teardown.
	for _, name := range sortedNames(owned) {
		if !owned[name].DeletionTimestamp.IsZero() {
			log.V(1).Info("Waiting for Application to finish terminating",
				"application", name, "namespace", componentSet.Namespace)
			return len(owned), nil
		}
	}

	// Highest wave first.
	components := componentSet.Spec.Components
	for i := len(components) - 1; i >= 0; i-- {
		name := applicationName(componentSet.Name, components[i].Name)
		application, present := owned[name]
		if !present {
			continue
		}
		if err := r.Delete(ctx, application); client.IgnoreNotFound(err) != nil {
			return len(owned), err
		}
		log.Info("Deleted Argo CD Application", "application", name,
			"namespace", componentSet.Namespace, "wave", i+1)
		return len(owned), nil
	}

	// Owned, but no longer named by any component - the spec was edited while the
	// CCLComponentSet was being deleted. There is no wave to order these by, so they go
	// in a deterministic order instead.
	name := sortedNames(owned)[0]
	if err := r.Delete(ctx, owned[name]); client.IgnoreNotFound(err) != nil {
		return len(owned), err
	}
	log.Info("Deleted Argo CD Application for a component no longer in the spec",
		"application", name, "namespace", componentSet.Namespace)
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

// listApplications returns every Application created for componentSet, keyed by name,
// including ones that are terminating. Callers that reason about what is deployed want
// liveApplications instead; teardown needs to see the terminating ones.
//
// A missing CRD is reported as an empty set rather than an error: Argo CD may not be
// installed, and reconcileApplications raises that properly a moment later.
func (r *CCLComponentSetReconciler) listApplications(
	ctx context.Context,
	componentSet *cclusternvidiacomv1alpha1.CCLComponentSet,
) (map[string]*argocdv1alpha1.Application, error) {
	var list argocdv1alpha1.ApplicationList
	err := r.List(ctx, &list,
		client.InNamespace(componentSet.Namespace),
		client.MatchingLabels{labelComponentSetName: componentSet.Name})
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

// releaseCount decides how many of componentSet's components may be deployed right now.
//
// This is the ordering mechanism: a component's Application is not created until every
// component before it is Healthy and Synced. Releasing is monotonic and derived entirely
// from which Applications exist, so it needs no state of its own and survives an operator
// restart. A component that has been released is never withdrawn just because it became
// unhealthy - that would delete a running Application and, through the cascade finalizer,
// uninstall its chart.
func releaseCount(
	componentSet *cclusternvidiacomv1alpha1.CCLComponentSet,
	applications map[string]*argocdv1alpha1.Application,
) int {
	components := componentSet.Spec.Components

	// Already released: the leading run of components that have an Application.
	released := 0
	for i := range components {
		if _, created := applications[applicationName(componentSet.Name, components[i].Name)]; !created {
			break
		}
		released = i + 1
	}
	if released == 0 {
		// Nothing deployed yet. The first component is always releasable; there is
		// nothing for it to wait on. Clamped because the caller slices
		// Components[:released]: a spec with no components is rejected by the
		// MinItems=1 CRD marker, but this function must not depend on the schema
		// for its own memory safety.
		return min(1, len(components))
	}

	// Hold if anything released is still converging. Checking all of them rather than
	// just the most recent means a component that degrades later stops the rollout,
	// which is what a sequential dependency implies.
	for i := 0; i < released; i++ {
		if !healthy(applications[applicationName(componentSet.Name, components[i].Name)]) {
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
func (r *CCLComponentSetReconciler) applicationsCondition(
	componentSet *cclusternvidiacomv1alpha1.CCLComponentSet,
	released int,
	err error,
) (status metav1.ConditionStatus, reason, message string, retryErr error) {
	switch {
	case err == nil:
		return metav1.ConditionTrue, reasonApplicationsCreated, fmt.Sprintf(
			"Applied %d of %d components in namespace %q",
			released, len(componentSet.Spec.Components), componentSet.Namespace), nil
	case apimeta.IsNoMatchError(err) || apierrors.IsNotFound(err):
		// Argo CD is not installed - `make deploy-cp ARGOCD=false`, or the operator
		// started first.
		return metav1.ConditionFalse, reasonApplicationCRDMissing, fmt.Sprintf(
			"Argo CD Application CRD not installed: %v", err), nil
	case errors.Is(err, errApplicationConflict):
		return metav1.ConditionFalse, reasonApplicationConflict, fmt.Sprintf(
			"An Application in namespace %q belongs to another CCLComponentSet",
			componentSet.Namespace), nil
	default:
		return metav1.ConditionFalse, reasonApplicationFailed, err.Error(), err
	}
}

// rolloutStatus reports how far through the components the rollout has got.
//
// The first component that is not yet released, or is released but still converging, is
// the one holding everything after it up. There is no timeout on that wait, so naming it
// is the only way a stuck rollout is visible.
func (r *CCLComponentSetReconciler) rolloutStatus(
	componentSet *cclusternvidiacomv1alpha1.CCLComponentSet,
	applications map[string]*argocdv1alpha1.Application,
) (status metav1.ConditionStatus, reason, message string) {
	for i, component := range componentSet.Spec.Components {
		wave := i + 1
		name := applicationName(componentSet.Name, component.Name)

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
		"All %d components are healthy and synced", len(componentSet.Spec.Components))
}
