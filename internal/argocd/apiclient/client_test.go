/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package apiclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	testNamespace = "argocd"
	testToken     = "token-1"
)

var (
	testApp = ApplicationRef{Namespace: "team-a", Name: "web", Project: "team-a"}

	tokenKey = SecretKey{NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: "token"}, Key: "token"}

	pod = ResourceRef{Version: "v1", Kind: "Pod", Namespace: "team-a", Name: "web-0", UID: "uid-1"}
)

// request is what the fake argocd-server received.
type request struct {
	path  string
	query url.Values
	auth  string
}

// fakeServer answers every request with status and body, recording what it received.
func fakeServer(t *testing.T, status int, body string, received *[]request) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*received = append(*received, request{path: r.URL.Path, query: r.URL.Query(), auth: r.Header.Get("Authorization")})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func secret(key SecretKey, value string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name},
		Data:       map[string][]byte{key.Key: []byte(value)},
	}
}

// newClient returns a Client for server, whose certificate is self-signed like the one
// argocd-server generates, reading its token from a fake cluster holding objs.
func newClient(t *testing.T, server *httptest.Server, objs ...client.Object) (*Client, client.Client) {
	t.Helper()
	reader := fake.NewClientBuilder().WithObjects(objs...).Build()
	c, err := New(Options{ServerURL: server.URL, Token: tokenKey, Reader: reader})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, reader
}

func TestResourceTree(t *testing.T) {
	var received []request
	server := fakeServer(t, http.StatusOK, `{"nodes": [
		{"version": "v1", "kind": "Pod", "namespace": "team-a", "name": "web-0", "uid": "uid-1",
		 "parentRefs": [{"group": "apps", "kind": "ReplicaSet", "namespace": "team-a", "name": "web-1"}],
		 "health": {"status": "Progressing", "message": "pulling"}},
		{"version": "v1", "kind": "ConfigMap", "namespace": "team-a", "name": "web"}
	]}`, &received)
	c, _ := newClient(t, server, secret(tokenKey, testToken+"\n"))

	tree, err := c.ResourceTree(context.Background(), testApp)
	if err != nil {
		t.Fatalf("ResourceTree: %v", err)
	}

	if len(received) != 1 {
		t.Fatalf("server received %d requests, want 1", len(received))
	}
	got := received[0]
	if got.path != "/api/v1/applications/web/resource-tree" {
		t.Errorf("path = %q", got.path)
	}
	if got.query.Get("appNamespace") != "team-a" || got.query.Get("project") != "team-a" {
		t.Errorf("query = %v, want the Application's namespace and project", got.query)
	}
	if got.auth != "Bearer "+testToken {
		t.Errorf("Authorization = %q, want the trimmed token", got.auth)
	}

	if len(tree.Nodes) != 2 {
		t.Fatalf("got %d nodes, want 2", len(tree.Nodes))
	}
	node := tree.Nodes[0]
	if node.ResourceRef != pod {
		t.Errorf("node = %+v, want %+v", node.ResourceRef, pod)
	}
	if len(node.ParentRefs) != 1 || node.ParentRefs[0].Kind != "ReplicaSet" {
		t.Errorf("parentRefs = %+v", node.ParentRefs)
	}
	if node.Health == nil || node.Health.Status != "Progressing" || node.Health.Message != "pulling" {
		t.Errorf("health = %+v", node.Health)
	}
	if tree.Nodes[1].Health != nil {
		t.Errorf("a node without health decoded as %+v", tree.Nodes[1].Health)
	}
}

func TestResource(t *testing.T) {
	manifest, err := json.Marshal(map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "web-0", "namespace": "team-a"},
		"status":   map[string]any{"phase": "Pending"},
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]string{"manifest": string(manifest)})
	if err != nil {
		t.Fatal(err)
	}
	var received []request
	server := fakeServer(t, http.StatusOK, string(body), &received)
	c, _ := newClient(t, server, secret(tokenKey, testToken))

	obj, err := c.Resource(context.Background(), testApp, pod)
	if err != nil {
		t.Fatalf("Resource: %v", err)
	}

	got := received[0]
	if got.path != "/api/v1/applications/web/resource" {
		t.Errorf("path = %q", got.path)
	}
	for key, want := range map[string]string{
		"namespace": "team-a", "resourceName": "web-0", "group": "", "version": "v1", "kind": "Pod",
		"appNamespace": "team-a", "project": "team-a",
	} {
		if values, ok := got.query[key]; !ok || values[0] != want {
			t.Errorf("query %s = %v, want %q", key, values, want)
		}
	}
	if obj.GetName() != "web-0" || obj.GetKind() != "Pod" {
		t.Errorf("decoded %s %s", obj.GetKind(), obj.GetName())
	}
	if phase := obj.Object["status"].(map[string]any)["phase"]; phase != "Pending" {
		t.Errorf("status.phase = %v", phase)
	}
}

// TestResourceWithoutManifest: argocd-server answers 200 without a manifest for an object
// that is gone, which is the same case as the 404 it answers otherwise.
func TestResourceWithoutManifest(t *testing.T) {
	var received []request
	server := fakeServer(t, http.StatusOK, `{}`, &received)
	c, _ := newClient(t, server, secret(tokenKey, testToken))

	if _, err := c.Resource(context.Background(), testApp, pod); !errors.Is(err, ErrResourceGone) {
		t.Errorf("Resource = %v, want ErrResourceGone", err)
	}
}

func TestResourceEvents(t *testing.T) {
	var received []request
	server := fakeServer(t, http.StatusOK, `{"items": [
		{"metadata": {"name": "e1"}, "reason": "Pulling", "message": "Pulling image \"x\"",
		 "firstTimestamp": "2026-10-20T10:00:00Z"},
		{"metadata": {"name": "e2"}, "reason": "Pulled"}
	]}`, &received)
	c, _ := newClient(t, server, secret(tokenKey, testToken))

	events, err := c.ResourceEvents(context.Background(), testApp, pod)
	if err != nil {
		t.Fatalf("ResourceEvents: %v", err)
	}

	got := received[0]
	if got.path != "/api/v1/applications/web/events" {
		t.Errorf("path = %q", got.path)
	}
	for key, want := range map[string]string{
		"resourceNamespace": "team-a", "resourceName": "web-0", "resourceUID": "uid-1",
		"appNamespace": "team-a", "project": "team-a",
	} {
		if got.query.Get(key) != want {
			t.Errorf("query %s = %q, want %q", key, got.query.Get(key), want)
		}
	}
	if len(events) != 2 || events[0].Reason != "Pulling" || events[0].FirstTimestamp.IsZero() {
		t.Errorf("events = %+v", events)
	}
}

// TestErrors: each failure argocd-server reports with an ordinary status code is told
// apart by its message, and only where the request makes the case possible.
func TestErrors(t *testing.T) {
	gone := `{"error": "x", "code": 5, "message": "pods \"web-0\" not found"}`
	for _, tc := range []struct {
		name   string
		op     string
		status int
		body   string
		want   error
	}{
		{"bad token", opResourceTree, http.StatusUnauthorized, `{"message": "invalid session"}`, ErrUnauthorized},
		{"no permission", opResource, http.StatusForbidden, `{"message": "permission denied"}`, ErrUnauthorized},
		{"application deleted", opResourceTree, http.StatusNotFound,
			`{"message": "applications.argoproj.io \"web\" not found"}`, ErrApplicationNotFound},
		{"application deleted mid-read", opResource, http.StatusNotFound,
			`{"message": "applications.argoproj.io \"web\" not found"}`, ErrApplicationNotFound},
		{"object gone", opResource, http.StatusNotFound, gone, ErrResourceGone},
		{"events of a gone object", opEvents, http.StatusNotFound, gone, ErrResourceGone},
		{"stale tree", opResource, http.StatusBadRequest,
			`{"message": "Pod web-0 not found as part of application web"}`, ErrResourceGone},
		{"tree not cached", opResourceTree, http.StatusInternalServerError,
			`{"message": "error getting cached app resource tree: cache: key is missing"}`, ErrTreeUnavailable},
		{"other server error", opResourceTree, http.StatusInternalServerError, `{"message": "boom"}`, nil},
		{"non-JSON body", opResourceTree, http.StatusBadGateway, `upstream unavailable`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var received []request
			server := fakeServer(t, tc.status, tc.body, &received)
			c, _ := newClient(t, server, secret(tokenKey, testToken))

			var err error
			switch tc.op {
			case opResourceTree:
				_, err = c.ResourceTree(context.Background(), testApp)
			case opResource:
				_, err = c.Resource(context.Background(), testApp, pod)
			case opEvents:
				_, err = c.ResourceEvents(context.Background(), testApp, pod)
			}

			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error %v is not an *APIError", err)
			}
			if apiErr.StatusCode != tc.status || apiErr.Op != tc.op {
				t.Errorf("APIError = %+v", apiErr)
			}
			for _, kind := range []error{ErrUnauthorized, ErrApplicationNotFound, ErrTreeUnavailable, ErrResourceGone} {
				if errors.Is(err, kind) != (kind == tc.want) {
					t.Errorf("errors.Is(%v, %v) = %v", err, kind, kind != tc.want)
				}
			}
			if tc.body == `upstream unavailable` && !strings.Contains(err.Error(), "upstream unavailable") {
				t.Errorf("error %q lost the non-JSON body", err)
			}
		})
	}
}

// TestToken: a missing token fails before any request is sent, and a rotated one is
// used by the next request.
func TestToken(t *testing.T) {
	var received []request
	server := fakeServer(t, http.StatusOK, `{}`, &received)
	c, cluster := newClient(t, server)

	if _, err := c.ResourceTree(context.Background(), testApp); err == nil {
		t.Fatal("ResourceTree succeeded without a token Secret")
	}
	if len(received) != 0 {
		t.Fatalf("server received %d requests without a token", len(received))
	}

	tokenSecret := secret(tokenKey, testToken)
	if err := cluster.Create(context.Background(), tokenSecret); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ResourceTree(context.Background(), testApp); err != nil {
		t.Fatalf("ResourceTree: %v", err)
	}
	tokenSecret.Data[tokenKey.Key] = []byte("token-2")
	if err := cluster.Update(context.Background(), tokenSecret); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ResourceTree(context.Background(), testApp); err != nil {
		t.Fatalf("ResourceTree: %v", err)
	}

	if len(received) != 2 || received[0].auth != "Bearer "+testToken || received[1].auth != "Bearer token-2" {
		t.Errorf("requests = %+v, want the original token then the rotated one", received)
	}
}

func TestNewRejectsBadOptions(t *testing.T) {
	reader := fake.NewClientBuilder().Build()
	valid := Options{ServerURL: "https://argocd-server", Token: tokenKey, Reader: reader}
	if _, err := New(valid); err != nil {
		t.Fatalf("New(valid) = %v", err)
	}

	for _, tc := range []struct {
		name   string
		mutate func(*Options)
	}{
		{"unparsable URL", func(o *Options) { o.ServerURL = "https://[::1" }},
		{"no scheme", func(o *Options) { o.ServerURL = "argocd-server" }},
		{"no host", func(o *Options) { o.ServerURL = "https://" }},
		{"no reader", func(o *Options) { o.Reader = nil }},
		{"no token name", func(o *Options) { o.Token.Name = "" }},
		{"no token key", func(o *Options) { o.Token.Key = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := valid
			tc.mutate(&opts)
			if _, err := New(opts); err == nil {
				t.Error("New succeeded")
			}
		})
	}
}
