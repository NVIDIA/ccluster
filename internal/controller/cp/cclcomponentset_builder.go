/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/google/uuid"

	"k8s.io/apimachinery/pkg/api/equality"
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

// errApplicationSyncInProgress reports that a spec write was held back because Argo CD
// already owes this Application an operation. The write carries a sync request, and
// writing one over an operation Argo CD has not picked up would lose it. Not a failure:
// reconcileApplications turns it into a wait, and the requeue brings the write back.
var errApplicationSyncInProgress = errors.New("application has a pending or running operation")

// applicationName is the name Argo CD gives the Application for a component.
func applicationName(componentSetName, componentName string) string {
	return componentSetName + "-" + componentName
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
			// Never switched off. The operator asks for a sync when it rewrites the
			// spec, but it never gates one, which is what keeps selfHeal working.
			// Argo CD's own RollingSync strategy is deliberately unused
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

// specEqual reports whether an Application's stored spec is still the one applicationSpec
// builds. It is the comparison behind every decision in this package: whether to ask
// Argo CD for a sync, and whether a recorded result still describes what is wanted.
//
// Helm values are apiextensionsv1.JSON, which equality.Semantic compares as raw bytes, so
// two spellings of the same document - a different key order, 3 against 3.0 - read as a
// change. That would be a new request on every reconcile, and a component that never
// reaches finished, because the same comparison gates both.
//
// The byte comparison settles it whenever the two are identical, which is the usual case.
// Decoding only runs when they differ, and a difference is either a real spec change or
// the formatting case this exists to absorb. Neither is frequent.
func specEqual(a, b argocdv1alpha1.ApplicationSpec) bool {
	if equality.Semantic.DeepEqual(a, b) {
		return true
	}
	aValues, bValues := decodedHelmValues(a.Source), decodedHelmValues(b.Source)
	// Compared apart, so the bytes underneath do not decide the rest of the spec. These
	// are copies: ApplicationSpec holds Source by value.
	a.Source.Helm, b.Source.Helm = nil, nil
	return equality.Semantic.DeepEqual(a, b) && equality.Semantic.DeepEqual(aValues, bValues)
}

// decodedHelmValues parses a component's values so that two spellings of one document
// compare equal. Values that are not valid JSON cannot reach here through the API server;
// if they ever do, the raw bytes stand in and that spec compares byte for byte as before.
func decodedHelmValues(source argocdv1alpha1.ApplicationSource) any {
	if source.Helm == nil || source.Helm.ValuesObject == nil {
		return nil
	}
	var values any
	if err := json.Unmarshal(source.Helm.ValuesObject.Raw, &values); err != nil {
		return string(source.Helm.ValuesObject.Raw)
	}
	return values
}

// readApplicationForWrite returns the Application a write is built on, and is the only
// place that decides which read to trust.
//
// The informer cache serves it, except when the request identity on what it returns
// disagrees with the one the CCLComponentSet recorded. A cached annotation older than the
// record reads as a request to recover, and recovering it would discard the newer one. The
// API server settles which half is stale.
//
// The caller asks untrackedRequest and recreatedApplication again, of the object this
// returns. That is not a repeat: the answers here decided whether to read again, and the
// answers there are the ones the write is built on.
func (r *CCLComponentSetReconciler) readApplicationForWrite(
	ctx context.Context, key client.ObjectKey, record *cclusternvidiacomv1alpha1.ComponentSyncStatus,
) (*argocdv1alpha1.Application, error) {
	// A fresh object every call: Get decodes into whatever it is given, so reusing one
	// would leave its stamp behind for before to copy.
	application := &argocdv1alpha1.Application{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
	}
	if err := r.Get(ctx, key, application); client.IgnoreNotFound(err) != nil {
		return nil, err
	}

	inDoubt := untrackedRequest(application, record) || recreatedApplication(application, record)
	if application.ResourceVersion == "" || r.APIReader == nil || !inDoubt {
		return application, nil
	}

	fresh := &argocdv1alpha1.Application{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
	}
	if err := r.APIReader.Get(ctx, key, fresh); client.IgnoreNotFound(err) != nil {
		return nil, err
	}
	return fresh, nil
}

// createOrPatchApplication brings one component's Application in line with the spec, and
// asks Argo CD to sync it when that spec changed.
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
		record := syncRecord(componentSet, component.Name)
		application, err := r.readApplicationForWrite(ctx, key, record)
		if err != nil {
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

		desired := r.applicationSpec(componentSet, component)
		specChanged := !specEqual(before.Spec, desired)
		newRequest := specChanged || recreatedApplication(application, record)
		if newRequest && before.OperationInProgress() {
			return errApplicationSyncInProgress
		}
		application.Spec = desired

		// Which request this write carries. The annotation and the record are the two
		// halves of that, and each case is one way they can be out of step.
		switch {
		case newRequest:
			// Nothing judged so far applies to what is being written. Both halves start over.
			application.Annotations[annotationSyncRequestID] = uuid.NewString()
			record = &cclusternvidiacomv1alpha1.ComponentSyncStatus{Name: component.Name}
		case untrackedRequest(application, record):
			// The Application half survived a crash, or a CCLComponentSet status write that
			// failed after this request was already committed. Rebuild the record from it.
			record = &cclusternvidiacomv1alpha1.ComponentSyncStatus{
				Name: component.Name, ApplicationUID: string(application.UID),
				RequestID: application.Annotations[annotationSyncRequestID],
			}
		case record != nil && application.Annotations[annotationSyncRequestID] == "":
			// The annotation was removed by hand. The record is this Application's - the
			// cases above rule out the alternatives - so put it back without forgetting
			// any result already observed against it.
			application.Annotations[annotationSyncRequestID] = record.RequestID
		}
		observeSync(application, record)

		var extra []patchPath
		if newRequest {
			application.Operation = newOperation(
				desired, application.Annotations[annotationSyncRequestID])
			extra = append(extra, patchPath{path: "/operation", value: application.Operation})
		}

		result, err = createOrPatchArgoCDObject(
			ctx, r.Client, before, application, application.Spec, extra...)
		if err == nil && record != nil {
			rememberRequest(componentSet, application, record)
		}
		return err
	})
	return result, err
}

// newOperation requests a sync of this spec's source under the given request ID.
//
// The spec and the ID are parameters rather than read back off the Application, because
// reading them back would mean this had to be called after both were set on it, and
// nothing said so. Reading the spec back would also dereference
// syncPolicy.automated, which only applicationSpec guarantees is there.
func newOperation(
	spec argocdv1alpha1.ApplicationSpec, requestID string,
) *argocdv1alpha1.Operation {
	sync := &argocdv1alpha1.SyncOperation{Source: spec.Source.DeepCopy()}

	// Argo CD reads neither of these from spec.syncPolicy for a sync it was asked for -
	// the one it starts itself copies them across field by field - so they are repeated
	// here. A request without ServerSideApply syncs, and then never converges.
	if spec.SyncPolicy != nil {
		sync.SyncOptions = spec.SyncPolicy.SyncOptions
		if spec.SyncPolicy.Automated != nil {
			sync.Prune = spec.SyncPolicy.Automated.Prune
		}
	}

	return &argocdv1alpha1.Operation{
		Sync: sync,
		// Not automated: Argo CD re-resolves the revision after a sync it did not start
		// itself, which keeps a just-synced Application from reporting OutOfSync.
		InitiatedBy: argocdv1alpha1.OperationInitiator{Username: labelManagedByValue},
		Info:        []argocdv1alpha1.Info{{Name: annotationSyncRequestID, Value: requestID}},
		Retry:       argocdv1alpha1.RetryStrategy{Limit: syncRetryLimit},
	}
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
) (changed, waiting bool, err error) {
	for i, component := range componentSet.Spec.Components[:released] {
		op, err := r.createOrPatchApplication(ctx, componentSet, component, i+1)
		if errors.Is(err, errApplicationSyncInProgress) {
			waiting = true
			break
		}
		if err != nil {
			return changed, waiting, err
		}
		if op != controllerutil.OperationResultNone {
			changed = true
		}
	}

	pruned, err := r.pruneApplications(ctx, componentSet, existing)
	return changed || pruned, waiting, err
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
		if r.finished(componentSet, component, application) {
			continue
		}

		phase, operationMessage := r.currentSyncPhase(componentSet, component, application)
		switch phase {
		case argocdv1alpha1.OperationFailed, argocdv1alpha1.OperationError:
			// Not retried by the operator. Any later successful sync of this spec is adopted
			// - see adoptLaterResult - so the message says how to start one.
			return metav1.ConditionTrue, reasonWaveSyncFailed, fmt.Sprintf(
				"Wave %d (component %q) failed to sync: %s. A successful sync of Application "+
					"%q in Argo CD resumes the rollout",
				wave, component.Name, operationMessage, name)
		case argocdv1alpha1.OperationSucceeded:
			if !healthObservedAfterSync(application, syncRecord(componentSet, component.Name)) {
				return metav1.ConditionTrue, reasonWaveSyncing, fmt.Sprintf(
					"Wave %d (component %q) is waiting for Argo CD to reassess health after syncing", wave, component.Name)
			}
			// Argo CD synced this spec and is unhappy with what it deployed. Which resource
			// is unhappy is in status.resources, which this type does not model.
			return metav1.ConditionTrue, reasonWaveSyncing, fmt.Sprintf(
				"Wave %d (component %q) has health %q, sync %q", wave, component.Name,
				application.HealthStatus(), application.SyncStatus())
		default:
			waiting := fmt.Sprintf(
				"Wave %d (component %q) is waiting for Argo CD to sync the spec the operator "+
					"wrote", wave, component.Name)
			// Argo CD holds a failing sync in Running while it retries, and says which
			// attempt it is on in the operation message. Without it a sync failing on
			// every attempt reads exactly like a slow one.
			if operationMessage != "" {
				waiting += ": " + operationMessage
			}
			return metav1.ConditionTrue, reasonWaveSyncing, waiting
		}
	}

	return metav1.ConditionFalse, reasonRolloutComplete, fmt.Sprintf(
		"All %d components have synced the current spec and are healthy",
		len(componentSet.Spec.Components))
}
