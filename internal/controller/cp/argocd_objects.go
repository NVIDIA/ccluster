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
// patches its metadata and spec - but only if obj differs from before.
//
// JSON Patch preserves Argo CD-owned fields outside those two paths and replaces spec
// without merge-patch null deletion semantics. A whole-object update through these partial
// types would discard unmodelled fields.
//
// Writing /spec whole means the stored source holds only what the operator models.
// ApplicationSource keeps the keys it does not model when it reads one, so drift there is
// a difference the comparison sees and this write clears. Outside source - syncPolicy
// most of all - the comparison still sees only modelled fields, and a hand edit confined
// to the others is cleared the next time something visible changes.
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

	// add, not replace: replace needs its target to exist, and metadata.annotations does
	// not on an Application written by hand without any. On a path that is there, add
	// replaces it.
	ops := []jsonPatchOp{
		{Op: "add", Path: "/metadata", Value: obj.GetObjectMeta()},
		{Op: "add", Path: "/spec", Value: spec},
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
	// A patch that leaves the stored object unchanged does not advance its resource version.
	if obj.GetResourceVersion() == version {
		return controllerutil.OperationResultNone, nil
	}
	return controllerutil.OperationResultUpdated, nil
}
