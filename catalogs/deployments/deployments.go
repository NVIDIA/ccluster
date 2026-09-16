/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package deployments holds what the workload charts under catalogs/deployments have in
// common: the pod-level values shared by inference_deployment, compute_deployment and
// job_deployment, and the validation rules for them. All three assume the namespace has
// had catalogs/namespace_base installed first.
//
// Unlike catalogs/cluster_base, these charts are written in this repository, so their
// values are the chart's own rather than an upstream chart's nested under its name: a
// chart's Values is the top-level values document, and the keys are camelCase like the
// CRDs. There is no sample rollout here either. A CCLComponentSet of deployments is
// assembled per tenant by whoever owns the tenant; the namespace it installs into is the
// caller's, as for every catalog chart, and the charts derive everything namespace-scoped
// from the release namespace instead of taking a user id.
//
// Every Values type here implements catalogs.Validator. The checks are the ones the
// chart cannot make itself - required values, port ranges, path rules - and only run for
// callers that go through the Go (or generated Python) types; see the catalogs package.
package deployments

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrMissingValue reports a value the chart cannot be installed without.
var ErrMissingValue = errors.New("required value not set")

// ErrInvalidValue reports a value the chart would render into something the cluster
// rejects, or that breaks an invariant the chart relies on.
var ErrInvalidValue = errors.New("invalid value")

// IstioReservedPorts are bound by the Istio sidecar on every meshed pod. A container or
// metrics port on one of them would be shadowed by the sidecar.
var IstioReservedPorts = []int32{15000, 15001, 15004, 15006, 15020, 15021, 15090}

// ValidatePort reports a port outside 1-65535 or on the Istio sidecar's reserved list.
// name is the value's key, for the error.
func ValidatePort(name string, port int32) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("%w: %s %d is outside 1-65535", ErrInvalidValue, name, port)
	}
	if slices.Contains(IstioReservedPorts, port) {
		return fmt.Errorf("%w: %s %d is reserved by the Istio sidecar",
			ErrInvalidValue, name, port)
	}
	return nil
}

// Field pairs a required string value with its key, for Missing.
type Field struct {
	Name  string
	Value string
}

// Missing returns the keys of the fields whose value is empty.
func Missing(fields ...Field) []string {
	var missing []string
	for _, f := range fields {
		if f.Value == "" {
			missing = append(missing, f.Name)
		}
	}
	return missing
}

// MissingError wraps ErrMissingValue around a list of keys, or returns nil for none.
func MissingError(names []string) error {
	if len(names) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrMissingValue, strings.Join(names, ", "))
}
