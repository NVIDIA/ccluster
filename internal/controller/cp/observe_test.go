/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/NVIDIA/ccluster/internal/argocd/apiclient"
)

var observedApp = apiclient.ApplicationRef{Namespace: "team-a", Name: "web", Project: "team-a"}

// treeServer serves a two-node tree: a Deployment and the Pod it owns. Requests for the
// Pod's manifest answer podStatus with podBody; everything else succeeds.
func treeServer(t *testing.T, podStatus int, podBody string) *httptest.Server {
	t.Helper()
	tree := `{"nodes": [
		{"group": "apps", "version": "v1", "kind": "Deployment", "namespace": "team-a", "name": "web",
		 "uid": "d1", "health": {"status": "Progressing", "message": "1 of 2 updated"}},
		{"version": "v1", "kind": "Pod", "namespace": "team-a", "name": "web-0", "uid": "p1",
		 "parentRefs": [{"group": "apps", "kind": "ReplicaSet", "namespace": "team-a", "name": "web-1", "uid": "r1"}]}
	]}`
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		write := func(status int, body string) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}
		query := r.URL.Query()
		switch r.URL.Path {
		case "/api/v1/applications/web/resource-tree":
			write(http.StatusOK, tree)
		case "/api/v1/applications/web/resource":
			if query.Get("kind") == "Pod" && podStatus != http.StatusOK {
				write(podStatus, podBody)
				return
			}
			manifest, _ := json.Marshal(map[string]any{
				"apiVersion": query.Get("version"), "kind": query.Get("kind"),
				"metadata": map[string]any{"name": query.Get("resourceName"), "namespace": query.Get("namespace")},
			})
			body, _ := json.Marshal(map[string]string{"manifest": string(manifest)})
			write(http.StatusOK, string(body))
		case "/api/v1/applications/web/events":
			write(http.StatusOK, `{"items": [{"metadata": {"name": "e"}, "reason": "Seen `+
				query.Get("resourceName")+`"}]}`)
		default:
			write(http.StatusNotFound, `{"message": "unexpected path"}`)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func observerFor(t *testing.T, server *httptest.Server) ResourceObserver {
	t.Helper()
	token := types.NamespacedName{Namespace: "argocd", Name: "token"}
	reader := fake.NewClientBuilder().WithObjects(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: token.Namespace, Name: token.Name},
		Data:       map[string][]byte{"token": []byte("t")},
	}).Build()
	c, err := apiclient.New(apiclient.Options{
		ServerURL: server.URL, Token: apiclient.SecretKey{NamespacedName: token, Key: "token"},
		Reader: reader,
	})
	if err != nil {
		t.Fatal(err)
	}
	return NewArgoCDObserver(c)
}

func TestObserve(t *testing.T) {
	observed, err := observerFor(t, treeServer(t, http.StatusOK, "")).Observe(context.Background(), observedApp)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}

	if len(observed) != 2 {
		t.Fatalf("observed %d resources, want the tree's 2", len(observed))
	}
	deployment, pod := observed[0], observed[1]
	if deployment.Kind != "Deployment" || deployment.Group != "apps" || deployment.UID != "d1" ||
		deployment.Health != "Progressing" || deployment.HealthMessage != "1 of 2 updated" {
		t.Errorf("deployment = %+v", deployment)
	}
	if pod.Health != "" || len(pod.Parents) != 1 || pod.Parents[0].Kind != "ReplicaSet" || pod.Parents[0].UID != "r1" {
		t.Errorf("pod = %+v", pod)
	}
	for _, r := range observed {
		if r.Manifest == nil || r.Manifest.GetName() != r.Name {
			t.Errorf("%s %s: manifest %v", r.Kind, r.Name, r.Manifest)
		}
		if len(r.Events) != 1 || r.Events[0].Reason != "Seen "+r.Name {
			t.Errorf("%s %s: events %+v, want its own", r.Kind, r.Name, r.Events)
		}
	}
}

// TestObserveGoneResource: an object gone since the tree was cached is still observed,
// with no manifest, rather than failing the observation.
func TestObserveGoneResource(t *testing.T) {
	server := treeServer(t, http.StatusBadRequest, `{"message": "Pod web-0 not found as part of application web"}`)
	observed, err := observerFor(t, server).Observe(context.Background(), observedApp)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(observed) != 2 {
		t.Fatalf("observed %d resources, want 2", len(observed))
	}
	if pod := observed[1]; pod.Manifest != nil || pod.Events != nil {
		t.Errorf("gone pod = %+v, want no manifest or events", pod)
	}
	if observed[0].Manifest == nil {
		t.Error("the Deployment lost its manifest")
	}
}

// TestObserveFailure: any other failed read fails the whole observation.
func TestObserveFailure(t *testing.T) {
	server := treeServer(t, http.StatusForbidden, `{"message": "permission denied"}`)
	_, err := observerFor(t, server).Observe(context.Background(), observedApp)
	if !errors.Is(err, apiclient.ErrUnauthorized) {
		t.Errorf("Observe = %v, want ErrUnauthorized", err)
	}
}
