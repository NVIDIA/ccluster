/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package registry is the shared shape of the operators' per-chart registries: a chart
// path mapped to a function whose signature the registry defines. A specific registry is
// a named type embedding Registry with its own function type, such as the ip operator's
// Verifiers.
package registry

import (
	"fmt"
	"reflect"
)

// Registry maps a chart path, relative to the repository root, to a function of type F.
// Ranging over it visits every registration in no particular order. The zero value is an
// empty registry.
//
// Registration happens while an operator is being wired up, before its manager starts;
// a Registry is not safe for Register calls concurrent with a running controller.
type Registry[F any] map[string]F

// Register adds f for chart. It panics on an empty chart, a nil f, or a chart registered
// twice: each is a wiring bug, caught at startup rather than at reconcile time.
func (r *Registry[F]) Register(chart string, f F) {
	if chart == "" {
		panic(fmt.Sprintf("registry: Register with an empty chart (%T)", f))
	}
	// F is any type, so it cannot be compared with nil directly.
	if v := reflect.ValueOf(f); !v.IsValid() || (v.Kind() == reflect.Func && v.IsNil()) {
		panic(fmt.Sprintf("registry: Register %s with a nil %T", chart, f))
	}
	if _, registered := (*r)[chart]; registered {
		panic(fmt.Sprintf("registry: %s registered twice (%T)", chart, f))
	}
	if *r == nil {
		*r = make(Registry[F])
	}
	(*r)[chart] = f
}

// Lookup returns chart's function, and whether chart is registered.
func (r Registry[F]) Lookup(chart string) (F, bool) {
	f, ok := r[chart]
	return f, ok
}
