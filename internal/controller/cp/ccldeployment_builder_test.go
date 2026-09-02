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

const testDeploymentName = "test-deployment"

// deploymentWith builds a CCLDeployment whose components are named, in order, by names.
func deploymentWith(names ...string) *cclusternvidiacomv1alpha1.CCLDeployment {
	deployment := &cclusternvidiacomv1alpha1.CCLDeployment{
		ObjectMeta: metav1.ObjectMeta{Name: testDeploymentName, Namespace: "default"},
	}
	for _, name := range names {
		deployment.Spec.Components = append(deployment.Spec.Components,
			cclusternvidiacomv1alpha1.ComponentSpec{
				Name:          name,
				InfraPlaneRef: cclusternvidiacomv1alpha1.LocalObjectReference{Name: "plane-a"},
				ComponentEntry: cclusternvidiacomv1alpha1.ComponentEntry{
					Chart:     "catalogs/cluster_base/" + name,
					Namespace: name,
				},
			})
	}
	return deployment
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

// applications keys apps by the Application name the reconciler would give them.
func applications(byComponent map[string]*argocdv1alpha1.Application) map[string]*argocdv1alpha1.Application {
	out := make(map[string]*argocdv1alpha1.Application, len(byComponent))
	for component, application := range byComponent {
		out[applicationName(testDeploymentName, component)] = application
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
			got := releaseCount(deploymentWith(test.components...), test.existing)
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

	if got := releaseCount(deploymentWith("a", "b", "c"), existing); got != 3 {
		t.Errorf("releaseCount() over all Applications = %d, want 3", got)
	}
	if got := releaseCount(deploymentWith("a", "b", "c"), liveApplications(existing)); got != 2 {
		t.Errorf("releaseCount() over live Applications = %d, want 2", got)
	}
}

// TestReleaseCountEmptySpecDoesNotPanic pins the reason for the clamp: the value
// releaseCount returns is used to slice the component list.
func TestReleaseCountEmptySpecDoesNotPanic(t *testing.T) {
	deployment := deploymentWith()
	released := releaseCount(deployment, nil)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("slicing Components[:%d] panicked: %v", released, r)
		}
	}()
	_ = deployment.Spec.Components[:released]
}

func TestApplicationsCondition(t *testing.T) {
	reconciler := &CCLDeploymentReconciler{ArgoCDNamespace: "argocd"}
	deployment := deploymentWith("a", "b")

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
				deployment, test.released, test.err)

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
	reconciler := &CCLDeploymentReconciler{ArgoCDNamespace: "argocd"}

	withMessage := app(argocdv1alpha1.HealthStatusDegraded, "OutOfSync")
	withMessage.Status.Health.Message = "back-off pulling image"

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
			name:       "appends the health message when Argo CD supplies one",
			components: []string{"a"},
			existing: applications(map[string]*argocdv1alpha1.Application{
				"a": withMessage,
			}),
			wantStatus:  metav1.ConditionTrue,
			wantReason:  reasonWaveSyncing,
			wantMessage: `sync "OutOfSync": back-off pulling image`,
		},
		{
			// Guards the `detail += ": "` branch.
			name:       "omits the separator when there is no health message",
			components: []string{"a"},
			existing: applications(map[string]*argocdv1alpha1.Application{
				"a": app(argocdv1alpha1.HealthStatusProgressing, "OutOfSync"),
			}),
			wantStatus: metav1.ConditionTrue,
			wantReason: reasonWaveSyncing,
			notMessage: `"OutOfSync": `,
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
				"a": healthyApp(),
				"b": healthyApp(),
			}),
			wantStatus:  metav1.ConditionFalse,
			wantReason:  reasonRolloutComplete,
			wantMessage: "All 2 components are healthy and synced",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, reason, message := reconciler.rolloutStatus(
				deploymentWith(test.components...), test.existing)

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

// TestApplicationNameCollidesAcrossDeployments is a regression guard, not a test of
// string concatenation. applicationName does no escaping, and neither the DNS-1123
// pattern on component names nor the 63-character CEL rule on the CCLDeployment name
// prevents two different deployments mapping onto one Application. The only thing that
// stops the second one clobbering the first is the ownedBy check in reconcileApplication,
// which reports errApplicationConflict. If this test is ever "simplified away", so is the
// reason that check exists.
func TestApplicationNameCollidesAcrossDeployments(t *testing.T) {
	if applicationName("a-b", "c") != applicationName("a", "b-c") {
		t.Fatal("expected the documented collision; if this now passes, the ownership " +
			"conflict check may no longer be the only guard")
	}
}

// TestApplicationSpecOmitsHelmWhenValuesAbsent covers the len(Raw) > 0 guard. The
// values-present path is asserted by the envtest suite; this is the branch that is easy
// to drop, and dropping it emits an empty helm block on every valueless component.
func TestApplicationSpecOmitsHelmWhenValuesAbsent(t *testing.T) {
	reconciler := &CCLDeploymentReconciler{
		ArgoCDNamespace: "argocd",
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
			spec := reconciler.applicationSpec(cclusternvidiacomv1alpha1.ComponentSpec{
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

// TestApplicationCRDInstalled covers the startup decision that guards the Application
// watch. It matters more than its size suggests: registering that watch when the CRD is
// absent stops the whole controller from ever starting, because controller-runtime waits
// for every watch source to sync and a source for an unserved kind never does.
func TestApplicationCRDInstalled(t *testing.T) {
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
			if got := applicationCRDInstalled(test.mapper, logr.Discard()); got != test.want {
				t.Errorf("applicationCRDInstalled() = %v, want %v", got, test.want)
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
