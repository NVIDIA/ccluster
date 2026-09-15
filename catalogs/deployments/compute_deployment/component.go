/*
Copyright 2026 NVIDIA CORPORATION & AFFILIATES.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
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

	// StorageClassName provisions the home volume. Its size is 100Gi per requested
	// accelerator, and at least 100Gi. Required.
	StorageClassName string `json:"storageClassName"`

	// SSHInjectorImage carries the compute-ssh launcher that an init container copies
	// into the pod. Required.
	SSHInjectorImage string `json:"sshInjectorImage"`

	// SSHUsername is the login name the launcher accepts.
	SSHUsername *string `json:"sshUsername,omitempty"`

	// SSHPassword is a login password, stored in a Secret. One of SSHPassword,
	// SSHGithubUsername or SSHPublicKey is required.
	SSHPassword *string `json:"sshPassword,omitempty"`

	// SSHGithubUsername makes the launcher accept that GitHub user's public keys.
	SSHGithubUsername *string `json:"sshGithubUsername,omitempty"`

	// SSHPublicKey is an authorized_keys entry, mounted from a ConfigMap.
	SSHPublicKey *string `json:"sshPublicKey,omitempty"`
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

// Validate implements catalogs.Validator: the required values that are unset, and that
// at least one way to log in is configured.
func (v Values) Validate() error {
	errs := []error{
		v.Workload.Validate(),
		deployments.MissingError(deployments.Missing(
			deployments.Field{Name: "domain", Value: v.Domain},
			deployments.Field{Name: "storageClassName", Value: v.StorageClassName},
			deployments.Field{Name: "sshInjectorImage", Value: v.SSHInjectorImage},
		)),
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
	if !set(v.SSHPassword) && !set(v.SSHGithubUsername) && !set(v.SSHPublicKey) {
		errs = append(errs, fmt.Errorf("%w: one of sshPassword, sshGithubUsername or sshPublicKey",
			deployments.ErrMissingValue))
	}
	return errors.Join(errs...)
}

func set(s *string) bool { return s != nil && *s != "" }
