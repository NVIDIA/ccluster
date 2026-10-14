/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package computedeployment describes the values of
// catalogs/deployments/compute_deployment: a single pod running a user image behind an
// SSH launcher, with a persistent home volume and a LoadBalancer Service exposing the
// SSH port.
package computedeployment

import (
	"errors"
	"fmt"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
	"github.com/NVIDIA/ccluster/catalogs/deployments"
)

// Chart is the chart directory, relative to the repository root.
const Chart = "catalogs/deployments/compute_deployment"

// Values is the top-level values document for
// catalogs/deployments/compute_deployment.
type Values struct {
	deployments.Workload

	// Domain is the DNS zone the deployment's hostname is created in, by external-dns
	// from the Service. The host is <name>.<namespace>.<domain> unless DeploymentURL
	// overrides it. Required.
	Domain string `json:"domain"`

	// DeploymentURL replaces the derived hostname with a full one.
	DeploymentURL *string `json:"deploymentUrl,omitempty"`

	// ContainerPort is the port the SSH launcher listens on inside the pod. Required.
	ContainerPort int32 `json:"containerPort"`

	// ExposedPort is the port the LoadBalancer Service exposes ContainerPort on.
	// Required.
	ExposedPort int32 `json:"exposedPort"`

	// RevisionNumber is this deployment's revision. It labels the pod, so bumping it
	// recreates the pod. Unset, no revision label is rendered.
	RevisionNumber *int64 `json:"revisionNumber,omitempty"`

	// StorageClassName provisions the home volume, unless HomePVCName supplies one.
	// Its size is 100Gi per requested accelerator, and at least 100Gi. Required.
	StorageClassName string `json:"storageClassName"`

	// HomePVCName mounts an existing claim at /home instead of the chart creating one.
	// The SSH host keys then live under a per-deployment directory on it.
	HomePVCName *string `json:"homePvcName,omitempty"`

	// VolumeMounts are additional existing claims mounted into the container.
	VolumeMounts []deployments.VolumeMount `json:"volumeMounts,omitempty"`

	// SSHInjectorImage carries the compute-ssh launcher (cmd/compute-ssh) that an init
	// container copies into the pod. The image must provide /compute-ssh. Unset, the
	// chart uses ghcr.io/nvidia/ccluster-compute-ssh at this repository's VERSION.
	SSHInjectorImage *string `json:"sshInjectorImage,omitempty"`

	// SSHPublicKey is an authorized_keys entry, mounted from a ConfigMap. It is the
	// only way to log in. Required.
	SSHPublicKey string `json:"sshPublicKey"`
}

// New returns the compute_deployment chart entry, installed into namespace with
// values. The chart's objects are all created in that namespace, and its hostname is
// derived from it.
func New(
	namespace string,
	values Values,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	return catalogs.Entry(Chart, namespace, values)
}

// Validate implements catalogs.Validator: the required values that are unset, and the
// values that are malformed.
func (v Values) Validate() error {
	errs := []error{
		v.Workload.Validate(),
		deployments.MissingError(deployments.Missing(
			deployments.Field{Name: "domain", Value: v.Domain},
			deployments.Field{Name: "storageClassName", Value: v.StorageClassName},
			deployments.Field{Name: "sshPublicKey", Value: v.SSHPublicKey},
		)),
		deployments.ValidateVolumeMounts(v.VolumeMounts),
	}
	if v.SSHInjectorImage != nil && *v.SSHInjectorImage == "" {
		errs = append(errs, fmt.Errorf("%w: sshInjectorImage is empty; leave it unset for the default",
			deployments.ErrInvalidValue))
	}
	if v.RevisionNumber != nil && *v.RevisionNumber < 0 {
		errs = append(errs, fmt.Errorf("%w: revisionNumber %d is negative",
			deployments.ErrInvalidValue, *v.RevisionNumber))
	}
	for _, port := range []struct {
		name  string
		value int32
	}{
		{"containerPort", v.ContainerPort},
		{"exposedPort", v.ExposedPort},
	} {
		if port.value == 0 {
			errs = append(errs, deployments.MissingError([]string{port.name}))
		} else if err := deployments.ValidatePort(port.name, port.value); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
