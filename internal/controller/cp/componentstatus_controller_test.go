/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

// Plain table-driven tests over what the component status controller does with a
// chart's status function and the states it reports. Like cclcomponentset_builder_test.go,
// they need no envtest; the reconcile path is covered by the Ginkgo specs.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
	"github.com/NVIDIA/ccluster/internal/argocd/apiclient"
	statusregistry "github.com/NVIDIA/ccluster/internal/registry/status_registry"
)

const statusTestChart = "catalogs/test/chart"

// fakeObserver returns resources, or err, and records which Applications it was asked
// to observe.
type fakeObserver struct {
	resources catalogs.ObservedResources
	err       error

	mu       sync.Mutex
	observed []apiclient.ApplicationRef
}

func (f *fakeObserver) Observe(_ context.Context, app apiclient.ApplicationRef) (catalogs.ObservedResources, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observed = append(f.observed, app)
	return f.resources, f.err
}

// registryWith returns a registry holding only f, for statusTestChart.
func registryWith(f statusregistry.StatusFunc) *statusregistry.Registry {
	r := &statusregistry.Registry{}
	r.Register(statusTestChart, f)
	return r
}

func TestRunStatus(t *testing.T) {
	observed := catalogs.ObservedResources{{ResourceRef: catalogs.ResourceRef{Kind: "Pod", Name: "web-0"}}}
	running := catalogs.Status{State: cclusternvidiacomv1alpha1.ComponentStateRunning, Reason: "Ready", Message: "1/1"}

	for _, tc := range []struct {
		name       string
		statuses   *statusregistry.Registry
		chart      string
		wantState  cclusternvidiacomv1alpha1.ComponentState
		wantReason string
	}{
		{
			name: "reported as returned",
			statuses: registryWith(func(entry cclusternvidiacomv1alpha1.ComponentEntry,
				got catalogs.ObservedResources) catalogs.Status {
				if entry.Chart != statusTestChart || len(got) != 1 || got[0].Name != "web-0" {
					return catalogs.Status{State: cclusternvidiacomv1alpha1.ComponentStateFailed,
						Reason: "WrongInput"}
				}
				return running
			}),
			chart:      statusTestChart,
			wantState:  cclusternvidiacomv1alpha1.ComponentStateRunning,
			wantReason: "Ready",
		},
		{
			name:       "catalog default",
			statuses:   statusregistry.Get(),
			chart:      "catalogs/cluster_base/keda",
			wantState:  cclusternvidiacomv1alpha1.ComponentStateUnknown,
			wantReason: statusregistry.ReasonStatusNotImplemented,
		},
		{
			name:       "chart not in the registry",
			statuses:   statusregistry.Get(),
			chart:      "catalogs/hand_written",
			wantState:  cclusternvidiacomv1alpha1.ComponentStateUnknown,
			wantReason: statusregistry.ReasonStatusNotImplemented,
		},
		{
			name:       "no registry",
			chart:      statusTestChart,
			wantState:  cclusternvidiacomv1alpha1.ComponentStateUnknown,
			wantReason: statusregistry.ReasonStatusNotImplemented,
		},
		{
			name: "panic",
			statuses: registryWith(func(cclusternvidiacomv1alpha1.ComponentEntry,
				catalogs.ObservedResources) catalogs.Status {
				panic("index out of range")
			}),
			chart:      statusTestChart,
			wantState:  cclusternvidiacomv1alpha1.ComponentStateUnknown,
			wantReason: reasonStatusPanicked,
		},
		{
			name: "state the CRD would refuse",
			statuses: registryWith(func(cclusternvidiacomv1alpha1.ComponentEntry,
				catalogs.ObservedResources) catalogs.Status {
				return catalogs.Status{State: "Healthy"}
			}),
			chart:      statusTestChart,
			wantState:  cclusternvidiacomv1alpha1.ComponentStateUnknown,
			wantReason: reasonInvalidStatus,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &ComponentStatusReconciler{Statuses: tc.statuses}
			got := r.runStatus(cclusternvidiacomv1alpha1.ComponentEntry{Chart: tc.chart}, observed)
			if got.State != tc.wantState || got.Reason != tc.wantReason {
				t.Errorf("runStatus = %+v, want %s/%s", got, tc.wantState, tc.wantReason)
			}
			if got.Message == "" {
				t.Error("runStatus reported no message")
			}
		})
	}
}

func TestRunStatusTruncatesMessage(t *testing.T) {
	r := &ComponentStatusReconciler{Statuses: registryWith(func(cclusternvidiacomv1alpha1.ComponentEntry,
		catalogs.ObservedResources) catalogs.Status {
		return catalogs.Status{State: cclusternvidiacomv1alpha1.ComponentStateFailing,
			Message: strings.Repeat("x", 5000)}
	})}
	got := r.runStatus(cclusternvidiacomv1alpha1.ComponentEntry{Chart: statusTestChart}, nil)
	if len(got.Message) != maxStatusMessage || !strings.HasSuffix(got.Message, "...") {
		t.Errorf("message of %d bytes, want %d ending in ...", len(got.Message), maxStatusMessage)
	}
}

// TestPollInterval: components are observed again soon while any is in a state expected
// to change, and rarely once all have settled.
func TestPollInterval(t *testing.T) {
	status := func(states ...cclusternvidiacomv1alpha1.ComponentState) []cclusternvidiacomv1alpha1.ComponentStatus {
		out := make([]cclusternvidiacomv1alpha1.ComponentStatus, 0, len(states))
		for _, s := range states {
			out = append(out, cclusternvidiacomv1alpha1.ComponentStatus{State: s})
		}
		return out
	}
	for _, tc := range []struct {
		name     string
		statuses []cclusternvidiacomv1alpha1.ComponentStatus
		want     time.Duration
	}{
		{"all settled", status(cclusternvidiacomv1alpha1.ComponentStateRunning,
			cclusternvidiacomv1alpha1.ComponentStateSucceeded,
			cclusternvidiacomv1alpha1.ComponentStateFailed), statusPollSettled},
		{"one starting", status(cclusternvidiacomv1alpha1.ComponentStateRunning,
			cclusternvidiacomv1alpha1.ComponentStateStarting), statusPollSettling},
		{"one unknown", status(cclusternvidiacomv1alpha1.ComponentStateUnknown), statusPollSettling},
		{"one failing", status(cclusternvidiacomv1alpha1.ComponentStateFailing), statusPollSettling},
		{"one pending", status(cclusternvidiacomv1alpha1.ComponentStatePending), statusPollSettling},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pollInterval(tc.statuses); got != tc.want {
				t.Errorf("pollInterval = %v, want %v", got, tc.want)
			}
		})
	}
}
