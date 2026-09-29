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

// patchPath is a path createOrPatchArgoCDObject writes on top of the metadata and spec it
// always writes, and the value to write there. Name one only when this write is what
// changed it - the value goes out as given.
type patchPath struct {
	path  string
	value any
}

// jsonPatchOp is one operation of an RFC 6902 patch.
type jsonPatchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value"`
}

// staleRead reports whether err means the read behind this write is out of date: the
// server has a newer version (Conflict), or it has an object the cached read missed
// (AlreadyExists). Both are HTTP 409 with different reasons. Reading again fixes both.
func staleRead(err error) bool {
	return apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err)
}

// createOrPatchArgoCDObject creates obj if it has no resource version, and otherwise
// patches its metadata and spec, plus any extra path the caller names - but only if obj
// differs from before.
//
// A JSON Patch, not an update: an update sends the whole object, and these partial types
// would take Argo CD's status and every unmodelled spec field with it. Unlike a merge
// patch it keeps explicit null Helm values. The comparison sees only modelled fields, so
// a hand edit confined to the others is cleared the next time something visible changes.
func createOrPatchArgoCDObject(
	ctx context.Context,
	c client.Client,
	before, obj argoCDObject,
	spec any,
	extra ...patchPath,
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

	fields := append([]patchPath{
		{path: "/metadata", value: obj.GetObjectMeta()},
		{path: "/spec", value: spec},
	}, extra...)

	// add, not replace: replace needs its target to exist, and /operation does not between
	// syncs. On a path that is there, add replaces it.
	ops := make([]jsonPatchOp, len(fields))
	for i, field := range fields {
		ops[i] = jsonPatchOp{Op: "add", Path: field.path, Value: field.value}
	}

	patch, err := json.Marshal(ops)
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
