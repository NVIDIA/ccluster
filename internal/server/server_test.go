/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package server

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stesting "k8s.io/client-go/testing"

	v1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/internal/server/ui"
	"github.com/NVIDIA/ccluster/pkg/client/clientset/versioned/fake"
)

var testUI = fstest.MapFS{
	"index.html":         {Data: []byte("<!doctype html><title>test</title>")},
	"assets/app-abc.js":  {Data: []byte("console.log(1)")},
	"favicon.svg":        {Data: []byte("<svg/>")},
	"assets/app-abc.css": {Data: []byte("body{}")},
}

func newTestServer(t *testing.T, opts Options, objects ...runtime.Object) (http.Handler, *fake.Clientset) {
	t.Helper()
	clientset := fake.NewClientset(objects...)
	opts.Clientset = clientset
	if opts.UI == nil {
		opts.UI = testUI
	}
	opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(opts), clientset
}

func get(t *testing.T, handler http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, handler, http.MethodGet, target)
}

func do(t *testing.T, handler http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, nil)
	request.Host = "127.0.0.1:8484"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func decode[T any](t *testing.T, recorder *httptest.ResponseRecorder) T {
	t.Helper()
	var body T
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("response %q is not JSON: %v", recorder.Body.String(), err)
	}
	return body
}

func objects() []runtime.Object {
	planeA := infraPlane("team-a", "gpu", 1)
	planeB := infraPlane("team-b", "cpu", 1)
	setA := componentSet("team-a", "base", 1, finishedConditions(1)...)
	return []runtime.Object{&planeA, &planeB, &setA}
}

func TestConfig(t *testing.T) {
	handler, _ := newTestServer(t, Options{Namespace: "team-a", Context: "kind-cp", Version: "v1.2.3"})
	recorder := get(t, handler, "/api/v1/config")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
	want := Config{Context: "kind-cp", Version: "v1.2.3", Namespace: "team-a", RefreshSeconds: 5}
	if got := decode[Config](t, recorder); got != want {
		t.Errorf("config = %+v, want %+v", got, want)
	}
}

func TestOverviewAcrossTheCluster(t *testing.T) {
	handler, _ := newTestServer(t, Options{}, objects()...)

	got := decode[Overview](t, get(t, handler, "/api/v1/overview"))
	if strings.Join(got.Namespaces, ",") != "team-a,team-b" || got.Selected != "" {
		t.Errorf("namespaces = %v, selected = %q", got.Namespaces, got.Selected)
	}
	if len(got.InfraPlanes) != 2 || len(got.ComponentSets) != 1 {
		t.Errorf("overview lists %d planes and %d sets, want 2 and 1", len(got.InfraPlanes), len(got.ComponentSets))
	}

	got = decode[Overview](t, get(t, handler, "/api/v1/overview?namespace=team-b"))
	if got.Selected != "team-b" || len(got.InfraPlanes) != 1 || len(got.ComponentSets) != 0 {
		t.Errorf("team-b overview = %+v", got)
	}
}

func TestOverviewOfTheServedNamespaceOnly(t *testing.T) {
	handler, clientset := newTestServer(t, Options{Namespace: "team-a"}, objects()...)

	got := decode[Overview](t, get(t, handler, "/api/v1/overview"))
	if strings.Join(got.Namespaces, ",") != "team-a" || got.Selected != "team-a" {
		t.Errorf("namespaces = %v, selected = %q, want only team-a", got.Namespaces, got.Selected)
	}
	if len(got.InfraPlanes) != 1 || got.InfraPlanes[0].Name != "gpu" {
		t.Errorf("infra planes = %+v, want team-a/gpu", got.InfraPlanes)
	}
	for _, action := range clientset.Actions() {
		if action.GetNamespace() != "team-a" {
			t.Errorf("%s %s in namespace %q, want only team-a", action.GetVerb(), action.GetResource().Resource, action.GetNamespace())
		}
	}

	clientset.ClearActions()
	recorder := get(t, handler, "/api/v1/overview?namespace=team-b")
	if recorder.Code != http.StatusNotFound || decode[Error](t, recorder).Reason != ReasonNamespaceNotServed {
		t.Errorf("other namespace answered %d %s, want 404 %s", recorder.Code, recorder.Body, ReasonNamespaceNotServed)
	}
	if actions := clientset.Actions(); len(actions) != 0 {
		t.Errorf("other namespace called Kubernetes: %v", actions)
	}
}

func forbid(clientset *fake.Clientset) {
	clientset.PrependReactor("list", "cclinfraplanes", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "cclinfraplanes"}, "",
			errors.New(`User "dev" cannot list resource "cclinfraplanes"`))
	})
}

func TestForbiddenReads(t *testing.T) {
	t.Run("across the cluster", func(t *testing.T) {
		handler, clientset := newTestServer(t, Options{})
		forbid(clientset)
		recorder := get(t, handler, "/api/v1/overview")
		body := decode[Error](t, recorder)
		if recorder.Code != http.StatusForbidden || body.Reason != ReasonClusterReadForbidden {
			t.Errorf("answered %d %+v, want 403 %s", recorder.Code, body, ReasonClusterReadForbidden)
		}
		if !strings.Contains(body.Message, `cannot list resource "cclinfraplanes"`) {
			t.Errorf("message %q does not carry the API server's", body.Message)
		}
	})

	t.Run("in the served namespace", func(t *testing.T) {
		handler, clientset := newTestServer(t, Options{Namespace: "team-a"})
		forbid(clientset)
		recorder := get(t, handler, "/api/v1/overview")
		if body := decode[Error](t, recorder); recorder.Code != http.StatusForbidden || body.Reason != string(metav1.StatusReasonForbidden) {
			t.Errorf("answered %d %+v, want 403 Forbidden", recorder.Code, body)
		}
	})

	t.Run("API server unreachable", func(t *testing.T) {
		handler, clientset := newTestServer(t, Options{})
		clientset.PrependReactor("list", "*", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("dial tcp 127.0.0.1:6443: connect: connection refused")
		})
		recorder := get(t, handler, "/api/v1/overview")
		if body := decode[Error](t, recorder); recorder.Code != http.StatusBadGateway || body.Reason != ReasonKubernetesUnavailable {
			t.Errorf("answered %d %+v, want 502 %s", recorder.Code, body, ReasonKubernetesUnavailable)
		}
	})
}

func TestServesTheUI(t *testing.T) {
	handler, _ := newTestServer(t, Options{})

	for _, path := range []string{"/", "/index.html", "/componentsets/team-a/base", "/no/such/page"} {
		recorder := get(t, handler, path)
		if recorder.Code != http.StatusOK || recorder.Body.String() != "<!doctype html><title>test</title>" {
			t.Errorf("GET %s = %d %q, want index.html", path, recorder.Code, recorder.Body)
		}
		if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("GET %s Cache-Control = %q, want no-store", path, got)
		}
	}

	recorder := get(t, handler, "/assets/app-abc.js")
	if recorder.Code != http.StatusOK || recorder.Body.String() != "console.log(1)" {
		t.Errorf("asset = %d %q", recorder.Code, recorder.Body)
	}
	if got := recorder.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("asset Cache-Control = %q, want immutable", got)
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/javascript") {
		t.Errorf("asset Content-Type = %q", got)
	}

	if recorder := get(t, handler, "/assets/gone-123.js"); recorder.Code != http.StatusNotFound {
		t.Errorf("missing asset = %d, want 404 rather than index.html", recorder.Code)
	}
	if recorder := get(t, handler, "/favicon.svg"); recorder.Code != http.StatusOK || recorder.Body.String() != "<svg/>" {
		t.Errorf("favicon = %d %q", recorder.Code, recorder.Body)
	}
	if recorder := get(t, handler, "/api/v1/nothing"); recorder.Code != http.StatusNotFound ||
		decode[Error](t, recorder).Reason != string(metav1.StatusReasonNotFound) {
		t.Errorf("unknown API path = %d %s, want a JSON 404", recorder.Code, recorder.Body)
	}
}

func TestServesTheEmbeddedBuild(t *testing.T) {
	// The committed build must be present and reference only its own assets, or go
	// install would produce a dashboard without a UI.
	index, err := ui.FS().Open("index.html")
	if err != nil {
		t.Fatalf("the embedded UI has no index.html; run make ui-build: %v", err)
	}
	_ = index.Close()

	handler := New(Options{Clientset: fake.NewClientset()})
	recorder := get(t, handler, "/")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `<div id="root">`) {
		t.Errorf("GET / = %d %q, want the embedded index.html", recorder.Code, recorder.Body)
	}
	if strings.Contains(recorder.Body.String(), "<script>") {
		t.Error("index.html has an inline script, which the Content-Security-Policy blocks")
	}
}

func TestSecurity(t *testing.T) {
	handler, _ := newTestServer(t, Options{AllowedHosts: LoopbackHosts})

	recorder := get(t, handler, "/api/v1/config")
	for header, want := range map[string]string{
		"Content-Security-Policy": contentSecurityPolicy,
		"Referrer-Policy":         "no-referrer",
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Cache-Control":           "no-store",
	} {
		if got := recorder.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		for _, path := range []string{"/", "/api/v1/overview"} {
			if recorder := do(t, handler, method, path); recorder.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s = %d, want 405", method, path, recorder.Code)
			}
		}
	}

	for host, want := range map[string]int{
		"127.0.0.1:8484":         http.StatusOK,
		"localhost:8484":         http.StatusOK,
		"LOCALHOST":              http.StatusOK,
		"[::1]:8484":             http.StatusOK,
		"attacker.example:8484":  http.StatusMisdirectedRequest,
		"127.0.0.1.nip.io:8484":  http.StatusMisdirectedRequest,
		"localhost.example.com:": http.StatusMisdirectedRequest,
	} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)
		request.Host = host
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != want {
			t.Errorf("Host %s = %d, want %d", host, recorder.Code, want)
		}
	}
}
