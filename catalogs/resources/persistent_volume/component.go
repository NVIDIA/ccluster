/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package persistentvolume describes the values of catalogs/resources/persistent_volume:
// one tenant volume, as a PersistentVolumeClaim named vol-<volumeId> in the release
// namespace. A filesystem volume is provisioned dynamically from a StorageClass; an
// object volume is a static PersistentVolume over an S3 bucket through the Mountpoint
// CSI driver, bound to the claim. Workload charts mount the claim through their
// volumeMounts value.
package persistentvolume

import (
	"errors"
	"fmt"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
	"github.com/NVIDIA/ccluster/catalogs/deployments"
)

// Chart is the chart directory, relative to the repository root.
const Chart = "catalogs/resources/persistent_volume"

// Values is the top-level values document for catalogs/resources/persistent_volume.
// Exactly one of Filesystem and Object is set.
type Values struct {
	// VolumeID identifies the volume; the claim is named vol-<volumeId>, so it must be
	// unique in the namespace and stable for the volume's lifetime. Required.
	VolumeID string `json:"volumeId"`

	// Name is the volume's display name, carried as a label. Required.
	Name string `json:"name"`

	// Filesystem provisions the volume dynamically.
	Filesystem *Filesystem `json:"filesystem,omitempty"`

	// Object backs the volume with an S3 bucket.
	Object *Object `json:"object,omitempty"`
}

// Filesystem is a dynamically provisioned volume.
type Filesystem struct {
	// SizeGB is the requested capacity in GiB. Required.
	SizeGB int64 `json:"sizeGb"`

	// StorageClassName provisions it. A WaitForFirstConsumer class leaves the claim
	// Pending until a workload mounts it. Required.
	StorageClassName string `json:"storageClassName"`
}

// Object is a static volume over an S3 bucket, mounted by the Mountpoint CSI driver
// with the driver's own IAM identity.
type Object struct {
	// Bucket is the bucket name. Required.
	Bucket string `json:"bucket"`

	// Region is the bucket's AWS region. Required.
	Region string `json:"region"`

	// Prefix restricts the mount to one key prefix.
	Prefix *string `json:"prefix,omitempty"`

	// ReadOnly mounts the bucket read-only. The chart ships false.
	ReadOnly *bool `json:"readOnly,omitempty"`
}

// New returns the persistent_volume chart entry, creating the claim in namespace.
func New(
	namespace string,
	values Values,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	return catalogs.Entry(Chart, namespace, values)
}

// Validate implements catalogs.Validator: the identifiers are set, exactly one backend
// is chosen, and that backend's required values are present.
func (v Values) Validate() error {
	errs := []error{deployments.MissingError(deployments.Missing(
		deployments.Field{Name: "volumeId", Value: v.VolumeID},
		deployments.Field{Name: "name", Value: v.Name},
	))}
	switch {
	case v.Filesystem != nil && v.Object != nil:
		errs = append(errs, fmt.Errorf("%w: filesystem and object are mutually exclusive",
			deployments.ErrInvalidValue))
	case v.Filesystem == nil && v.Object == nil:
		errs = append(errs, fmt.Errorf("%w: one of filesystem or object", deployments.ErrMissingValue))
	case v.Filesystem != nil:
		errs = append(errs, deployments.MissingError(deployments.Missing(
			deployments.Field{Name: "filesystem.storageClassName", Value: v.Filesystem.StorageClassName},
		)))
		if v.Filesystem.SizeGB < 1 {
			errs = append(errs, fmt.Errorf("%w: filesystem.sizeGb must be at least 1",
				deployments.ErrInvalidValue))
		}
	case v.Object != nil:
		errs = append(errs, deployments.MissingError(deployments.Missing(
			deployments.Field{Name: "object.bucket", Value: v.Object.Bucket},
			deployments.Field{Name: "object.region", Value: v.Object.Region},
		)))
	}
	return errors.Join(errs...)
}
