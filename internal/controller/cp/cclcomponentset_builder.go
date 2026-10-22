/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/yaml"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
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

// applicationSpec builds the desired Application spec for one component.
// Components are enumerated explicitly, so Applications need no ApplicationSet generator.
// Helm values are serialized directly to YAML without Go-template interpolation.
func (r *CCLComponentSetReconciler) applicationSpec(
	componentSet *cclusternvidiacomv1alpha1.CCLComponentSet,
	component cclusternvidiacomv1alpha1.ComponentSpec,
) argocdv1alpha1.ApplicationSpec {
	// ServerSideApply is not optional for this catalog. Argo CD's default client-side
	// apply records the manifest in the kubectl.kubernetes.io/last-applied-configuration
	// annotation and diffs against it, but an annotation cannot hold a large object -
	// argo-rollouts' rollouts.argoproj.io CRD is ~600KB - so the copy is truncated and the
	// diff never converges. The resource then sits OutOfSync forever despite syncing
	// successfully, which stalls every later component. It needs
	// controller.diff.server.side on the application controller to match; the cp chart
	// sets that.
	syncOptions := []string{"CreateNamespace=true", "ServerSideApply=true"}

	var ignored []argocdv1alpha1.ResourceIgnoreDifferences
	if r.Drift != nil {
		if rules, ok := r.Drift.Lookup(component.Chart); ok {
			ignored = ignoreDifferences(rules())
		}
	}
	if len(ignored) > 0 {
		// Without it ignoreDifferences only hides the drift: every sync, self-heal's
		// included, still applies the rendered value, and the controller that owns
		// the field patches it back.
		syncOptions = append(syncOptions, "RespectIgnoreDifferences=true")
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
			Helm: helmValues(component.Values),
		},
		Destination: argocdv1alpha1.ApplicationDestination{
			// By name, not by server: the name is the one the CCLInfraPlane's mirrored
			// cluster Secret registered, and the ref is same-namespace by definition.
			Name:      argoCDClusterName(componentSet.Namespace, component.InfraPlaneRef.Name),
			Namespace: component.Namespace,
		},
		SyncPolicy: &argocdv1alpha1.SyncPolicy{
			// Never switched off. It runs every sync, including the one after the
			// operator rewrites the spec, and the operator never gates one, which is
			// what keeps selfHeal working. Argo CD's own RollingSync strategy is
			// deliberately unused because it gates Applications by switching automated
			// sync off and, as of v3.5.1, never switches it back on.
			Automated: &argocdv1alpha1.SyncPolicyAutomated{
				Prune:    true,
				SelfHeal: true,
			},
			SyncOptions: syncOptions,
		},
		IgnoreDifferences: ignored,
	}
}

// helmValues serializes component values with the conversion Argo CD uses for valuesObject.
// The YAML string preserves explicit nulls in comparedTo and normalizes key order and
// number formatting for spec comparisons.
func helmValues(values *apiextensionsv1.JSON) *argocdv1alpha1.ApplicationSourceHelm {
	if values == nil || len(values.Raw) == 0 {
		return nil
	}
	document, err := yaml.JSONToYAML(values.Raw)
	if err != nil {
		// The API server validates JSON. Preserve invalid bytes from direct callers
		// rather than silently dropping their values.
		return &argocdv1alpha1.ApplicationSourceHelm{Values: string(values.Raw)}
	}
	return &argocdv1alpha1.ApplicationSourceHelm{Values: string(document)}
}

// ignoreDifferences writes a chart's drift rules as Argo CD ignoreDifferences. A rule
// names no object, so it applies to every object of its kind that the Application
// manages.
func ignoreDifferences(rules []catalogs.DriftSpec) []argocdv1alpha1.ResourceIgnoreDifferences {
	if len(rules) == 0 {
		return nil
	}
	ignored := make([]argocdv1alpha1.ResourceIgnoreDifferences, 0, len(rules))
	for _, rule := range rules {
		ignored = append(ignored, argocdv1alpha1.ResourceIgnoreDifferences{
			Group:             rule.Group,
			Kind:              rule.Kind,
			JQPathExpressions: rule.JQPaths,
		})
	}
	return ignored
}

// comparedToMatchesSpec reports whether Argo CD's latest comparison used the
// desired source, destination and ignoreDifferences.
//
// Argo CD records sync status, comparedTo and health from the same comparison.
// Matching its inputs ties those statuses to the desired configuration without tracking
// which sync request produced them.
func comparedToMatchesSpec(
	app *argocdv1alpha1.Application, desired argocdv1alpha1.ApplicationSpec,
) bool {
	if app.Status == nil {
		return false
	}
	// ApplicationSource keeps the keys it does not model, so this sees a source Argo CD
	// rendered from something the operator did not write. Destination and
	// ignoreDifferences are modelled in full; a comparison made before a rule applied
	// still reports the drift the rule hides. comparedTo.sources is neither written nor
	// read: the operator writes exactly one source.
	comparedTo := app.Status.Sync.ComparedTo
	return equality.Semantic.DeepEqual(comparedTo.Source, desired.Source) &&
		equality.Semantic.DeepEqual(comparedTo.Destination, desired.Destination) &&
		equality.Semantic.DeepEqual(comparedTo.IgnoreDifferences, desired.IgnoreDifferences)
}

// finished reports that a released component has converged on the current spec.
// Hooks do not contribute to aggregate health and sync, so operation state must also
// permit completion.
func (r *CCLComponentSetReconciler) finished(
	componentSet *cclusternvidiacomv1alpha1.CCLComponentSet,
	component cclusternvidiacomv1alpha1.ComponentSpec,
	application *argocdv1alpha1.Application,
) bool {
	if application == nil || application.OperationInProgress() {
		return false
	}
	desired := r.applicationSpec(componentSet, component)
	return equality.Semantic.DeepEqual(application.Spec, desired) &&
		comparedToMatchesSpec(application, desired) &&
		!currentSyncFailed(application, desired) &&
		application.SyncStatus() == argocdv1alpha1.SyncStatusSynced &&
		application.HealthStatus() == argocdv1alpha1.HealthStatusHealthy
}

// createOrPatchApplication brings one component's Application in line with the spec.
//
// It asks Argo CD for nothing. An Application whose spec.source differs from
// status.sync.comparedTo.source is queued for comparison at once, and automated sync
// runs the sync that follows, so a request of the operator's own would only duplicate
// it - and writing one over an operation Argo CD had not started yet would lose that one.
//
// Argo CD has no status subresource, so its status writes move the resource version this
// write carries and the read has to be redone rather than the write forced.
func (r *CCLComponentSetReconciler) createOrPatchApplication(
	ctx context.Context,
	componentSet *cclusternvidiacomv1alpha1.CCLComponentSet,
	component cclusternvidiacomv1alpha1.ComponentSpec,
	wave int,
) (controllerutil.OperationResult, error) {
	result := controllerutil.OperationResultNone

	// Alongside the CCLComponentSet, not in the Argo CD namespace: Argo CD watches every
	// namespace (the cp chart's application.namespaces), and the namespace's AppProject
	// admits Applications from here.
	key := client.ObjectKey{
		Name:      applicationName(componentSet.Name, component.Name),
		Namespace: componentSet.Namespace,
	}

	// DefaultBackoff, not DefaultRetry: the interval grows (10ms, 50ms, 250ms, 1250ms),
	// which gives the informer time to catch up. DefaultRetry stays at 10ms.
	err := retry.OnError(retry.DefaultBackoff, staleRead, func() error {
		// A fresh object every call: Get decodes into whatever it is given, so reusing
		// one would leave its stamp behind for before to copy.
		application := &argocdv1alpha1.Application{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		}
		if err := r.Get(ctx, key, application); client.IgnoreNotFound(err) != nil {
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
		result, err = createOrPatchArgoCDObject(
			ctx, r.Client, before, application, application.Spec)
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

	owned := ownedApplications(componentSet, all)
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
	for i, component := range slices.Backward(components) {
		name := applicationName(componentSet.Name, component.Name)
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

// ownedApplications drops the Applications that back-reference another CCLComponentSet.
//
// The operator refuses to write one (ApplicationConflict) and never deletes one, so its
// state says nothing about componentSet's spec. An unclaimed Application is kept: the
// operator adopts it on its next write.
func ownedApplications(
	componentSet *cclusternvidiacomv1alpha1.CCLComponentSet,
	applications map[string]*argocdv1alpha1.Application,
) map[string]*argocdv1alpha1.Application {
	owned := make(map[string]*argocdv1alpha1.Application, len(applications))
	for name, application := range applications {
		if componentSetOwner.ownedBy(application, componentSet) {
			owned[name] = application
		}
	}
	return owned
}

// releaseCount decides how many of componentSet's components may be deployed right now.
//
// This is the ordering mechanism: a component's Application is not created until every
// component before it is Healthy and Synced. Releasing is monotonic and derived entirely
// from which Applications exist, so it needs no state of its own and survives an operator
// restart. A component that has been released is never withdrawn just because it became
// unhealthy - that would delete a running Application and, through the cascade finalizer,
// uninstall its chart.
func (r *CCLComponentSetReconciler) releaseCount(
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
		if !r.finished(componentSet, components[i], applications[applicationName(componentSet.Name, components[i].Name)]) {
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

// componentSyncStatuses builds status.componentSyncs from observed Applications, one
// entry per component that has one, in spec order. Callers replace the field with the
// result, which drops removed components and stale observations.
func (r *CCLComponentSetReconciler) componentSyncStatuses(
	componentSet *cclusternvidiacomv1alpha1.CCLComponentSet,
	applications map[string]*argocdv1alpha1.Application,
) []cclusternvidiacomv1alpha1.ComponentSyncStatus {
	statuses := make([]cclusternvidiacomv1alpha1.ComponentSyncStatus, 0, len(componentSet.Spec.Components))
	for _, component := range componentSet.Spec.Components {
		application := applications[applicationName(componentSet.Name, component.Name)]
		if application == nil {
			// Omit components without a live Application.
			continue
		}
		statuses = append(statuses, cclusternvidiacomv1alpha1.ComponentSyncStatus{
			Name:                  component.Name,
			ComparedToMatchesSpec: comparedToMatchesSpec(application, r.applicationSpec(componentSet, component)),
			Health:                application.HealthStatus(),
			Sync:                  application.SyncStatus(),
			Message:               application.OperationMessage(),
		})
	}
	return statuses
}

// currentSyncFailed reports a terminal failure for the source currently desired and
// the revision Argo CD compared. Comparison and operation status advance independently,
// so comparedTo alone cannot attribute the last operation to the current spec.
func currentSyncFailed(app *argocdv1alpha1.Application, desired argocdv1alpha1.ApplicationSpec) bool {
	phase := app.OperationPhase()
	if app.OperationInProgress() ||
		(phase != argocdv1alpha1.OperationFailed && phase != argocdv1alpha1.OperationError) {
		return false
	}
	state := app.Status.OperationState
	if result := state.SyncResult; result != nil {
		return equality.Semantic.DeepEqual(result.Source, desired.Source) &&
			(result.Revision == "" || app.Status.Sync.Revision == "" || result.Revision == app.Status.Sync.Revision)
	}
	// Early errors can precede syncResult. An explicit source in the captured request
	// still attributes them; without either source the failure cannot be attributed.
	return state.Operation.Sync != nil && state.Operation.Sync.Source != nil &&
		equality.Semantic.DeepEqual(*state.Operation.Sync.Source, desired.Source)
}

// rolloutStatus reports how far through the components the rollout has got.
//
// The first component that is not yet released, or is released but still converging, is
// the one holding everything after it up. There is no timeout on that wait, so naming it
// is the only way a stuck rollout is visible.
//
// invalidReason and invalidMessage are this pass's validation failure, empty when it
// passed. The operator writes no Application on such a pass, so a component waiting for
// the operator is reported under that reason, which names what actually holds it up.
// Waits on Argo CD are reported as usual: Argo CD keeps working on what was written.
func (r *CCLComponentSetReconciler) rolloutStatus(
	componentSet *cclusternvidiacomv1alpha1.CCLComponentSet,
	applications map[string]*argocdv1alpha1.Application,
	invalidReason, invalidMessage string,
) (status metav1.ConditionStatus, reason, message string) {
	for i, component := range componentSet.Spec.Components {
		wave := i + 1
		name := applicationName(componentSet.Name, component.Name)

		application, released := applications[name]
		if !released {
			if invalidReason != "" {
				return metav1.ConditionTrue, invalidReason, fmt.Sprintf(
					"Wave %d (component %q) cannot be deployed until validation passes: %s",
					wave, component.Name, invalidMessage)
			}
			return metav1.ConditionTrue, reasonWaveSyncing, fmt.Sprintf(
				"Waiting to deploy wave %d (component %q)", wave, component.Name)
		}
		if r.finished(componentSet, component, application) {
			continue
		}

		desired := r.applicationSpec(componentSet, component)
		switch {
		case !equality.Semantic.DeepEqual(application.Spec, desired) && invalidReason != "":
			// Only an edit to the CCLComponentSet or its CCLInfraPlanes, both watched,
			// lets the operator write this spec.
			return metav1.ConditionTrue, invalidReason, fmt.Sprintf(
				"Wave %d (component %q) cannot be written until validation passes: %s",
				wave, component.Name, invalidMessage)

		case !equality.Semantic.DeepEqual(application.Spec, desired):
			// The list behind this report predates this reconcile's write. The write's
			// own event brings the CCLComponentSet back.
			return metav1.ConditionTrue, reasonWaveSyncing, fmt.Sprintf(
				"Wave %d (component %q) is waiting for the operator to write the current spec",
				wave, component.Name)

		case !comparedToMatchesSpec(application, desired):
			// The latest comparison does not match the desired source, destination and
			// ignoreDifferences, so its health and sync statuses cannot establish
			// completion.
			return metav1.ConditionTrue, reasonWaveSyncing, withRunningOperationMessage(fmt.Sprintf(
				"Wave %d (component %q) is waiting for Argo CD to compare the spec the "+
					"operator wrote", wave, component.Name), application)

		case application.OperationInProgress():
			// Hooks are excluded from aggregate health and sync. Healthy/Synced can
			// therefore precede operation completion, including a pending PostSync hook.
			return metav1.ConditionTrue, reasonWaveSyncing, withRunningOperationMessage(fmt.Sprintf(
				"Wave %d (component %q) is waiting for an Argo CD operation to complete",
				wave, component.Name), application)

		case currentSyncFailed(application, desired):
			// A failed hook can leave aggregate health Healthy and sync Synced.
			// A later successful sync of this source and revision resumes the rollout.
			return metav1.ConditionTrue, reasonWaveSyncFailed, fmt.Sprintf(
				"Wave %d (component %q) failed to sync: %s. A successful sync of "+
					"Application %q in Argo CD resumes the rollout",
				wave, component.Name, application.OperationMessage(), name)

		default:
			// Argo CD compared this spec and is unhappy with what it found. Which
			// resource is unhappy is in status.resources, which this type does not model.
			return metav1.ConditionTrue, reasonWaveSyncing, fmt.Sprintf(
				"Wave %d (component %q) has health %q, sync %q", wave, component.Name,
				application.HealthStatus(), application.SyncStatus())
		}
	}

	return metav1.ConditionFalse, reasonRolloutComplete, fmt.Sprintf(
		"All %d components have synced the current spec and are healthy",
		len(componentSet.Spec.Components))
}

// withRunningOperationMessage appends the message of the operation Argo CD is running.
//
// Argo CD holds a failing sync in Running while it retries, and says which attempt it is
// on in the operation message. Without it a sync failing on every attempt reads exactly
// like a slow one. The phase decides whether the message is current, not spec.operation:
// Argo CD keeps that set from the request until the operation reaches a terminal phase.
// A terminal phase, beside a pending request or not, belongs to an earlier operation.
func withRunningOperationMessage(waiting string, application *argocdv1alpha1.Application) string {
	message := application.OperationMessage()
	if message == "" || argocdv1alpha1.OperationCompleted(application.OperationPhase()) {
		return waiting
	}
	return waiting + ": " + message
}
