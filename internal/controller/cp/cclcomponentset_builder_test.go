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
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"

	"k8s.io/apimachinery/pkg/api/equality"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
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
	syncFailureMessage = "one or more objects failed to apply"

	// retryMessage is the message format Argo CD uses while it waits to retry a failed sync.
	retryMessage = syncFailureMessage + ". Retrying attempt #2 at 10:00AM."

	// uncomparedMarker is how a case says "Argo CD has not compared this spec yet". It
	// is written into a field applications() overwrites, so it never reaches a decision.
	uncomparedMarker = "uncompared"
)

// specBehind leaves component's Application on a spec other than the one the operator
// would write now, as after a spec edit the operator has not applied yet.
func specBehind(
	existing map[string]*argocdv1alpha1.Application,
	component string,
) map[string]*argocdv1alpha1.Application {
	existing[applicationName(testComponentSetName, component)].Spec.Source.Path = "catalogs/previous"
	return existing
}

// uncompared leaves Argo CD's last comparison describing the previous spec. Its health
// and sync verdicts are then about that one, whatever they say.
func uncompared(application *argocdv1alpha1.Application) *argocdv1alpha1.Application {
	if application.Status == nil {
		application.Status = &argocdv1alpha1.ApplicationStatus{}
	}
	application.Status.Sync.ComparedTo.Source.Path = uncomparedMarker
	return application
}

// failedSync adds the terminal operation state Argo CD leaves behind when a sync has
// failed and the retry budget is spent.
func failedSync(application *argocdv1alpha1.Application) *argocdv1alpha1.Application {
	if application.Status == nil {
		application.Status = &argocdv1alpha1.ApplicationStatus{}
	}
	application.Status.OperationState = &argocdv1alpha1.OperationState{
		Phase:      argocdv1alpha1.OperationFailed,
		Message:    syncFailureMessage,
		FinishedAt: &metav1.Time{Time: time.Unix(100, 0)},
	}
	return application
}

// pending stages a request Argo CD has not picked up yet. The operation state beside it,
// if any, still describes the previous operation.
func pending(application *argocdv1alpha1.Application) *argocdv1alpha1.Application {
	application.Operation = &argocdv1alpha1.Operation{Sync: &argocdv1alpha1.SyncOperation{}}
	return application
}

// retrying puts a sync in the state Argo CD holds it in between failed attempts: phase
// Running, the attempt in the message, and spec.operation still set, because Argo CD
// clears that only when the operation reaches a terminal phase.
func retrying(application *argocdv1alpha1.Application) *argocdv1alpha1.Application {
	if application.Status == nil {
		application.Status = &argocdv1alpha1.ApplicationStatus{}
	}
	application.Status.OperationState = &argocdv1alpha1.OperationState{
		Phase:   "Running",
		Message: retryMessage,
	}
	return pending(application)
}

// applications keys apps by the Application name the reconciler would give them, and
// records that Argo CD compared the spec they carry. uncompared() opts out of that.
func applications(byComponent map[string]*argocdv1alpha1.Application) map[string]*argocdv1alpha1.Application {
	out := make(map[string]*argocdv1alpha1.Application, len(byComponent))
	for component, application := range byComponent {
		set := componentSetWith(component)
		application.Spec = (&CCLComponentSetReconciler{}).applicationSpec(set, set.Spec.Components[0])
		if application.Status != nil {
			if state := application.Status.OperationState; state != nil {
				state.SyncResult = &argocdv1alpha1.SyncOperationResult{Source: application.Spec.Source}
			}
			if application.Status.Sync.ComparedTo.Source.Path == uncomparedMarker {
				application.Status.Sync.ComparedTo = argocdv1alpha1.ComparedTo{}
			} else {
				application.Status.Sync.ComparedTo = argocdv1alpha1.ComparedTo{
					Source:      application.Spec.Source,
					Destination: application.Spec.Destination,
				}
			}
		}
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
			name:       "holds while Argo CD has not compared the spec the operator wrote",
			components: []string{"a", "b"},
			existing: applications(map[string]*argocdv1alpha1.Application{
				"a": uncompared(healthyApp()),
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
			got := testReleaseCount(componentSetWith(test.components...), test.existing)
			if got != test.want {
				t.Errorf("testReleaseCount() = %d, want %d", got, test.want)
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

	if got := testReleaseCount(componentSetWith("a", "b", "c"), existing); got != 3 {
		t.Errorf("testReleaseCount() over all Applications = %d, want 3", got)
	}
	if got := testReleaseCount(componentSetWith("a", "b", "c"), liveApplications(existing)); got != 2 {
		t.Errorf("testReleaseCount() over live Applications = %d, want 2", got)
	}
}

// TestReleaseCountEmptySpecDoesNotPanic pins the reason for the clamp: the value
// releaseCount returns is used to slice the component list.
func TestReleaseCountEmptySpecDoesNotPanic(t *testing.T) {
	componentSet := componentSetWith()
	released := testReleaseCount(componentSet, nil)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("slicing Components[:%d] panicked: %v", released, r)
		}
	}()
	_ = componentSet.Spec.Components[:released]
}

func testReleaseCount(set *cclusternvidiacomv1alpha1.CCLComponentSet, apps map[string]*argocdv1alpha1.Application) int {
	return (&CCLComponentSetReconciler{}).releaseCount(set, apps)
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
			wantReason:  cclusternvidiacomv1alpha1.CCLComponentSetReasonApplicationsCreated,
			wantMessage: "Applied 1 of 2 components",
		},
		{
			// Argo CD is not installed. Retrying does not fix it; the controller
			// already watches for the CRD appearing.
			name:       "reports ApplicationCRDMissing for a NoMatch error without asking for a retry",
			err:        noMatch,
			wantStatus: metav1.ConditionFalse,
			wantReason: cclusternvidiacomv1alpha1.CCLComponentSetReasonApplicationCRDMissing,
		},
		{
			name:       "reports ApplicationCRDMissing for a NotFound error",
			err:        notFound,
			wantStatus: metav1.ConditionFalse,
			wantReason: cclusternvidiacomv1alpha1.CCLComponentSetReasonApplicationCRDMissing,
		},
		{
			// Returning the error here would hot-loop on a condition no retry can fix.
			name:       "reports ApplicationConflict without asking for a retry",
			err:        errApplicationConflict,
			wantStatus: metav1.ConditionFalse,
			wantReason: cclusternvidiacomv1alpha1.CCLComponentSetReasonApplicationConflict,
		},
		{
			// errors.Is, not ==: a refactor to == would pass every other case here.
			name:       "reports ApplicationConflict for a wrapped conflict",
			err:        fmt.Errorf("updating Application: %w", errApplicationConflict),
			wantStatus: metav1.ConditionFalse,
			wantReason: cclusternvidiacomv1alpha1.CCLComponentSetReasonApplicationConflict,
		},
		{
			name:       "returns an unclassified error so the request is retried",
			err:        boom,
			wantStatus: metav1.ConditionFalse,
			wantReason: cclusternvidiacomv1alpha1.CCLComponentSetReasonApplicationFailed,
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

	// invalidMessage stands for the Validated message of a pass that failed validation.
	const invalidMessage = `Component "a" references CCLInfraPlane "missing", which does not exist`

	tests := []struct {
		name       string
		components []string
		existing   map[string]*argocdv1alpha1.Application
		// invalid is the reason validation failed with on this pass, empty if it passed.
		invalid     string
		wantStatus  metav1.ConditionStatus
		wantReason  string
		wantMessage string
		notMessage  string
	}{
		{
			// The operator creates nothing on a pass that failed validation.
			name:       "names a failed validation for a component the operator has not deployed",
			components: []string{"a", "b"},
			existing: applications(map[string]*argocdv1alpha1.Application{
				"a": healthyApp(),
			}),
			invalid:     cclusternvidiacomv1alpha1.CCLComponentSetReasonInfraPlaneNotFound,
			wantStatus:  metav1.ConditionTrue,
			wantReason:  cclusternvidiacomv1alpha1.CCLComponentSetReasonInfraPlaneNotFound,
			wantMessage: `Wave 2 (component "b") cannot be deployed until validation passes: ` + invalidMessage,
		},
		{
			// Nor does it write a changed spec, so "waiting for the operator to write"
			// would describe a write that is not coming.
			name:       "names a failed validation for a spec the operator has not written",
			components: []string{"a"},
			existing: specBehind(applications(map[string]*argocdv1alpha1.Application{
				"a": healthyApp(),
			}), "a"),
			invalid:     cclusternvidiacomv1alpha1.CCLComponentSetReasonInfraPlaneNotFound,
			wantStatus:  metav1.ConditionTrue,
			wantReason:  cclusternvidiacomv1alpha1.CCLComponentSetReasonInfraPlaneNotFound,
			wantMessage: `Wave 1 (component "a") cannot be written until validation passes: ` + invalidMessage,
		},
		{
			// Argo CD keeps working on what the operator already wrote.
			name:       "keeps reporting a wait on Argo CD when validation failed",
			components: []string{"a"},
			existing: applications(map[string]*argocdv1alpha1.Application{
				"a": uncompared(healthyApp()),
			}),
			invalid:     cclusternvidiacomv1alpha1.CCLComponentSetReasonInfraPlaneNotFound,
			wantStatus:  metav1.ConditionTrue,
			wantReason:  cclusternvidiacomv1alpha1.CCLComponentSetReasonWaveSyncing,
			wantMessage: `Wave 1 (component "a") is waiting for Argo CD to compare`,
		},
		{
			name:       "names the first component that has no Application yet",
			components: []string{"a", "b"},
			existing:   nil,
			// Progressing is True while the rollout is still going.
			wantStatus:  metav1.ConditionTrue,
			wantReason:  cclusternvidiacomv1alpha1.CCLComponentSetReasonWaveSyncing,
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
			wantReason:  cclusternvidiacomv1alpha1.CCLComponentSetReasonWaveSyncing,
			wantMessage: `Wave 2 (component "b") has health "Progressing", sync "OutOfSync"`,
		},
		{
			// Healthy and Synced here describe the spec before the one the operator
			// wrote, so the message must not repeat them as if they were current.
			name:       "waits when Argo CD has not compared the spec the operator wrote",
			components: []string{"a"},
			existing: applications(map[string]*argocdv1alpha1.Application{
				"a": uncompared(healthyApp()),
			}),
			wantStatus:  metav1.ConditionTrue,
			wantReason:  cclusternvidiacomv1alpha1.CCLComponentSetReasonWaveSyncing,
			wantMessage: `Wave 1 (component "a") is waiting for Argo CD to compare`,
			notMessage:  "health",
		},
		{
			// Argo CD syncs only after it compares, so an operation that finished before
			// the comparison is about an older source.
			name:       "leaves a finished operation's message out while the comparison is outstanding",
			components: []string{"a"},
			existing: applications(map[string]*argocdv1alpha1.Application{
				"a": uncompared(failedSync(healthyApp())),
			}),
			wantStatus:  metav1.ConditionTrue,
			wantReason:  cclusternvidiacomv1alpha1.CCLComponentSetReasonWaveSyncing,
			wantMessage: "is waiting for Argo CD to compare the spec the operator wrote",
			notMessage:  syncFailureMessage,
		},
		{
			// Argo CD puts the retry attempt in the operation message, which is the only
			// thing separating a sync failing on every attempt from a slow one.
			name:       "repeats the retry attempt while an operation is running",
			components: []string{"a"},
			existing: applications(map[string]*argocdv1alpha1.Application{
				"a": retrying(healthyApp()),
			}),
			wantStatus:  metav1.ConditionTrue,
			wantReason:  cclusternvidiacomv1alpha1.CCLComponentSetReasonWaveSyncing,
			wantMessage: "is waiting for an Argo CD operation to complete: " + retryMessage,
		},
		{
			name:       "leaves the previous operation's message out while a request is pending",
			components: []string{"a"},
			existing: applications(map[string]*argocdv1alpha1.Application{
				"a": pending(failedSync(healthyApp())),
			}),
			wantStatus:  metav1.ConditionTrue,
			wantReason:  cclusternvidiacomv1alpha1.CCLComponentSetReasonWaveSyncing,
			wantMessage: "is waiting for an Argo CD operation to complete",
			notMessage:  syncFailureMessage,
		},
		{
			name:       "reports a failed sync apart from a slow one",
			components: []string{"a"},
			existing: applications(map[string]*argocdv1alpha1.Application{
				"a": failedSync(app(argocdv1alpha1.HealthStatusHealthy, "OutOfSync")),
			}),
			wantStatus:  metav1.ConditionTrue,
			wantReason:  cclusternvidiacomv1alpha1.CCLComponentSetReasonWaveSyncFailed,
			wantMessage: `Wave 1 (component "a") failed to sync: ` + syncFailureMessage,
		},
		{
			name:       "names the health verdict once Argo CD has compared this spec",
			components: []string{"a"},
			existing: applications(map[string]*argocdv1alpha1.Application{
				"a": app(argocdv1alpha1.HealthStatusDegraded, "OutOfSync"),
			}),
			wantStatus:  metav1.ConditionTrue,
			wantReason:  cclusternvidiacomv1alpha1.CCLComponentSetReasonWaveSyncing,
			wantMessage: `has health "Degraded", sync "OutOfSync"`,
		},
		{
			name:       "reports an Application with no status as still converging",
			components: []string{"a"},
			existing: applications(map[string]*argocdv1alpha1.Application{
				"a": app("", ""),
			}),
			wantStatus: metav1.ConditionTrue,
			wantReason: cclusternvidiacomv1alpha1.CCLComponentSetReasonWaveSyncing,
		},
		{
			// Progressing flips False only when everything is done. A refactor that
			// "corrects" this polarity inverts the whole condition.
			name:       "reports complete when every component is healthy and synced",
			components: []string{"a", "b"},
			existing: applications(map[string]*argocdv1alpha1.Application{
				"a": healthyApp(),
				"b": healthyApp(),
			}),
			wantStatus:  metav1.ConditionFalse,
			wantReason:  cclusternvidiacomv1alpha1.CCLComponentSetReasonRolloutComplete,
			wantMessage: "All 2 components have synced the current spec and are healthy",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			set := componentSetWith(test.components...)
			var validation string
			if test.invalid != "" {
				validation = invalidMessage
			}
			status, reason, message := reconciler.rolloutStatus(set, test.existing, test.invalid, validation)

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

// TestHelmValuesKeepsNullValuedKeys verifies that YAML values retain explicit nulls.
// A null inside valuesObject would be removed by Argo CD's status merge patch.
func TestHelmValuesKeepsNullValuedKeys(t *testing.T) {
	helm := helmValues(&apiextensionsv1.JSON{Raw: []byte(`{"kept":"yes","cleared":null}`)})
	if helm == nil {
		t.Fatal("helmValues returned nil for a non-empty document")
	}
	if !strings.Contains(helm.Values, "cleared: null") {
		t.Errorf("values = %q, want it to carry the null-valued key", helm.Values)
	}
}

// TestHelmValuesMatchesArgoCDRendering verifies the conversion Argo CD uses for valuesObject.
func TestHelmValuesMatchesArgoCDRendering(t *testing.T) {
	raw := []byte(`{"keda":{"operator":{"replicaCount":1}}}`)
	want, err := yaml.JSONToYAML(raw)
	if err != nil {
		t.Fatalf("JSONToYAML: %v", err)
	}
	if got := helmValues(&apiextensionsv1.JSON{Raw: raw}); got.Values != string(want) {
		t.Errorf("values = %q, want %q", got.Values, string(want))
	}
}

// TestHelmValuesIsAbsentWithoutValues verifies that omitted values produce no helm block.
func TestHelmValuesIsAbsentWithoutValues(t *testing.T) {
	for _, values := range []*apiextensionsv1.JSON{nil, {Raw: []byte{}}} {
		if got := helmValues(values); got != nil {
			t.Errorf("helmValues(%v) = %+v, want nil", values, got)
		}
	}
}

// TestUnmodelledSourceFieldsAreVisible verifies that source drift survives decoding.
// Unmodelled fields must affect both spec reconciliation and comparedTo matching.
func TestUnmodelledSourceFieldsAreVisible(t *testing.T) {
	set := componentSetWith("a")
	desired := (&CCLComponentSetReconciler{}).applicationSpec(set, set.Spec.Components[0])

	tests := []struct {
		name  string
		patch string
	}{
		{name: "a source field", patch: `{"chart":"keda"}`},
		{name: "a helm field", patch: `{"helm":{"valueFiles":["values-prod.yaml"]}}`},
		{name: "a helm parameter", patch: `{"helm":{"parameters":[{"name":"x","value":"y"}]}}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			drifted := decodeSource(t, desired.Source, test.patch)
			stored := desired
			stored.Source = drifted
			if equality.Semantic.DeepEqual(stored, desired) {
				t.Error("the drift did not read as a spec difference, so nothing would rewrite it away")
			}

			app := &argocdv1alpha1.Application{
				Spec: desired,
				Status: &argocdv1alpha1.ApplicationStatus{
					Sync: argocdv1alpha1.ApplicationSyncStatus{
						ComparedTo: argocdv1alpha1.ComparedTo{
							Source: drifted, Destination: desired.Destination,
						},
					},
				},
			}
			if comparedToMatchesSpec(app, desired) {
				t.Error("a comparison of a different source counted as a comparison of this one")
			}
		})
	}
}

// decodeSource merges a JSON overlay into source and decodes the result.
// The JSON round trip exercises retention of unmodelled fields.
func decodeSource(
	t *testing.T, source argocdv1alpha1.ApplicationSource, patch string,
) argocdv1alpha1.ApplicationSource {
	t.Helper()

	var base, overlay map[string]any
	encoded, err := json.Marshal(source)
	if err != nil {
		t.Fatalf("marshal source: %v", err)
	}
	if err := json.Unmarshal(encoded, &base); err != nil {
		t.Fatalf("unmarshal source: %v", err)
	}
	if err := json.Unmarshal([]byte(patch), &overlay); err != nil {
		t.Fatalf("unmarshal patch: %v", err)
	}
	mergeInto(base, overlay)

	merged, err := json.Marshal(base)
	if err != nil {
		t.Fatalf("marshal merged source: %v", err)
	}
	var decoded argocdv1alpha1.ApplicationSource
	if err := json.Unmarshal(merged, &decoded); err != nil {
		t.Fatalf("unmarshal merged source: %v", err)
	}
	return decoded
}

func mergeInto(base, overlay map[string]any) {
	for key, value := range overlay {
		if nested, ok := value.(map[string]any); ok {
			if existing, ok := base[key].(map[string]any); ok {
				mergeInto(existing, nested)
				continue
			}
		}
		base[key] = value
	}
}

// TestZeroValuedUnmodelledFieldsAreIgnored verifies normalization of optional source fields.
// Zero values are ignored; empty objects that select a source type are retained.
func TestZeroValuedUnmodelledFieldsAreIgnored(t *testing.T) {
	set := componentSetWith("a")
	// With values, so that the helm cases patch a block that is already there rather
	// than adding one.
	set.Spec.Components[0].Values = &apiextensionsv1.JSON{Raw: []byte(`{"replicaCount":3}`)}
	desired := (&CCLComponentSetReconciler{}).applicationSpec(set, set.Spec.Components[0])

	tests := []struct {
		name    string
		patch   string
		ignored bool
	}{
		{name: "an empty chart", patch: `{"chart":""}`, ignored: true},
		{name: "a null ref", patch: `{"ref":null}`, ignored: true},
		{name: "an empty valueFiles", patch: `{"helm":{"valueFiles":[]}}`, ignored: true},
		{name: "a false skipCrds", patch: `{"helm":{"skipCrds":false}}`, ignored: true},
		{name: "a named chart", patch: `{"chart":"keda"}`, ignored: false},
		{name: "a true skipCrds", patch: `{"helm":{"skipCrds":true}}`, ignored: false},
		{name: "a non-empty valueFiles", patch: `{"helm":{"valueFiles":["x.yaml"]}}`, ignored: false},
		// Which block is present is how Argo CD decides the source type, so an empty
		// one is still a difference.
		{name: "an empty kustomize block", patch: `{"kustomize":{}}`, ignored: false},
		// Removing an existing values document is a source change.
		{name: "a removed helm block", patch: `{"helm":null}`, ignored: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stored := desired
			stored.Source = decodeSource(t, desired.Source, test.patch)
			if got := equality.Semantic.DeepEqual(stored, desired); got != test.ignored {
				t.Errorf("spec compared equal = %v, want %v", got, test.ignored)
			}
		})
	}
}

// TestModelledSourceFieldsStayOutOfUnmodelled verifies that modelled fields are excluded
// from Unmodelled and that the desired source survives a JSON round trip unchanged.
func TestModelledSourceFieldsStayOutOfUnmodelled(t *testing.T) {
	set := componentSetWith("a")
	set.Spec.Components[0].Values = &apiextensionsv1.JSON{Raw: []byte(`{"replicaCount":3}`)}
	desired := (&CCLComponentSetReconciler{}).applicationSpec(set, set.Spec.Components[0])

	roundTripped := decodeSource(t, desired.Source, `{}`)
	if roundTripped.Unmodelled != nil {
		t.Errorf("Unmodelled = %v, want nil - a modelled key is missing from sourceKeys", roundTripped.Unmodelled)
	}
	if roundTripped.Helm.Unmodelled != nil {
		t.Errorf("helm Unmodelled = %v, want nil - a modelled key is missing from helmKeys", roundTripped.Helm.Unmodelled)
	}
	if !equality.Semantic.DeepEqual(roundTripped, desired.Source) {
		t.Error("a source the operator wrote does not read back equal to the one it built")
	}
}

// TestHelmValuesIgnoresValueFormatting verifies that equivalent JSON produces equal specs.
// Formatting-only differences must not submit another sync or prevent completion.
func TestHelmValuesIgnoresValueFormatting(t *testing.T) {
	spec := func(values string) argocdv1alpha1.ApplicationSpec {
		set := componentSetWith("a")
		set.Spec.Components[0].Values = &apiextensionsv1.JSON{Raw: []byte(values)}
		return (&CCLComponentSetReconciler{}).applicationSpec(set, set.Spec.Components[0])
	}

	tests := []struct {
		name  string
		a, b  string
		equal bool
	}{
		{
			name: "key order", equal: true,
			a: `{"image":"nvcr.io/x","replicaCount":3}`, b: `{"replicaCount":3,"image":"nvcr.io/x"}`,
		},
		{name: "number formatting", equal: true, a: `{"replicaCount":3}`, b: `{"replicaCount":3.0}`},
		{name: "whitespace", equal: true, a: `{"replicaCount":3}`, b: `{ "replicaCount" : 3 }`},
		{name: "a real change", equal: false, a: `{"replicaCount":3}`, b: `{"replicaCount":4}`},
		{name: "a removed key", equal: false, a: `{"replicaCount":3,"image":"x"}`, b: `{"replicaCount":3}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := equality.Semantic.DeepEqual(spec(test.a), spec(test.b)); got != test.equal {
				t.Errorf("spec compared equal for %s and %s = %v, want %v", test.a, test.b, got, test.equal)
			}
		})
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
