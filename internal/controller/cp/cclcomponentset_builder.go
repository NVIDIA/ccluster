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
	"strings"

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

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
	argocdv1alpha1 "github.com/NVIDIA/ccluster/internal/api/argocd/v1alpha1"
)

// errApplicationConflict reports that an Argo CD Application of the wanted name exists
// but back-references a different CCLComponentSet. It is refused rather than overwritten.
//
// The operator's own naming can produce it: applicationName joins with "-", so set "a"
// with component "b-c" and set "a-b" with component "c" both want "a-b-c" in one
// namespace. The first to write it keeps it. Reconciled in parallel, both can see it
// missing and create it. The loser's AlreadyExists is a staleRead: its retried reads
// usually find the winner's back-reference in the cache, and if they do not, the pass
// fails and its retry reports the conflict. A manifest copied from another namespace
// produces it too.
var errApplicationConflict = errors.New("application belongs to another CCLComponentSet")

// applicationName is the name Argo CD gives the Application for a component.
func applicationName(componentSetName, componentName string) string {
	return componentSetName + "-" + componentName
}

// componentName is the component an Application deploys, read back from its name. A
// component removed from the spec has nothing else to name it by.
func componentName(componentSetName, name string) string {
	return strings.TrimPrefix(name, componentSetName+"-")
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

// finished reports whether a released component is done: Argo CD has synced the
// current spec, and the component is healthy. The health is judged in this pass.
func (r *CCLComponentSetReconciler) finished(
	componentSet *cclusternvidiacomv1alpha1.CCLComponentSet,
	component cclusternvidiacomv1alpha1.ComponentSpec,
	application *argocdv1alpha1.Application,
	healths componentHealths,
) bool {
	return r.syncedCurrentSpec(componentSet, component, application) &&
		healthy(healths.of(component.Name, application).Status)
}

// syncedCurrentSpec reports whether Argo CD has synced the current spec, not only
// whether sync status is Synced. That means four things: the Application has the spec
// the operator writes, Argo CD compared that spec, no operation runs, and the last sync
// of that spec did not fail. Argo CD leaves hooks out of sync status, so a running
// operation, such as a PostSync hook, also means not yet.
func (r *CCLComponentSetReconciler) syncedCurrentSpec(
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
		application.SyncStatus() == argocdv1alpha1.SyncStatusSynced
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

	// DefaultBackoff, not DefaultRetry: four reads with growing waits (10ms, 50ms, 250ms,
	// about 310ms in all), which gives the informer time to catch up. DefaultRetry stays
	// at 10ms.
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

		// The rollout position, which kubectl shows with -L. Once the component leaves the
		// spec, this is the only record of where it was, and pruneOrder orders by it.
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

// pruneNext deletes one of the prunable Applications, and reports whether it deleted
// one. prunable is what prunableApplications returns, terminating ones included.
// applyComponents calls it only once every component in the spec has finished;
// teardownNext calls it before it deletes any of them.
//
// Removal waits for that because a removed component can still serve one that remains:
// an ExternalSecret producer replaced by a new one keeps the old Secret in place for pods
// that have not restarted on the new one yet. Removed components then go in reverse, one at
// a time, for the reason teardownNext gives. Their spec positions are gone, so the wave
// label they were last written with orders them.
func (r *CCLComponentSetReconciler) pruneNext(
	ctx context.Context,
	componentSet *cclusternvidiacomv1alpha1.CCLComponentSet,
	prunable map[string]*argocdv1alpha1.Application,
) (bool, error) {
	log := logf.FromContext(ctx)

	if len(prunable) == 0 {
		return false, nil
	}
	if terminating := terminatingName(prunable); terminating != "" {
		log.V(1).Info("Skipped deleting the next Application while one is terminating",
			"application", terminating, "namespace", componentSet.Namespace)
		return false, nil
	}

	name := pruneOrder(prunable)[0]
	if err := r.Delete(ctx, prunable[name]); client.IgnoreNotFound(err) != nil {
		return false, err
	}
	log.Info("Deleted Argo CD Application for a removed component", "application", name,
		"namespace", componentSet.Namespace, "wave", prunable[name].Labels[labelWave])
	return true, nil
}

// prunableApplications returns componentSet's own Applications whose component is no longer
// in the spec, terminating ones included.
func prunableApplications(
	componentSet *cclusternvidiacomv1alpha1.CCLComponentSet,
	applications map[string]*argocdv1alpha1.Application,
) map[string]*argocdv1alpha1.Application {
	prunable := ownedApplications(componentSet, applications)
	for _, component := range componentSet.Spec.Components {
		delete(prunable, applicationName(componentSet.Name, component.Name))
	}
	return prunable
}

// pruneOrder lists the Applications of removed components in the order they are deleted:
// the highest wave label first. One whose label is missing or not a position goes after
// every labelled one. Equal waves, and unlabelled Applications, go in name order.
func pruneOrder(prunable map[string]*argocdv1alpha1.Application) []string {
	names := sortedNames(prunable)
	sort.SliceStable(names, func(i, j int) bool {
		return labelledWave(prunable[names[i]]) > labelledWave(prunable[names[j]])
	})
	return names
}

// labelledWave reads an Application's wave label. It returns 0, below every position the
// operator writes, for a label that is missing or not a position.
func labelledWave(application *argocdv1alpha1.Application) int {
	wave, err := strconv.Atoi(application.Labels[labelWave])
	if err != nil || wave < 1 {
		return 0
	}
	return wave
}

// teardownNext removes one Application and reports how many of componentSet's
// Applications are still present. Those of removed components go first, in the order
// pruneNext uses; then the rest, highest position first.
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

	all, err := listApplications(ctx, r.Client, componentSet)
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
	if terminating := terminatingName(owned); terminating != "" {
		log.V(1).Info("Skipped deleting the next Application while one is terminating",
			"application", terminating, "namespace", componentSet.Namespace)
		return len(owned), nil
	}

	// A removal still waiting, or one from a spec edited during the delete, finishes first:
	// a removed component depending on a remaining one is the more common case. DESIGN.md
	// §5 gives the case this order gets wrong.
	pruned, err := r.pruneNext(ctx, componentSet, prunableApplications(componentSet, owned))
	if err != nil || pruned {
		return len(owned), err
	}

	// Highest position first.
	for i, component := range slices.Backward(componentSet.Spec.Components) {
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
		break
	}
	return len(owned), nil
}

// terminatingName returns the name of an Application in applications that is being
// deleted, the first in name order, or "" when none is.
func terminatingName(applications map[string]*argocdv1alpha1.Application) string {
	for _, name := range sortedNames(applications) {
		if !applications[name].DeletionTimestamp.IsZero() {
			return name
		}
	}
	return ""
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
// liveApplications instead; teardown and removal need to see the terminating ones.
//
// A missing CRD is reported as an empty set rather than an error: Argo CD may not be
// installed, and applyComponents raises that properly a moment later.
func listApplications(
	ctx context.Context,
	c client.Reader,
	componentSet *cclusternvidiacomv1alpha1.CCLComponentSet,
) (map[string]*argocdv1alpha1.Application, error) {
	var list argocdv1alpha1.ApplicationList
	err := c.List(ctx, &list,
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

// applicationsCondition turns the outcome of applyComponents' writes into the
// ComponentsApplied condition. A missing CRD and a name collision are reported rather
// than returned: neither is fixed by retrying, and both are resolved by an event the
// controller already watches for.
func (r *CCLComponentSetReconciler) applicationsCondition(
	componentSet *cclusternvidiacomv1alpha1.CCLComponentSet,
	written int,
	err error,
) (status metav1.ConditionStatus, reason, message string, retryErr error) {
	switch {
	case err == nil:
		return metav1.ConditionTrue, cclusternvidiacomv1alpha1.CCLComponentSetReasonApplicationsCreated, fmt.Sprintf(
			"Applied %d of %d components in namespace %q",
			written, len(componentSet.Spec.Components), componentSet.Namespace), nil
	case apimeta.IsNoMatchError(err) || apierrors.IsNotFound(err):
		// Argo CD is not installed - `make deploy-cp ARGOCD=false`, or the operator
		// started first.
		return metav1.ConditionFalse, cclusternvidiacomv1alpha1.CCLComponentSetReasonApplicationCRDMissing, fmt.Sprintf(
			"Argo CD Application CRD not installed: %v", err), nil
	case errors.Is(err, errApplicationConflict):
		return metav1.ConditionFalse, cclusternvidiacomv1alpha1.CCLComponentSetReasonApplicationConflict, fmt.Sprintf(
			"An Application in namespace %q belongs to another CCLComponentSet",
			componentSet.Namespace), nil
	default:
		return metav1.ConditionFalse, cclusternvidiacomv1alpha1.CCLComponentSetReasonApplicationFailed, err.Error(), err
	}
}

// componentSyncStatuses builds status.componentSyncStatuses from observed Applications:
// one entry per component in the spec that has a live Application, in spec order, then
// one per prunable Application, in pruneOrder. Callers replace the field with the result,
// which drops stale observations and Applications that are gone.
func (r *CCLComponentSetReconciler) componentSyncStatuses(
	componentSet *cclusternvidiacomv1alpha1.CCLComponentSet,
	applications map[string]*argocdv1alpha1.Application,
	prunable map[string]*argocdv1alpha1.Application,
) []cclusternvidiacomv1alpha1.ComponentSyncStatus {
	statuses := make([]cclusternvidiacomv1alpha1.ComponentSyncStatus, 0,
		len(componentSet.Spec.Components)+len(prunable))
	for _, component := range componentSet.Spec.Components {
		application := applications[applicationName(componentSet.Name, component.Name)]
		if application == nil {
			// Omit components without a live Application.
			continue
		}
		statuses = append(statuses, cclusternvidiacomv1alpha1.ComponentSyncStatus{
			Name:                  component.Name,
			ComparedToMatchesSpec: comparedToMatchesSpec(application, r.applicationSpec(componentSet, component)),
			Sync:                  application.SyncStatus(),
			Message:               application.OperationMessage(),
		})
	}
	for _, name := range pruneOrder(prunable) {
		application := prunable[name]
		removal := cclusternvidiacomv1alpha1.ComponentRemovalDeletePending
		if !application.DeletionTimestamp.IsZero() {
			removal = cclusternvidiacomv1alpha1.ComponentRemovalDeleting
		}
		statuses = append(statuses, cclusternvidiacomv1alpha1.ComponentSyncStatus{
			Name:    componentName(componentSet.Name, name),
			Sync:    application.SyncStatus(),
			Message: application.OperationMessage(),
			Removal: removal,
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

// rolloutStatus reports how far through the components the rollout has got: the first
// component that has not finished holds up everything after it, and once every component
// has finished, the Applications of removed components hold up completion (see
// pruneNext). There is no timeout on these waits, so naming what holds the rollout up is
// the only way a stuck rollout is visible.
//
// invalidReason and invalidMessage are this pass's validation failure, empty when it
// passed. The operator writes and deletes no Application on such a pass, so a wait for
// the operator is reported under that reason, which names what actually holds it up.
// Waits on Argo CD are reported as usual: Argo CD keeps working on what was written.
func (r *CCLComponentSetReconciler) rolloutStatus(
	componentSet *cclusternvidiacomv1alpha1.CCLComponentSet,
	applications map[string]*argocdv1alpha1.Application,
	prunable map[string]*argocdv1alpha1.Application,
	invalidReason, invalidMessage string,
	healths componentHealths,
) (status metav1.ConditionStatus, reason, message string) {
	for i, component := range componentSet.Spec.Components {
		application := applications[applicationName(componentSet.Name, component.Name)]
		if r.finished(componentSet, component, application, healths) {
			continue
		}
		reason, message := r.componentWaitReason(componentSet, i+1, component, application,
			healths, invalidReason, invalidMessage)
		return metav1.ConditionTrue, reason, message
	}
	if len(prunable) > 0 {
		reason, message := pruneWaitReason(prunable, invalidReason, invalidMessage)
		return metav1.ConditionTrue, reason, message
	}
	return metav1.ConditionFalse, cclusternvidiacomv1alpha1.CCLComponentSetReasonRolloutComplete, fmt.Sprintf(
		"All %d components have synced the current spec and are healthy",
		len(componentSet.Spec.Components))
}

// componentWaitReason returns the Progressing reason and message for the component at
// position wave, which has not finished. application is nil when it has not been
// released.
func (r *CCLComponentSetReconciler) componentWaitReason(
	componentSet *cclusternvidiacomv1alpha1.CCLComponentSet,
	wave int,
	component cclusternvidiacomv1alpha1.ComponentSpec,
	application *argocdv1alpha1.Application,
	healths componentHealths,
	invalidReason, invalidMessage string,
) (reason, message string) {
	if application == nil {
		if invalidReason != "" {
			return invalidReason, fmt.Sprintf(
				"Wave %d (component %q) cannot be deployed until validation passes: %s",
				wave, component.Name, invalidMessage)
		}
		return cclusternvidiacomv1alpha1.CCLComponentSetReasonWaveSyncing, fmt.Sprintf(
			"Waiting to deploy wave %d (component %q)", wave, component.Name)
	}

	desired := r.applicationSpec(componentSet, component)
	switch {
	case !equality.Semantic.DeepEqual(application.Spec, desired) && invalidReason != "":
		// Only an edit to the CCLComponentSet or its CCLInfraPlanes, both watched,
		// lets the operator write this spec.
		return invalidReason, fmt.Sprintf(
			"Wave %d (component %q) cannot be written until validation passes: %s",
			wave, component.Name, invalidMessage)

	case !equality.Semantic.DeepEqual(application.Spec, desired):
		// The list behind this report predates this reconcile's write. The write's
		// own event brings the CCLComponentSet back.
		return cclusternvidiacomv1alpha1.CCLComponentSetReasonWaveSyncing, fmt.Sprintf(
			"Wave %d (component %q) is waiting for the operator to write the current spec",
			wave, component.Name)

	case !comparedToMatchesSpec(application, desired):
		// The latest comparison does not match the desired source, destination and
		// ignoreDifferences, so its health and sync statuses cannot establish
		// completion.
		return cclusternvidiacomv1alpha1.CCLComponentSetReasonWaveSyncing, withRunningOperationMessage(fmt.Sprintf(
			"Wave %d (component %q) is waiting for Argo CD to compare the spec the "+
				"operator wrote", wave, component.Name), application)

	case application.OperationInProgress():
		// Hooks are excluded from aggregate health and sync. Healthy/Synced can
		// therefore precede operation completion, including a pending PostSync hook.
		return cclusternvidiacomv1alpha1.CCLComponentSetReasonWaveSyncing, withRunningOperationMessage(fmt.Sprintf(
			"Wave %d (component %q) is waiting for an Argo CD operation to complete",
			wave, component.Name), application)

	case currentSyncFailed(application, desired):
		// A failed hook can leave aggregate health Healthy and sync Synced.
		// A later successful sync of this source and revision resumes the rollout.
		return cclusternvidiacomv1alpha1.CCLComponentSetReasonWaveSyncFailed, fmt.Sprintf(
			"Wave %d (component %q) failed to sync: %s. A successful sync of "+
				"Application %q in Argo CD resumes the rollout",
			wave, component.Name, application.OperationMessage(), application.Name)

	default:
		// Argo CD compared this spec, and either has not synced it or the component
		// is not healthy. The health message says which object holds it up.
		health := healths.of(component.Name, application)
		message := fmt.Sprintf("Wave %d (component %q) has health %q, sync %q", wave, component.Name,
			health.Status, application.SyncStatus())
		if health.Message != "" {
			message += ": " + health.Message
		}
		return cclusternvidiacomv1alpha1.CCLComponentSetReasonWaveSyncing, message
	}
}

// pruneWaitReason returns the Progressing reason and message while the Applications in
// prunable are not all gone. They go one at a time (see pruneNext), so a terminating one
// is the one being waited on; otherwise it is the next to delete.
func pruneWaitReason(
	prunable map[string]*argocdv1alpha1.Application,
	invalidReason, invalidMessage string,
) (reason, message string) {
	if terminating := terminatingName(prunable); terminating != "" {
		return cclusternvidiacomv1alpha1.CCLComponentSetReasonComponentRemoving, fmt.Sprintf(
			"Waiting for Argo CD to delete Application %q, whose component is no longer "+
				"in the spec", terminating)
	}
	name := pruneOrder(prunable)[0]
	if invalidReason != "" {
		return invalidReason, fmt.Sprintf(
			"Application %q, whose component is no longer in the spec, cannot be deleted "+
				"until validation passes: %s", name, invalidMessage)
	}
	// The list behind this report predates this reconcile's delete. The delete's own
	// event brings the CCLComponentSet back.
	return cclusternvidiacomv1alpha1.CCLComponentSetReasonComponentRemoving, fmt.Sprintf(
		"Waiting to delete Application %q, whose component is no longer in the spec", name)
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
