/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

// Plain table-driven tests over how a component's health is judged and how often it is
// judged again. Like cclcomponentset_builder_test.go, they need no envtest; the
// reconcile paths are covered by the Ginkgo specs.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
	argocdv1alpha1 "github.com/NVIDIA/ccluster/internal/api/argocd/v1alpha1"
	"github.com/NVIDIA/ccluster/internal/argocd/apiclient"
	healthregistry "github.com/NVIDIA/ccluster/internal/registry/health_registry"
)

const healthTestChart = "catalogs/test/chart"

// registryWith returns a registry holding only f, for healthTestChart.
func registryWith(f healthregistry.HealthFunc) *healthregistry.Registry {
	r := &healthregistry.Registry{}
	r.Register(healthTestChart, f)
	return r
}

func applicationWithHealth(health string) *argocdv1alpha1.Application {
	application := &argocdv1alpha1.Application{}
	application.Namespace, application.Name = "team-a", "web"
	application.Spec.Project = "team-a"
	application.Status = &argocdv1alpha1.ApplicationStatus{}
	application.Status.Health.Status = health
	return application
}

func TestJudge(t *testing.T) {
	healthy := catalogs.Health{Status: cclusternvidiacomv1alpha1.ComponentHealthHealthy, Reason: "Running"}
	reports := func(health catalogs.Health) healthregistry.HealthFunc {
		return func(context.Context, cclusternvidiacomv1alpha1.ComponentEntry, catalogs.Observation) catalogs.Health {
			return health
		}
	}

	for _, tc := range []struct {
		name        string
		argoCD      ArgoCDServer
		healthFunc  healthregistry.HealthFunc
		chart       string
		aggregate   string
		wantHealth  cclusternvidiacomv1alpha1.ComponentHealth
		wantReason  string
		wantMessage string
	}{
		{
			name:       "the chart's function decides",
			argoCD:     &fakeArgoCD{},
			healthFunc: reports(healthy),
			chart:      healthTestChart,
			aggregate:  argocdv1alpha1.HealthStatusProgressing,
			wantHealth: cclusternvidiacomv1alpha1.ComponentHealthHealthy,
			wantReason: "Running",
		},
		{
			name:       "a chart without a function gets the default",
			argoCD:     &fakeArgoCD{},
			healthFunc: reports(healthy),
			chart:      "catalogs/test/other",
			aggregate:  argocdv1alpha1.HealthStatusDegraded,
			wantHealth: cclusternvidiacomv1alpha1.ComponentHealthDegraded,
		},
		{
			name:       "without argocd-server the aggregate decides",
			healthFunc: reports(healthy),
			chart:      healthTestChart,
			aggregate:  argocdv1alpha1.HealthStatusProgressing,
			wantHealth: cclusternvidiacomv1alpha1.ComponentHealthProgressing,
		},
		{
			name:        "an unreadable tree falls back to the aggregate",
			argoCD:      &fakeArgoCD{treeErr: errors.New("token missing")},
			healthFunc:  reports(healthy),
			chart:       healthTestChart,
			aggregate:   argocdv1alpha1.HealthStatusHealthy,
			wantHealth:  cclusternvidiacomv1alpha1.ComponentHealthHealthy,
			wantReason:  cclusternvidiacomv1alpha1.ComponentHealthReasonResourceTreeUnavailable,
			wantMessage: "token missing",
		},
		{
			name:   "a panic falls back to the aggregate",
			argoCD: &fakeArgoCD{},
			healthFunc: func(context.Context, cclusternvidiacomv1alpha1.ComponentEntry, catalogs.Observation) catalogs.Health {
				panic("index out of range")
			},
			chart:       healthTestChart,
			aggregate:   argocdv1alpha1.HealthStatusProgressing,
			wantHealth:  cclusternvidiacomv1alpha1.ComponentHealthProgressing,
			wantReason:  cclusternvidiacomv1alpha1.ComponentHealthReasonHealthFuncPanicked,
			wantMessage: "index out of range",
		},
		{
			name:        "a health the CRD refuses falls back to the aggregate",
			argoCD:      &fakeArgoCD{},
			healthFunc:  reports(catalogs.Health{Status: "Running"}),
			chart:       healthTestChart,
			aggregate:   argocdv1alpha1.HealthStatusHealthy,
			wantHealth:  cclusternvidiacomv1alpha1.ComponentHealthHealthy,
			wantReason:  cclusternvidiacomv1alpha1.ComponentHealthReasonInvalidHealth,
			wantMessage: `"Running"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := judge(context.Background(), tc.argoCD, registryWith(tc.healthFunc),
				applicationWithHealth(tc.aggregate), cclusternvidiacomv1alpha1.ComponentEntry{Chart: tc.chart})
			if got.Status != tc.wantHealth || got.Reason != tc.wantReason {
				t.Errorf("health = %q/%q, want %q/%q", got.Status, got.Reason, tc.wantHealth, tc.wantReason)
			}
			if !strings.Contains(got.Message, tc.wantMessage) {
				t.Errorf("message = %q, want it to contain %q", got.Message, tc.wantMessage)
			}
		})
	}
}

// TestJudgeObservation: the chart's function gets the Application's tree and a ctx with
// a deadline, and its reader reads through the same Application.
func TestJudgeObservation(t *testing.T) {
	pod := &unstructured.Unstructured{}
	pod.SetName("web-0")
	argoCD := &fakeArgoCD{
		tree: apiclient.ResourceTree{Nodes: []apiclient.ResourceNode{{
			ResourceRef: apiclient.ResourceRef{Version: "v1", Kind: "Pod", Namespace: "team-a", Name: "web-0"},
		}}},
		objects: map[string]*unstructured.Unstructured{"Pod/web-0": pod},
	}
	var (
		read        *unstructured.Unstructured
		hasDeadline bool
	)
	healthFuncs := registryWith(
		func(ctx context.Context, _ cclusternvidiacomv1alpha1.ComponentEntry, observed catalogs.Observation) catalogs.Health {
			_, hasDeadline = ctx.Deadline()
			if len(observed.Resources) == 1 {
				read, _ = observed.Reader.Get(ctx, observed.Resources[0].ResourceRef)
			}
			return catalogs.Health{Status: cclusternvidiacomv1alpha1.ComponentHealthHealthy}
		})

	judge(t.Context(), argoCD, healthFuncs, applicationWithHealth(argocdv1alpha1.HealthStatusHealthy),
		cclusternvidiacomv1alpha1.ComponentEntry{Chart: healthTestChart})

	if read == nil || read.GetName() != "web-0" {
		t.Errorf("the chart's function read %v, want Pod web-0 of the tree", read)
	}
	if !hasDeadline {
		t.Error("the chart's function got a ctx without a deadline")
	}
	want := []apiclient.ApplicationRef{{Namespace: "team-a", Name: "web", Project: "team-a"}}
	if got := argoCD.treesRead(); !slices.Equal(got, want) {
		t.Errorf("trees read of %+v, want %+v", got, want)
	}
	if got := argoCD.objectsRead(); !slices.Equal(got, want) {
		t.Errorf("objects read through %+v, want %+v", got, want)
	}
}

func TestJudgeTruncatesMessage(t *testing.T) {
	long := strings.Repeat("x", 2*maxHealthMessage)
	healthFuncs := registryWith(
		func(context.Context, cclusternvidiacomv1alpha1.ComponentEntry, catalogs.Observation) catalogs.Health {
			return catalogs.Health{Status: cclusternvidiacomv1alpha1.ComponentHealthDegraded, Message: long}
		})
	got := judge(context.Background(), &fakeArgoCD{}, healthFuncs,
		applicationWithHealth(argocdv1alpha1.HealthStatusHealthy), cclusternvidiacomv1alpha1.ComponentEntry{Chart: healthTestChart})
	if len(got.Message) != maxHealthMessage || !strings.HasSuffix(got.Message, "...") {
		t.Errorf("message of %d bytes, want %d ending in ...", len(got.Message), maxHealthMessage)
	}
}

func TestComponentHealthsOf(t *testing.T) {
	application := applicationWithHealth(argocdv1alpha1.HealthStatusProgressing)
	judged := componentHealths{"web": {Status: cclusternvidiacomv1alpha1.ComponentHealthHealthy}}

	if got := judged.of("web", application).Status; got != cclusternvidiacomv1alpha1.ComponentHealthHealthy {
		t.Errorf("judged component: %q, want Healthy", got)
	}
	if got := judged.of("api", application).Status; got != cclusternvidiacomv1alpha1.ComponentHealthProgressing {
		t.Errorf("unjudged component: %q, want the aggregate Progressing", got)
	}
	if got := componentHealths(nil).of("web", application).Status; got != cclusternvidiacomv1alpha1.ComponentHealthProgressing {
		t.Errorf("nothing judged: %q, want the aggregate Progressing", got)
	}
}

func TestPollInterval(t *testing.T) {
	statuses := func(healths ...cclusternvidiacomv1alpha1.ComponentHealth) []cclusternvidiacomv1alpha1.ComponentHealthStatus {
		out := make([]cclusternvidiacomv1alpha1.ComponentHealthStatus, 0, len(healths))
		for _, h := range healths {
			out = append(out, cclusternvidiacomv1alpha1.ComponentHealthStatus{Health: h})
		}
		return out
	}
	for _, tc := range []struct {
		name     string
		statuses []cclusternvidiacomv1alpha1.ComponentHealthStatus
		want     time.Duration
	}{
		{"nothing released", nil, healthPollSettled},
		{"all settled", statuses(cclusternvidiacomv1alpha1.ComponentHealthHealthy,
			cclusternvidiacomv1alpha1.ComponentHealthSucceeded, cclusternvidiacomv1alpha1.ComponentHealthFailed),
			healthPollSettled},
		{"one progressing", statuses(cclusternvidiacomv1alpha1.ComponentHealthHealthy,
			cclusternvidiacomv1alpha1.ComponentHealthProgressing), healthPollSettling},
		{"one degraded", statuses(cclusternvidiacomv1alpha1.ComponentHealthDegraded), healthPollSettling},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pollInterval(tc.statuses); got != tc.want {
				t.Errorf("pollInterval = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSameHealthStatuses(t *testing.T) {
	at := metav1.NewTime(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC))
	status := cclusternvidiacomv1alpha1.ComponentHealthStatus{
		Name: "web", Health: cclusternvidiacomv1alpha1.ComponentHealthHealthy, LastTransitionTime: at,
	}
	changed := status
	changed.Message = "now with a message"

	if !sameHealthStatuses(nil, []cclusternvidiacomv1alpha1.ComponentHealthStatus{}) {
		t.Error("an empty list differs from a missing one")
	}
	if !sameHealthStatuses([]cclusternvidiacomv1alpha1.ComponentHealthStatus{status},
		[]cclusternvidiacomv1alpha1.ComponentHealthStatus{status}) {
		t.Error("equal lists differ")
	}
	if sameHealthStatuses([]cclusternvidiacomv1alpha1.ComponentHealthStatus{changed},
		[]cclusternvidiacomv1alpha1.ComponentHealthStatus{status}) {
		t.Error("a changed message is the same")
	}
}
