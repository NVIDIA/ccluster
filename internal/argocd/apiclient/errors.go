/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package apiclient

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Failures a caller can act on. A request that fails with one of them returns an
// *APIError that wraps it, so errors.Is selects the case; any other failed request
// returns an *APIError that wraps none of them.
var (
	// ErrUnauthorized: argocd-server rejected the token, or the account it belongs to
	// may not read the Application.
	ErrUnauthorized = errors.New("not authorized by argocd-server")

	// ErrApplicationNotFound: the Application does not exist, for instance because it
	// was deleted between listing it and reading its tree.
	ErrApplicationNotFound = errors.New("application not found")

	// ErrTreeUnavailable: the application controller has not cached the Application's
	// resource tree yet, as happens for a while after it starts or after the
	// Application is created. Retrying later succeeds.
	ErrTreeUnavailable = errors.New("resource tree not cached yet")

	// ErrResourceGone: the object is in the cached tree but no longer in the cluster,
	// typically a Pod replaced since the tree was read.
	ErrResourceGone = errors.New("resource no longer exists")
)

// Substrings of argocd-server's error messages that identify the cases above. None of
// them has a code of its own: each is an ordinary status code that other failures also
// return.
const (
	// The resource tree read when the application controller's cache has no entry.
	treeCacheMissMessage = "cache: key is missing"
	// A resource read for an object the cached tree lists but the cluster no longer has.
	staleTreeMessage = "not found as part of application"
	// The Kubernetes NotFound for the Application itself, as opposed to a child object.
	applicationNotFoundMessage = "applications.argoproj.io"
)

// APIError is a request argocd-server answered with a status other than 200.
type APIError struct {
	// Op is the request: "resource-tree", "resource" or "events", the last element of
	// its path.
	Op         string
	StatusCode int
	// Message is argocd-server's description of the failure.
	Message string

	kind error
}

func (e *APIError) Error() string {
	if e.kind != nil {
		return fmt.Sprintf("argocd-server %s: %v (HTTP %d): %s", e.Op, e.kind, e.StatusCode, e.Message)
	}
	return fmt.Sprintf("argocd-server %s: HTTP %d: %s", e.Op, e.StatusCode, e.Message)
}

// Unwrap returns the case the failure was classified as, or nil.
func (e *APIError) Unwrap() error {
	return e.kind
}

// classify returns the case a failed op falls under, or nil for one a caller has nothing
// to do about but report. Only a read of one object can find that object gone.
func classify(op string, statusCode int, message string) error {
	objectRead := op == opResource || op == opEvents
	switch {
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden:
		return ErrUnauthorized
	case statusCode == http.StatusNotFound && strings.Contains(message, applicationNotFoundMessage):
		return ErrApplicationNotFound
	case objectRead && statusCode == http.StatusNotFound:
		return ErrResourceGone
	case objectRead && statusCode == http.StatusBadRequest && strings.Contains(message, staleTreeMessage):
		return ErrResourceGone
	case statusCode == http.StatusInternalServerError && strings.Contains(message, treeCacheMissMessage):
		return ErrTreeUnavailable
	}
	return nil
}
