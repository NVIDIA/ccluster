/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package server is the HTTP server behind ccluster server: a read-only dashboard of a
// control plane's infra planes and component sets.
//
// One handler serves both halves from one origin. Under /api/v1/ it answers JSON, read
// from Kubernetes on every request with the clientset a ClientSource picks for it, so
// Kubernetes RBAC alone decides what a request may see. Every other GET returns the
// React UI embedded by package ui: its hashed assets as files, and any other path
// index.html, which routes in the browser.
//
// Without Options.Namespace the server reads across the cluster and offers every
// namespace holding either kind; with it, it reads only that namespace and answers 404
// for any other without calling Kubernetes.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/ccluster/internal/server/ui"
)

// RefreshInterval is how often the UI reloads the overview.
const RefreshInterval = 5 * time.Second

// requestTimeout bounds the Kubernetes reads behind one API request.
const requestTimeout = 15 * time.Second

// The reasons of the errors the API answers with, besides Kubernetes' own.
const (
	// ReasonClusterReadForbidden: without a namespace, listing across the cluster was
	// forbidden; reading one namespace may still be allowed.
	ReasonClusterReadForbidden = "ClusterReadForbidden"
	// ReasonNamespaceNotServed: the server reads only its own namespace.
	ReasonNamespaceNotServed = "NamespaceNotServed"
	// ReasonKubernetesUnavailable: the API server could not be reached or failed.
	ReasonKubernetesUnavailable = "KubernetesUnavailable"
	// ReasonNotFound: no such API path.
	ReasonNotFound = "NotFound"
)

// Options configures New.
type Options struct {
	// Namespace, when set, is the only namespace the server reads.
	Namespace string
	// Source picks each request's clientset.
	Source ClientSource
	// Context is the kubeconfig context shown in the UI; informational only.
	Context string
	// Version is the ccluster version shown in the UI.
	Version string
	// AllowedHosts, when set, are the only Host header names the server answers.
	AllowedHosts []string
	// UI is the files served besides the API. nil means the embedded build.
	UI fs.FS
	// Logger receives failed Kubernetes reads. nil means slog.Default().
	Logger *slog.Logger
}

// Config is the body of GET /api/v1/config.
type Config struct {
	Context string `json:"context"`
	Version string `json:"version"`
	// Namespace is the only namespace served, or "" for all of them.
	Namespace      string `json:"namespace"`
	RefreshSeconds int    `json:"refreshSeconds"`
}

// Error is the body of every API error.
type Error struct {
	Status  int    `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

type server struct {
	opts  Options
	ui    fs.FS
	index []byte
	log   *slog.Logger
}

// New returns the server's handler.
func New(opts Options) http.Handler {
	s := &server{opts: opts, ui: opts.UI, log: opts.Logger}
	if s.ui == nil {
		s.ui = ui.FS()
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	index, err := fs.ReadFile(s.ui, "index.html")
	if err != nil {
		index = []byte("<!doctype html><title>ccluster</title><p>This ccluster binary was built without its UI; run make ui-build.</p>")
	}
	s.index = index

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/config", s.config)
	mux.HandleFunc("GET /api/v1/overview", s.overview)
	mux.HandleFunc("GET /api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, ReasonNotFound, "No API at "+r.URL.Path+".")
	})
	mux.HandleFunc("GET /", s.static)
	return checkHost(opts.AllowedHosts, secureHeaders(mux))
}

func (s *server) config(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, Config{
		Context:        s.opts.Context,
		Version:        s.opts.Version,
		Namespace:      s.opts.Namespace,
		RefreshSeconds: int(RefreshInterval / time.Second),
	})
}

func (s *server) overview(w http.ResponseWriter, r *http.Request) {
	requested := r.URL.Query().Get("namespace")
	listNamespace := metav1.NamespaceAll
	var namespaces []string
	if fixed := s.opts.Namespace; fixed != "" {
		if requested != "" && requested != fixed {
			writeError(w, http.StatusNotFound, ReasonNamespaceNotServed,
				"This server shows only namespace "+fixed+".")
			return
		}
		requested, listNamespace, namespaces = fixed, fixed, []string{fixed}
	}

	client, err := s.opts.Source.Client(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, string(metav1.StatusReasonUnauthorized), err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	api := client.CclusterV1alpha1()
	planes, err := api.CCLInfraPlanes(listNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		s.writeKubernetesError(w, "CCLInfraPlanes", listNamespace, err)
		return
	}
	sets, err := api.CCLComponentSets(listNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		s.writeKubernetesError(w, "CCLComponentSets", listNamespace, err)
		return
	}
	writeJSON(w, http.StatusOK, buildOverview(planes.Items, sets.Items, requested, namespaces))
}

// writeKubernetesError answers with the status of a failed list: Kubernetes' own for an
// API error, so the UI can explain a 401 or 403, and 502 when the API server did not
// answer.
func (s *server) writeKubernetesError(w http.ResponseWriter, kind, namespace string, err error) {
	var status apierrors.APIStatus
	if !errors.As(err, &status) {
		s.log.Error("Failed to list "+kind, "namespace", namespace, "error", err)
		writeError(w, http.StatusBadGateway, ReasonKubernetesUnavailable,
			"Kubernetes could not be reached: "+err.Error())
		return
	}
	code := int(status.Status().Code)
	reason := string(status.Status().Reason)
	if code == 0 {
		code = http.StatusInternalServerError
	}
	if code == http.StatusForbidden && namespace == metav1.NamespaceAll {
		reason = ReasonClusterReadForbidden
	}
	if code >= http.StatusInternalServerError {
		s.log.Error("Failed to list "+kind, "namespace", namespace, "error", err)
	}
	writeError(w, code, reason, status.Status().Message)
}

// static serves the UI: a file that exists under its path, otherwise index.html, so a
// page the UI routes in the browser can be reloaded or linked to.
func (s *server) static(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name != "" && name != "." && name != "index.html" {
		if info, err := fs.Stat(s.ui, name); err == nil && !info.IsDir() {
			if strings.HasPrefix(name, "assets/") {
				// Vite puts a content hash in every asset's name.
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			http.ServeFileFS(w, r, s.ui, name)
			return
		}
		if strings.HasPrefix(name, "assets/") {
			http.NotFound(w, r)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(s.index)
	}
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, code int, reason, message string) {
	writeJSON(w, code, Error{Status: code, Reason: reason, Message: message})
}
