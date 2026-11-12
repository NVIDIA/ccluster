/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/NVIDIA/ccluster/catalogs"
	"github.com/NVIDIA/ccluster/internal/argocd/apiclient"
)

// fakeArgoCD is an ArgoCDServer over a fixed tree, objects and events. Objects and
// events are keyed by "Kind/name". treeErr fails ResourceTree; readErr fails the other
// reads. It records each Application it read from, one entry per request, and the
// deadline of each tree read.
type fakeArgoCD struct {
	tree    apiclient.ResourceTree
	objects map[string]*unstructured.Unstructured
	events  map[string][]corev1.Event
	treeErr error
	readErr error

	mu        sync.Mutex
	trees     []apiclient.ApplicationRef
	deadlines []time.Time
	reads     []apiclient.ApplicationRef
}

func (f *fakeArgoCD) ResourceTree(ctx context.Context, app apiclient.ApplicationRef) (*apiclient.ResourceTree, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trees = append(f.trees, app)
	deadline, _ := ctx.Deadline()
	f.deadlines = append(f.deadlines, deadline)
	if f.treeErr != nil {
		return nil, f.treeErr
	}
	return &f.tree, nil
}

func (f *fakeArgoCD) Resource(
	_ context.Context, app apiclient.ApplicationRef, ref apiclient.ResourceRef,
) (*unstructured.Unstructured, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, app)
	if f.readErr != nil {
		return nil, f.readErr
	}
	if obj, ok := f.objects[ref.Kind+"/"+ref.Name]; ok {
		return obj, nil
	}
	return nil, apiclient.ErrResourceGone
}

func (f *fakeArgoCD) ResourceEvents(
	_ context.Context, app apiclient.ApplicationRef, ref apiclient.ResourceRef,
) ([]corev1.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, app)
	if f.readErr != nil {
		return nil, f.readErr
	}
	return f.events[ref.Kind+"/"+ref.Name], nil
}

// treesRead returns the Applications whose tree was read.
func (f *fakeArgoCD) treesRead() []apiclient.ApplicationRef {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]apiclient.ApplicationRef(nil), f.trees...)
}

// treeDeadlines returns the deadline of each tree read.
func (f *fakeArgoCD) treeDeadlines() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.deadlines...)
}

// objectsRead returns the Application of each object or events read.
func (f *fakeArgoCD) objectsRead() []apiclient.ApplicationRef {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]apiclient.ApplicationRef(nil), f.reads...)
}

var readerApp = apiclient.ApplicationRef{Namespace: "team-a", Name: "web", Project: "team-a"}

// TestObservedResources: every tree node converts with its owners, health and info.
func TestObservedResources(t *testing.T) {
	tree := &apiclient.ResourceTree{Nodes: []apiclient.ResourceNode{
		{
			ResourceRef: apiclient.ResourceRef{
				Group: "apps", Version: "v1", Kind: "Deployment", Namespace: "team-a", Name: "web", UID: "d1",
			},
			Health: &apiclient.HealthStatus{Status: "Progressing", Message: "1 of 2 updated"},
		},
		{
			ResourceRef: apiclient.ResourceRef{Version: "v1", Kind: "Pod", Namespace: "team-a", Name: "web-0", UID: "p1"},
			ParentRefs: []apiclient.ResourceRef{
				{Group: "apps", Kind: "ReplicaSet", Namespace: "team-a", Name: "web-1", UID: "r1"},
			},
			Info: []apiclient.InfoItem{{Name: "Status Reason", Value: "CrashLoopBackOff"}, {Name: "Restart Count", Value: "6"}},
		},
	}}

	observed := observedResources(tree)
	if len(observed) != 2 {
		t.Fatalf("observed %d resources, want the tree's 2", len(observed))
	}
	deployment, pod := observed[0], observed[1]
	if deployment.Kind != "Deployment" || deployment.Group != "apps" || deployment.UID != "d1" ||
		deployment.Health != "Progressing" || deployment.HealthMessage != "1 of 2 updated" || !deployment.Rendered() {
		t.Errorf("deployment = %+v", deployment)
	}
	if pod.Health != "" || len(pod.Parents) != 1 || pod.Parents[0].Kind != "ReplicaSet" || pod.Parents[0].UID != "r1" {
		t.Errorf("pod = %+v", pod)
	}
	if pod.Info[catalogs.InfoStatusReason] != "CrashLoopBackOff" || pod.Info[catalogs.InfoRestartCount] != "6" {
		t.Errorf("pod info = %v", pod.Info)
	}
}

// TestApplicationReader: the reader reads through its own Application. An object gone
// since the tree was cached reads as nothing, not as an error; any other failure is
// the reader's error.
func TestApplicationReader(t *testing.T) {
	pod := catalogs.ResourceRef{Version: "v1", Kind: "Pod", Namespace: "team-a", Name: "web-0"}
	live := &unstructured.Unstructured{}
	live.SetName("web-0")

	for _, tc := range []struct {
		name       string
		argoCD     *fakeArgoCD
		wantObject bool
		wantEvents int
		wantErr    error
	}{
		{
			name: "found",
			argoCD: &fakeArgoCD{
				objects: map[string]*unstructured.Unstructured{"Pod/web-0": live},
				events:  map[string][]corev1.Event{"Pod/web-0": {{Reason: "FailedMount"}}},
			},
			wantObject: true,
			wantEvents: 1,
		},
		{
			name:   "gone",
			argoCD: &fakeArgoCD{readErr: fmt.Errorf("reading Pod web-0: %w", apiclient.ErrResourceGone)},
		},
		{
			name:    "failed",
			argoCD:  &fakeArgoCD{readErr: apiclient.ErrUnauthorized},
			wantErr: apiclient.ErrUnauthorized,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := applicationReader{argoCD: tc.argoCD, app: readerApp}

			obj, err := reader.Get(t.Context(), pod)
			if !errors.Is(err, tc.wantErr) || (obj != nil) != tc.wantObject {
				t.Errorf("Get = %v, %v; want an object %v, error %v", obj, err, tc.wantObject, tc.wantErr)
			}
			events, err := reader.Events(t.Context(), pod)
			if !errors.Is(err, tc.wantErr) || len(events) != tc.wantEvents {
				t.Errorf("Events = %v, %v; want %d events, error %v", events, err, tc.wantEvents, tc.wantErr)
			}
			for _, app := range tc.argoCD.objectsRead() {
				if app != readerApp {
					t.Errorf("read through %+v, want %+v", app, readerApp)
				}
			}
		})
	}
}
