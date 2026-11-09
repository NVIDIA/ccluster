/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package deploymentstest

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/NVIDIA/ccluster/catalogs"
)

// Reader is a catalogs.ResourceReader over fixed objects, keyed by "Kind/name". It
// counts reads, so a test can hold a health function to the few reads it should make.
type Reader struct {
	Objects  map[string]map[string]any
	EventsOf map[string][]corev1.Event
	// Err fails every read.
	Err error

	Reads int
}

// Get returns the object stored under ref's kind and name, nil if there is none.
func (r *Reader) Get(_ context.Context, ref catalogs.ResourceRef) (*unstructured.Unstructured, error) {
	r.Reads++
	if r.Err != nil {
		return nil, r.Err
	}
	obj, ok := r.Objects[ref.Kind+"/"+ref.Name]
	if !ok {
		return nil, nil
	}
	return &unstructured.Unstructured{Object: obj}, nil
}

// Events returns the events stored under ref's kind and name.
func (r *Reader) Events(_ context.Context, ref catalogs.ResourceRef) ([]corev1.Event, error) {
	r.Reads++
	if r.Err != nil {
		return nil, r.Err
	}
	return r.EventsOf[ref.Kind+"/"+ref.Name], nil
}

// Object converts a typed object into the map a Reader holds.
func Object(t testing.TB, obj runtime.Object) map[string]any {
	t.Helper()
	out, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
