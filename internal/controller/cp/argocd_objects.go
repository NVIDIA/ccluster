/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

import (
	"context"
	"encoding/json"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// argoCDObject is an Argo CD object the operator owns outright. The patch below sends
// ObjectMeta as one value, which client.Object cannot give it - hence ObjectMetaAccessor.
type argoCDObject interface {
	client.Object
	metav1.ObjectMetaAccessor
}

// staleRead reports whether err means the read behind this write is out of date: the
// server has a newer version (Conflict), or it has an object the cached read missed
// (AlreadyExists). Both are HTTP 409 with different reasons. Reading again fixes both.
func staleRead(err error) bool {
	return apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err)
}

// createOrPatchArgoCDObject creates obj if it has no resource version, and otherwise
// replaces its metadata and spec - but only if obj differs from before. The caller reads
// obj, copies it into before, checks ownership and sets obj.Spec.
//
// The write is a JSON Patch of the two fields the operator owns. An update would send the
// whole object, and these partial types do not model Argo CD's status, a pending
// operation, or any undeclared spec field - the API server would delete all three.
// Replacing /spec still reverts hand edits, and unlike a merge patch it keeps explicit
// null Helm values.
//
// The comparison only sees modelled fields, because that is all a partial read returns.
// A hand edit confined to unmodelled spec fields is therefore cleared the next time
// something visible changes, not when it is made.
func createOrPatchArgoCDObject(
	ctx context.Context,
	c client.Client,
	before, obj argoCDObject,
	spec any,
) (controllerutil.OperationResult, error) {
	version := obj.GetResourceVersion()
	if version == "" {
		if err := c.Create(ctx, obj); err != nil {
			return controllerutil.OperationResultNone, err
		}
		return controllerutil.OperationResultCreated, nil
	}

	if equality.Semantic.DeepEqual(before, obj) {
		return controllerutil.OperationResultNone, nil
	}

	patch, err := json.Marshal([]struct {
		Op    string `json:"op"`
		Path  string `json:"path"`
		Value any    `json:"value"`
	}{
		{Op: "replace", Path: "/metadata", Value: obj.GetObjectMeta()},
		{Op: "replace", Path: "/spec", Value: spec},
	})
	if err != nil {
		return controllerutil.OperationResultNone, err
	}

	// metadata carries the resource version from the read, so a write built on a stale
	// read is rejected rather than applied. The caller retries it - see staleRead.
	if err := c.Patch(ctx, obj, client.RawPatch(types.JSONPatchType, patch)); err != nil {
		return controllerutil.OperationResultNone, err
	}
	// The comparison above is over Go values, the server's is over the stored object.
	// apiextensionsv1.JSON compares as bytes, so Helm values differing only in key order
	// or number format look changed here and store as no change there.
	if obj.GetResourceVersion() == version {
		return controllerutil.OperationResultNone, nil
	}
	return controllerutil.OperationResultUpdated, nil
}
