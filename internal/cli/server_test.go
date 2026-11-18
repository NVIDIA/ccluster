/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/ccluster/internal/server"
	"github.com/NVIDIA/ccluster/pkg/cclclient"
)

// startServer runs ccluster server with args until the test ends and returns the URL it
// would have opened in the browser.
func startServer(t *testing.T, args ...string) string {
	t.Helper()

	opened := make(chan string, 1)
	previous := openBrowser
	openBrowser = func(url string) error {
		opened <- url
		return nil
	}
	t.Cleanup(func() { openBrowser = previous })

	ctx, cancel := context.WithCancel(t.Context())
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		cmd := NewRootCommand()
		cmd.SetArgs(append([]string{"server", "--listen", "127.0.0.1:0"}, args...))
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		done <- cmd.ExecuteContext(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("ccluster server returned %v after Ctrl-C, want nil; output:\n%s", err, out.String())
			}
		case <-time.After(10 * time.Second):
			t.Error("ccluster server did not stop after its context was cancelled")
		}
	})

	select {
	case url := <-opened:
		return url
	case err := <-done:
		t.Fatalf("ccluster server exited early: %v\n%s", err, out.String())
	case <-time.After(10 * time.Second):
		t.Fatal("ccluster server did not open a browser")
	}
	return ""
}

func getJSON(t *testing.T, url string, body any) int {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, body); err != nil {
		t.Fatalf("GET %s: %q is not JSON: %v", url, data, err)
	}
	return response.StatusCode
}

func TestServerServesTheDashboard(t *testing.T) {
	setUpInitialized(t)
	namespace := newNamespaceName()
	client := cclclient.New(namespace, kubeClient, clientset)
	if err := client.EnsureNamespace(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ApplyInfraPlane(t.Context(), cclclient.InfraPlaneOptions{Name: "edge-1"}); err != nil {
		t.Fatal(err)
	}

	url := startServer(t, "-n", namespace)
	if !strings.HasPrefix(url, "http://127.0.0.1:") || strings.HasSuffix(url, ":0") {
		t.Errorf("opened %q, want the bound loopback address", url)
	}

	var config server.Config
	if code := getJSON(t, url+"/api/v1/config", &config); code != http.StatusOK {
		t.Fatalf("config answered %d", code)
	}
	if config.Context != "cp" || config.Namespace != namespace || config.Version != Version() {
		t.Errorf("config = %+v, want context cp and namespace %s", config, namespace)
	}

	var overview server.Overview
	if code := getJSON(t, url+"/api/v1/overview", &overview); code != http.StatusOK {
		t.Fatalf("overview answered %d", code)
	}
	if len(overview.InfraPlanes) != 1 || overview.InfraPlanes[0].Name != "edge-1" {
		t.Errorf("infra planes = %+v, want %s/edge-1", overview.InfraPlanes, namespace)
	}

	response, err := http.Get(url + "/")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(page), `<div id="root">`) {
		t.Errorf("GET / = %d %q, want the UI", response.StatusCode, page)
	}
}

func TestServerRejects(t *testing.T) {
	t.Run("a non-loopback address", func(t *testing.T) {
		setUpInitialized(t)
		for _, listen := range []string{
			"0.0.0.0:8484", ":8484", "192.168.1.10:8484", "example.com:8484",
			// Loopback, but not a host name the server answers to.
			"127.0.0.2:8484", "[0:0:0:0:0:0:0:1]:8484",
		} {
			expectError(t, "is not a loopback address", "server", "--no-browser", "--listen", listen)
		}
		expectError(t, "is not host:port", "server", "--no-browser", "--listen", "127.0.0.1")
	})

	t.Run("no control plane", func(t *testing.T) {
		setUp(t)
		expectError(t, "run ccluster init first", "server", "--no-browser")
	})
}
