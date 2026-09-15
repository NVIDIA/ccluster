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

package computedeployment_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/NVIDIA/ccluster/catalogs/deployments"
	computedeployment "github.com/NVIDIA/ccluster/catalogs/deployments/compute_deployment"
)

func ptr[T any](v T) *T { return &v }

func valid() computedeployment.Values {
	return computedeployment.Values{
		Workload: deployments.Workload{
			Name:         "box",
			DeploymentID: "42",
			ImageURL:     "registry.example.com/box:latest",
			Resources: deployments.ResourceRequirements{
				Limits:   deployments.Resources{CPU: "4", Memory: "16Gi"},
				Requests: deployments.Resources{CPU: "2", Memory: "8Gi"},
			},
		},
		Domain:           "example.com",
		ContainerPort:    2222,
		ExposedPort:      22,
		StorageClassName: "fast",
		SSHInjectorImage: "registry.example.com/compute-ssh:latest",
		SSHPublicKey:     ptr("ssh-ed25519 AAAA"),
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*computedeployment.Values)
		wantErr error
		wantMsg string
	}{
		{name: "public key", mutate: func(*computedeployment.Values) {}},
		{
			name: "password only",
			mutate: func(v *computedeployment.Values) {
				v.SSHPublicKey, v.SSHPassword = nil, ptr("hunter2")
			},
		},
		{
			name: "github username only",
			mutate: func(v *computedeployment.Values) {
				v.SSHPublicKey, v.SSHGithubUsername = nil, ptr("octocat")
			},
		},
		{
			name: "no way to log in",
			mutate: func(v *computedeployment.Values) {
				v.SSHPublicKey, v.SSHPassword = nil, ptr("")
			},
			wantErr: deployments.ErrMissingValue, wantMsg: "one of sshPassword, sshGithubUsername or sshPublicKey",
		},
		{
			name: "missing chart-specific values",
			mutate: func(v *computedeployment.Values) {
				v.Domain, v.StorageClassName, v.SSHInjectorImage = "", "", ""
			},
			wantErr: deployments.ErrMissingValue, wantMsg: "domain, storageClassName, sshInjectorImage",
		},
		{
			name:    "missing exposed port",
			mutate:  func(v *computedeployment.Values) { v.ExposedPort = 0 },
			wantErr: deployments.ErrMissingValue, wantMsg: "exposedPort",
		},
		{
			name:    "container port out of range",
			mutate:  func(v *computedeployment.Values) { v.ContainerPort = 70000 },
			wantErr: deployments.ErrInvalidValue, wantMsg: "containerPort",
		},
		{
			name:    "workload errors surface",
			mutate:  func(v *computedeployment.Values) { v.ImageURL = "" },
			wantErr: deployments.ErrMissingValue, wantMsg: "imageUrl",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			v := valid()
			test.mutate(&v)
			_, err := computedeployment.New("user-7", v)
			if test.wantErr == nil {
				if err != nil {
					t.Fatalf("New() error = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("New() error = %v, want one wrapping %v", err, test.wantErr)
			}
			if !strings.Contains(err.Error(), test.wantMsg) {
				t.Fatalf("New() error = %q, want it to mention %q", err, test.wantMsg)
			}
		})
	}
}
