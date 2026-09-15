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

package jobdeployment_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/NVIDIA/ccluster/catalogs/deployments"
	jobdeployment "github.com/NVIDIA/ccluster/catalogs/deployments/job_deployment"
)

func ptr[T any](v T) *T { return &v }

func valid() jobdeployment.Values {
	return jobdeployment.Values{
		Workload: deployments.Workload{
			Name:         "train",
			DeploymentID: "7",
			ImageURL:     "registry.example.com/train:latest",
			Resources: deployments.ResourceRequirements{
				Limits:   deployments.Resources{CPU: "4", Memory: "16Gi"},
				Requests: deployments.Resources{CPU: "2", Memory: "8Gi"},
			},
		},
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*jobdeployment.Values)
		wantErr error
		wantMsg string
	}{
		{name: "required only", mutate: func(*jobdeployment.Values) {}},
		{
			name: "counts and deadline",
			mutate: func(v *jobdeployment.Values) {
				v.Completions, v.Parallelism, v.BackoffLimit = ptr(int32(4)), ptr(int32(2)), ptr(int32(0))
				v.ActiveDeadlineSeconds = ptr(int64(3600))
			},
		},
		{
			name:    "zero completions",
			mutate:  func(v *jobdeployment.Values) { v.Completions = ptr(int32(0)) },
			wantErr: deployments.ErrInvalidValue, wantMsg: "completions 0 is below 1",
		},
		{
			name:    "negative backoff",
			mutate:  func(v *jobdeployment.Values) { v.BackoffLimit = ptr(int32(-1)) },
			wantErr: deployments.ErrInvalidValue, wantMsg: "backoffLimit",
		},
		{
			name:    "zero deadline",
			mutate:  func(v *jobdeployment.Values) { v.ActiveDeadlineSeconds = ptr(int64(0)) },
			wantErr: deployments.ErrInvalidValue, wantMsg: "activeDeadlineSeconds",
		},
		{
			name: "bad config file",
			mutate: func(v *jobdeployment.Values) {
				v.ConfigFile = &deployments.ConfigFile{Filename: "c", MountPath: "etc", Content: "x"}
			},
			wantErr: deployments.ErrInvalidValue, wantMsg: "not absolute",
		},
		{
			name:    "workload errors surface",
			mutate:  func(v *jobdeployment.Values) { v.DeploymentID = "" },
			wantErr: deployments.ErrMissingValue, wantMsg: "deploymentId",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			v := valid()
			test.mutate(&v)
			_, err := jobdeployment.New("user-7", v)
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
