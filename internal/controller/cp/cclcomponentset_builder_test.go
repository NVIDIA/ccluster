/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

// Plain table-driven tests over the pure rollout logic. These deliberately do not use
// Ginkgo or envtest: BeforeSuite only runs under RunSpecs, so `go test -run TestReleaseCount`
// needs no KUBEBUILDER_ASSETS and no etcd. That matters because the interesting states of
// releaseCount are combinatorial, and several of them - a gap in the middle of the wave
// sequence, most of all - cannot be staged through a reconciler that never creates
// Applications out of order.

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/go-logr/logr"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
	argocdv1alpha1 "github.com/NVIDIA/ccluster/internal/argocd/v1alpha1"
)

const testComponentSetName = "test-componentset"

// componentSetWith builds a CCLComponentSet whose components are named, in order, by names.
func componentSetWith(names ...string) *cclusternvidiacomv1alpha1.CCLComponentSet {
	componentSet := &cclusternvidiacomv1alpha1.CCLComponentSet{
		ObjectMeta: metav1.ObjectMeta{Name: testComponentSetName, Namespace: "default"},
	}
	for _, name := range names {
		componentSet.Spec.Components = append(componentSet.Spec.Components,
			cclusternvidiacomv1alpha1.ComponentSpec{
				Name:          name,
				InfraPlaneRef: cclusternvidiacomv1alpha1.LocalObjectReference{Name: "plane-a"},
				ComponentEntry: cclusternvidiacomv1alpha1.ComponentEntry{
					Chart:     testChart(name),
					Namespace: name,
				},
			})
	}
	return componentSet
}

// testChart is the catalog path componentSetWith gives a component.
func testChart(component string) string {
	return "catalogs/cluster_base/" + component
}

// app builds an Application in the state Argo CD would report. A health of "" stands for
// an Application Argo CD has not written a status to yet.
func app(health, sync string) *argocdv1alpha1.Application {
	if health == "" && sync == "" {
		return &argocdv1alpha1.Application{}
	}
	return &argocdv1alpha1.Application{
		Status: &argocdv1alpha1.ApplicationStatus{
			Health: argocdv1alpha1.ApplicationHealthStatus{Status: health},
			Sync:   argocdv1alpha1.ApplicationSyncStatus{Status: sync},
		},
	}
}

func healthyApp() *argocdv1alpha1.Application {
	return app(argocdv1alpha1.HealthStatusHealthy, argocdv1alpha1.SyncStatusSynced)
}

const (
	testGeneration     = "7"
	syncFailureMessage = "one or more objects failed to apply"
)

// stamped is an Application the operator recorded testGeneration on, carrying the phase
// Argo CD reported for the sync it ran for that generation. An empty phase is an
// Application Argo CD has not synced since the operator wrote that generation.
func stamped(health, sync, phase string) *argocdv1alpha1.Application {
	application := app(health, sync)
	application.Annotations = map[string]string{annotationComponentSetGeneration: testGeneration}
	if phase == "" {
		return application
	}
	if application.Status == nil {
		application.Status = &argocdv1alpha1.ApplicationStatus{}
	}
	application.Status.OperationState = &argocdv1alpha1.OperationState{
		Operation: argocdv1alpha1.Operation{Info: []argocdv1alpha1.Info{{
			Name:  annotationComponentSetGeneration,
			Value: testGeneration,
		}}},
		Phase: phase,
	}
	if phase != argocdv1alpha1.OperationSucceeded {
		application.Status.OperationState.Message = syncFailureMessage
	}
	return application
}

// applications keys apps by the Application name the reconciler would give them.
func applications(byComponent map[string]*argocdv1alpha1.Application) map[string]*argocdv1alpha1.Application {
	out := make(map[string]*argocdv1alpha1.Application, len(byComponent))
	for component, application := range byComponent {
		out[applicationName(testComponentSetName, component)] = application
	}
	return out
}

func TestReleaseCount(t *testing.T) {
	tests := []struct {
		name       string
		components []string
		existing   map[string]*argocdv1alpha1.Application
		want       int
	}{
		{
			name:       "holds while a released component has no sync for its generation",
			components: []string{"a", "b"},
			existing: applications(map[string]*argocdv1alpha1.Application{
				"a": stamped(argocdv1alpha1.HealthStatusHealthy, argocdv1alpha1.SyncStatusSynced, ""),
			}),
			want: 1,
		},
		{
			name:       "releases only the first component when nothing is deployed",
			components: []string{"a", "b", "c"},
			existing:   nil,
			want:       1,
		},
		{
			name:       "holds while the leading component is still converging",
			components: []string{"a", "b"},
			existing: applications(map[string]*argocdv1alpha1.Application{
				"a": app(argocdv1alpha1.HealthStatusProgressing, "OutOfSync"),
			}),
			want: 1,
		},
		{
			name:       "advances one component once the released ones are healthy and synced",
			components: []string{"a", "b", "c"},
			existing: applications(map[string]*argocdv1alpha1.Application{
				"a": healthyApp(),
			}),
			want: 2,
		},
		{
			// Regressing this deletes a running Application and, through the Argo CD
			// cascade finalizer, uninstalls its chart.
			name:       "never withdraws a released component that later degrades",
			components: []string{"a", "b", "c"},
			existing: applications(map[string]*argocdv1alpha1.Application{
				"a": app(argocdv1alpha1.HealthStatusDegraded, argocdv1alpha1.SyncStatusSynced),
				"b": healthyApp(),
				"c": healthyApp(),
			}),
			want: 3,
		},
		{
			// An early degrade stops the rollout advancing, which is what a sequential
			// dependency implies - but see the case above: it does not withdraw.
			name:       "stops advancing when an early component degrades after later waves released",
			components: []string{"a", "b", "c"},
			existing: applications(map[string]*argocdv1alpha1.Application{
				"a": app(argocdv1alpha1.HealthStatusDegraded, argocdv1alpha1.SyncStatusSynced),
				"b": healthyApp(),
			}),
			want: 2,
		},
		{
			// Gap in the middle: the leading-run scan must stop at "b" and never count
			// "c". Unreachable through the reconciler, which never creates out of order.
			name:       "counts only the leading run, so a stray later Application does not advance the rollout",
			components: []string{"a", "b", "c"},
			existing: applications(map[string]*argocdv1alpha1.Application{
				"a": healthyApp(),
				"c": healthyApp(),
			}),
			want: 2,
		},
		{
			name:       "requires Synced as well as Healthy",
			components: []string{"a", "b"},
			existing: applications(map[string]*argocdv1alpha1.Application{
				"a": app(argocdv1alpha1.HealthStatusHealthy, "OutOfSync"),
			}),
			want: 1,
		},
		{
			name:       "requires Healthy as well as Synced",
			components: []string{"a", "b"},
			existing: applications(map[string]*argocdv1alpha1.Application{
				"a": app(argocdv1alpha1.HealthStatusDegraded, argocdv1alpha1.SyncStatusSynced),
			}),
			want: 1,
		},
		{
			name:       "treats an Application Argo CD has not written a status to as not healthy",
			components: []string{"a", "b"},
			existing: applications(map[string]*argocdv1alpha1.Application{
				"a": app("", ""),
			}),
			want: 1,
		},
		{
			name:       "stops at the last component",
			components: []string{"a", "b"},
			existing: applications(map[string]*argocdv1alpha1.Application{
				"a": healthyApp(),
				"b": healthyApp(),
			}),
			want: 2,
		},
		{
			// Guarded in the API by MinItems=1. Before the clamp in releaseCount this
			// returned 1, and reconcileApplications then sliced Components[:1] on an
			// empty slice and panicked.
			name:       "returns zero for a spec with no components",
			components: nil,
			existing:   nil,
			want:       0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := releaseCount(componentSetWith(test.components...), test.existing)
			if got != test.want {
				t.Errorf("releaseCount() = %d, want %d", got, test.want)
			}
		})
	}
}

// TestReleaseCountIgnoresTerminatingApplications composes releaseCount with
// liveApplications, which is how Reconcile calls it. A component whose Application is
// being deleted must not read as already released, or a re-added component is never
// recreated.
func TestReleaseCountIgnoresTerminatingApplications(t *testing.T) {
	deleting := healthyApp()
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	deleting.Finalizers = []string{argocdv1alpha1.ResourcesFinalizer}

	existing := applications(map[string]*argocdv1alpha1.Application{
		"a": healthyApp(),
		"b": deleting,
	})

	if got := releaseCount(componentSetWith("a", "b", "c"), existing); got != 3 {
		t.Errorf("releaseCount() over all Applications = %d, want 3", got)
	}
	if got := releaseCount(componentSetWith("a", "b", "c"), liveApplications(existing)); got != 2 {
		t.Errorf("releaseCount() over live Applications = %d, want 2", got)
	}
}

// TestReleaseCountEmptySpecDoesNotPanic pins the reason for the clamp: the value
// releaseCount returns is used to slice the component list.
func TestReleaseCountEmptySpecDoesNotPanic(t *testing.T) {
	componentSet := componentSetWith()
	released := releaseCount(componentSet, nil)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("slicing Components[:%d] panicked: %v", released, r)
		}
	}()
	_ = componentSet.Spec.Components[:released]
}

// TestNeedsSync covers the decision that keeps the operator from asking Argo CD for a
// sync on every reconcile. Argo CD clears the request when the sync ends and the
// Application watch brings the operator straight back, so a branch that wrongly returns
// true is a sync loop at watch speed. No envtest spec can catch one: the stand-in there
// clears a request only when the test tells it to, and a rollout driven that way finishes
// either way.
func TestNeedsSync(t *testing.T) {
	// The last sync on this Application is one Argo CD started itself - a self-heal, say.
	// Those carry no info, so the recorded generation does not come back.
	selfHealed := func() *argocdv1alpha1.Application {
		application := stamped(argocdv1alpha1.HealthStatusHealthy,
			argocdv1alpha1.SyncStatusSynced, argocdv1alpha1.OperationSucceeded)
		application.Status.OperationState.Operation.Info = nil
		return application
	}

	// Recorded, then stripped by hand. The generation comes back from a sync the operator
	// no longer expects, which is the one state where the annotation and the sync result
	// disagree in this direction.
	stripped := func() *argocdv1alpha1.Application {
		application := stamped(argocdv1alpha1.HealthStatusHealthy,
			argocdv1alpha1.SyncStatusSynced, argocdv1alpha1.OperationSucceeded)
		delete(application.Annotations, annotationComponentSetGeneration)
		return application
	}

	tests := []struct {
		name        string
		application *argocdv1alpha1.Application
		specChanged bool
		want        bool
	}{
		{
			name:        "asks for a spec this write is the first to carry",
			application: &argocdv1alpha1.Application{},
			specChanged: true,
			want:        true,
		},
		{
			// The upgrade path: recording starts with this Application's next spec
			// change, so restarting the operator must not sync every component at once.
			name:        "leaves an Application written before generations were recorded alone",
			application: healthyApp(),
			want:        false,
		},
		{
			// Without the annotation there is nothing to ask for: newOperation would send
			// an empty generation, which can never come back as a match.
			name:        "leaves an Application whose recorded generation was stripped alone",
			application: stripped(),
			want:        false,
		},
		{
			name: "does not ask again once Argo CD has taken the request",
			application: stamped(argocdv1alpha1.HealthStatusHealthy,
				argocdv1alpha1.SyncStatusSynced, argocdv1alpha1.OperationSucceeded),
			want: false,
		},
		{
			name: "does not ask again while the sync it took is still running",
			application: stamped(argocdv1alpha1.HealthStatusHealthy,
				argocdv1alpha1.SyncStatusSynced, "Running"),
			want: false,
		},
		{
			// A failed sync is reported, not retried: Argo CD has already retried it to
			// its limit, and asking again would retry it forever.
			name: "does not ask again for a sync it took and failed",
			application: stamped(argocdv1alpha1.HealthStatusHealthy,
				argocdv1alpha1.SyncStatusSynced, argocdv1alpha1.OperationFailed),
			want: false,
		},
		{
			name: "does not replace a request Argo CD has not picked up yet",
			application: func() *argocdv1alpha1.Application {
				application := selfHealed()
				application.Operation = &argocdv1alpha1.Operation{}
				return application
			}(),
			want: false,
		},
		{
			// Argo CD was busy with a sync of its own when the request arrived and cleared
			// it along with that one, so the generation never came back.
			name:        "asks again when Argo CD dropped the request",
			application: selfHealed(),
			want:        true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := needsSync(test.application, test.specChanged); got != test.want {
				t.Errorf("needsSync() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestApplicationsCondition(t *testing.T) {
	reconciler := &CCLComponentSetReconciler{}
	componentSet := componentSetWith("a", "b")

	noMatch := &apimeta.NoKindMatchError{
		GroupKind: schema.GroupKind{Group: "argoproj.io", Kind: "Application"},
	}
	notFound := apierrors.NewNotFound(schema.GroupResource{Group: "argoproj.io", Resource: "applications"}, "x")
	boom := errors.New("boom")

	tests := []struct {
		name        string
		released    int
		err         error
		wantStatus  metav1.ConditionStatus
		wantReason  string
		wantRetry   bool
		wantMessage string
	}{
		{
			name:        "reports the applied count out of the total on success",
			released:    1,
			err:         nil,
			wantStatus:  metav1.ConditionTrue,
			wantReason:  reasonApplicationsCreated,
			wantMessage: "Applied 1 of 2 components",
		},
		{
			// Argo CD is not installed. Retrying does not fix it; the controller
			// already watches for the CRD appearing.
			name:       "reports ApplicationCRDMissing for a NoMatch error without asking for a retry",
			err:        noMatch,
			wantStatus: metav1.ConditionFalse,
			wantReason: reasonApplicationCRDMissing,
		},
		{
			name:       "reports ApplicationCRDMissing for a NotFound error",
			err:        notFound,
			wantStatus: metav1.ConditionFalse,
			wantReason: reasonApplicationCRDMissing,
		},
		{
			// Returning the error here would hot-loop on a condition no retry can fix.
			name:       "reports ApplicationConflict without asking for a retry",
			err:        errApplicationConflict,
			wantStatus: metav1.ConditionFalse,
			wantReason: reasonApplicationConflict,
		},
		{
			// errors.Is, not ==: a refactor to == would pass every other case here.
			name:       "reports ApplicationConflict for a wrapped conflict",
			err:        fmt.Errorf("updating Application: %w", errApplicationConflict),
			wantStatus: metav1.ConditionFalse,
			wantReason: reasonApplicationConflict,
		},
		{
			name:       "returns an unclassified error so the request is retried",
			err:        boom,
			wantStatus: metav1.ConditionFalse,
			wantReason: reasonApplicationFailed,
			wantRetry:  true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, reason, message, retryErr := reconciler.applicationsCondition(
				componentSet, test.released, test.err)

			if status != test.wantStatus {
				t.Errorf("status = %q, want %q", status, test.wantStatus)
			}
			if reason != test.wantReason {
				t.Errorf("reason = %q, want %q", reason, test.wantReason)
			}
			if test.wantRetry && !errors.Is(retryErr, test.err) {
				t.Errorf("retryErr = %v, want it to wrap %v", retryErr, test.err)
			}
			if !test.wantRetry && retryErr != nil {
				t.Errorf("retryErr = %v, want nil so the reconciler does not hot-loop", retryErr)
			}
			if test.wantMessage != "" && !strings.Contains(message, test.wantMessage) {
				t.Errorf("message = %q, want it to contain %q", message, test.wantMessage)
			}
		})
	}
}

func TestRolloutStatus(t *testing.T) {
	reconciler := &CCLComponentSetReconciler{}

	tests := []struct {
		name        string
		components  []string
		existing    map[string]*argocdv1alpha1.Application
		wantStatus  metav1.ConditionStatus
		wantReason  string
		wantMessage string
		notMessage  string
	}{
		{
			name:       "names the first component that has no Application yet",
			components: []string{"a", "b"},
			existing:   nil,
			// Progressing is True while the rollout is still going.
			wantStatus:  metav1.ConditionTrue,
			wantReason:  reasonWaveSyncing,
			wantMessage: `Waiting to deploy wave 1 (component "a")`,
		},
		{
			name:       "skips healthy components to name the first one still converging",
			components: []string{"a", "b"},
			existing: applications(map[string]*argocdv1alpha1.Application{
				"a": healthyApp(),
				"b": app(argocdv1alpha1.HealthStatusProgressing, "OutOfSync"),
			}),
			wantStatus:  metav1.ConditionTrue,
			wantReason:  reasonWaveSyncing,
			wantMessage: `Wave 2 (component "b") has health "Progressing", sync "OutOfSync"`,
		},
		{
			// Healthy and Synced here describe the spec before the one the operator
			// wrote, so the message must not repeat them as if they were current.
			name:       "waits when Argo CD has not synced the spec the operator wrote",
			components: []string{"a"},
			existing: applications(map[string]*argocdv1alpha1.Application{
				"a": stamped(argocdv1alpha1.HealthStatusHealthy, argocdv1alpha1.SyncStatusSynced, ""),
			}),
			wantStatus:  metav1.ConditionTrue,
			wantReason:  reasonWaveSyncing,
			wantMessage: `Wave 1 (component "a") is waiting for Argo CD to sync`,
			notMessage:  "health",
		},
		{
			name:       "waits while the sync it asked for is still running",
			components: []string{"a"},
			existing: applications(map[string]*argocdv1alpha1.Application{
				"a": stamped(argocdv1alpha1.HealthStatusHealthy,
					argocdv1alpha1.SyncStatusSynced, "Running"),
			}),
			wantStatus:  metav1.ConditionTrue,
			wantReason:  reasonWaveSyncing,
			wantMessage: "is waiting for Argo CD to sync",
		},
		{
			name:       "reports a failed sync apart from a slow one",
			components: []string{"a"},
			existing: applications(map[string]*argocdv1alpha1.Application{
				"a": stamped(argocdv1alpha1.HealthStatusHealthy,
					argocdv1alpha1.SyncStatusSynced, argocdv1alpha1.OperationFailed),
			}),
			wantStatus:  metav1.ConditionTrue,
			wantReason:  reasonWaveSyncFailed,
			wantMessage: `Wave 1 (component "a") failed to sync: ` + syncFailureMessage,
		},
		{
			name:       "names the health verdict once the sync it asked for succeeded",
			components: []string{"a"},
			existing: applications(map[string]*argocdv1alpha1.Application{
				"a": stamped(argocdv1alpha1.HealthStatusDegraded, "OutOfSync",
					argocdv1alpha1.OperationSucceeded),
			}),
			wantStatus:  metav1.ConditionTrue,
			wantReason:  reasonWaveSyncing,
			wantMessage: `has health "Degraded", sync "OutOfSync"`,
		},
		{
			name:       "reports an Application with no status as still converging",
			components: []string{"a"},
			existing: applications(map[string]*argocdv1alpha1.Application{
				"a": app("", ""),
			}),
			wantStatus: metav1.ConditionTrue,
			wantReason: reasonWaveSyncing,
		},
		{
			// Progressing flips False only when everything is done. A refactor that
			// "corrects" this polarity inverts the whole condition.
			name:       "reports complete when every component is healthy and synced",
			components: []string{"a", "b"},
			existing: applications(map[string]*argocdv1alpha1.Application{
				// Recorded, and written before the operator recorded generations.
				"a": stamped(argocdv1alpha1.HealthStatusHealthy,
					argocdv1alpha1.SyncStatusSynced, argocdv1alpha1.OperationSucceeded),
				"b": healthyApp(),
			}),
			wantStatus:  metav1.ConditionFalse,
			wantReason:  reasonRolloutComplete,
			wantMessage: "All 2 components have synced the current spec and are healthy",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, reason, message := reconciler.rolloutStatus(
				componentSetWith(test.components...), test.existing)

			if status != test.wantStatus {
				t.Errorf("status = %q, want %q", status, test.wantStatus)
			}
			if reason != test.wantReason {
				t.Errorf("reason = %q, want %q", reason, test.wantReason)
			}
			if test.wantMessage != "" && !strings.Contains(message, test.wantMessage) {
				t.Errorf("message = %q, want it to contain %q", message, test.wantMessage)
			}
			if test.notMessage != "" && strings.Contains(message, test.notMessage) {
				t.Errorf("message = %q, want it not to contain %q", message, test.notMessage)
			}
		})
	}
}

// TestApplicationNameCollidesAcrossComponentSets is a regression guard, not a test of
// string concatenation. applicationName does no escaping, and neither the DNS-1123
// pattern on component names nor the 63-character CEL rule on the CCLComponentSet name
// prevents two different componentSets mapping onto one Application. The only thing that
// stops the second one clobbering the first is the ownedBy check in
// createOrPatchApplication, which reports errApplicationConflict. If this test is ever
// "simplified away", so is the reason that check exists.
func TestApplicationNameCollidesAcrossComponentSets(t *testing.T) {
	if applicationName("a-b", "c") != applicationName("a", "b-c") {
		t.Fatal("expected the documented collision; if this now passes, the ownership " +
			"conflict check may no longer be the only guard")
	}
}

// TestApplicationSpecOmitsHelmWhenValuesAbsent covers the len(Raw) > 0 guard. The
// values-present path is asserted by the envtest suite; this is the branch that is easy
// to drop, and dropping it emits an empty helm block on every valueless component.
func TestApplicationSpecOmitsHelmWhenValuesAbsent(t *testing.T) {
	reconciler := &CCLComponentSetReconciler{
		CatalogRepoURL:  "https://github.com/NVIDIA/ccluster",
		CatalogRevision: "main",
	}

	tests := []struct {
		name   string
		values *apiextensionsv1.JSON
	}{
		{name: "nil values", values: nil},
		{name: "values present but empty", values: &apiextensionsv1.JSON{Raw: []byte{}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := reconciler.applicationSpec(componentSetWith("a"), cclusternvidiacomv1alpha1.ComponentSpec{
				Name:          "a",
				InfraPlaneRef: cclusternvidiacomv1alpha1.LocalObjectReference{Name: "plane-a"},
				ComponentEntry: cclusternvidiacomv1alpha1.ComponentEntry{
					Chart:     "catalogs/cluster_base/a",
					Namespace: "a",
					Values:    test.values,
				},
			})
			if spec.Source.Helm != nil {
				t.Errorf("Source.Helm = %+v, want nil", spec.Source.Helm)
			}
		})
	}
}

// TestArgoCDKindInstalled covers the startup decision that guards the Application and
// AppProject watches. It matters more than its size suggests: registering a watch when
// its CRD is absent stops the whole controller from ever starting, because
// controller-runtime waits for every watch source to sync and a source for an unserved
// kind never does.
func TestArgoCDKindInstalled(t *testing.T) {
	applicationGK := argocdv1alpha1.GroupVersion.WithKind("Application").GroupKind()

	installed := apimeta.NewDefaultRESTMapper([]schema.GroupVersion{argocdv1alpha1.GroupVersion})
	installed.Add(argocdv1alpha1.GroupVersion.WithKind("Application"), apimeta.RESTScopeNamespace)

	tests := []struct {
		name   string
		mapper apimeta.RESTMapper
		want   bool
	}{
		{
			name:   "registers the watch when the CRD is served",
			mapper: installed,
			want:   true,
		},
		{
			// make deploy-cp ARGOCD=false, or the operator started before Argo CD.
			name:   "skips the watch when the CRD is absent",
			mapper: apimeta.NewDefaultRESTMapper(nil),
			want:   false,
		},
		{
			// Discovery failing is not evidence the CRD is missing. Dropping the watch
			// here would silently disable it for the lifetime of the process, so the
			// safe answer is to register it and let the manager retry.
			name:   "registers the watch when discovery fails outright",
			mapper: errorRESTMapper{err: errors.New("connection refused")},
			want:   true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := argoCDKindInstalled(test.mapper, logr.Discard(), "Application"); got != test.want {
				t.Errorf("argoCDKindInstalled() = %v, want %v", got, test.want)
			}
		})
	}

	// Guard the branch discrimination itself: a NoKindMatchError must not be mistaken
	// for a transport error, or the absent case silently starts registering the watch.
	noMatch := &apimeta.NoKindMatchError{GroupKind: applicationGK}
	if !apimeta.IsNoMatchError(noMatch) {
		t.Fatal("expected NoKindMatchError to satisfy IsNoMatchError")
	}
}

// errorRESTMapper fails every lookup with a non-NoMatch error.
type errorRESTMapper struct {
	apimeta.RESTMapper
	err error
}

func (m errorRESTMapper) RESTMapping(schema.GroupKind, ...string) (*apimeta.RESTMapping, error) {
	return nil, m.err
}
