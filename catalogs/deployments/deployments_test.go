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

package deployments_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/NVIDIA/ccluster/catalogs/deployments"
)

func ptr[T any](v T) *T { return &v }

// valid is a Workload every required value set; tests copy and break it.
func valid() deployments.Workload {
	return deployments.Workload{
		Name:         "llama",
		DeploymentID: "1",
		ImageURL:     "registry.example.com/vllm:latest",
		Resources: deployments.ResourceRequirements{
			Limits:   deployments.Resources{CPU: "4", Memory: "16Gi"},
			Requests: deployments.Resources{CPU: "2", Memory: "8Gi"},
		},
	}
}

func TestWorkloadValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*deployments.Workload)
		wantErr error  // nil means valid
		wantMsg string // substring of the error, when wantErr is set
	}{
		{name: "complete", mutate: func(*deployments.Workload) {}},
		{
			name:    "missing name and image",
			mutate:  func(w *deployments.Workload) { w.Name, w.ImageURL = "", "" },
			wantErr: deployments.ErrMissingValue, wantMsg: "name, imageUrl",
		},
		{
			name:    "missing a quantity",
			mutate:  func(w *deployments.Workload) { w.Resources.Requests.Memory = "" },
			wantErr: deployments.ErrMissingValue, wantMsg: "resources.requests.memory",
		},
		{
			name:    "negative accelerator count",
			mutate:  func(w *deployments.Workload) { w.Resources.Limits.AcceleratorCount = ptr(int32(-1)) },
			wantErr: deployments.ErrInvalidValue, wantMsg: "acceleratorCount",
		},
		{
			name: "incomplete pull secret",
			mutate: func(w *deployments.Workload) {
				w.ImagePullSecret = &deployments.ImagePullSecret{Server: "registry.example.com"}
			},
			wantErr: deployments.ErrMissingValue, wantMsg: "imagePullSecret.username, imagePullSecret.password",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			w := valid()
			test.mutate(&w)
			check(t, w.Validate(), test.wantErr, test.wantMsg)
		})
	}
}

func TestConfigFileValidate(t *testing.T) {
	long := strings.Repeat("a", deployments.ConfigFilePathMax)
	tests := []struct {
		name    string
		file    *deployments.ConfigFile
		wantErr error
		wantMsg string
	}{
		{name: "unset", file: nil},
		{name: "plain", file: &deployments.ConfigFile{Filename: "cfg.yaml", MountPath: "/etc/app", Content: "a: 1"}},
		{
			name:    "empty",
			file:    &deployments.ConfigFile{},
			wantErr: deployments.ErrMissingValue, wantMsg: "configFile.filename",
		},
		{
			name:    "filename with a slash",
			file:    &deployments.ConfigFile{Filename: "a/b", MountPath: "/etc", Content: "x"},
			wantErr: deployments.ErrInvalidValue, wantMsg: "filename",
		},
		{
			name:    "filename is dot-dot",
			file:    &deployments.ConfigFile{Filename: "..", MountPath: "/etc", Content: "x"},
			wantErr: deployments.ErrInvalidValue, wantMsg: "filename",
		},
		{
			name:    "relative mount path",
			file:    &deployments.ConfigFile{Filename: "a", MountPath: "etc", Content: "x"},
			wantErr: deployments.ErrInvalidValue, wantMsg: "not absolute",
		},
		{
			name:    "mount path escapes under /proc via dot-dot",
			file:    &deployments.ConfigFile{Filename: "a", MountPath: "/etc/../proc/x", Content: "x"},
			wantErr: deployments.ErrInvalidValue, wantMsg: `".." segment`,
		},
		{
			name:    "mount path under /dev",
			file:    &deployments.ConfigFile{Filename: "a", MountPath: "/dev/shm/", Content: "x"},
			wantErr: deployments.ErrInvalidValue, wantMsg: "under /dev",
		},
		{
			// Component-aligned: /devtmp is not /dev.
			name: "mount path merely prefixed by a forbidden root",
			file: &deployments.ConfigFile{Filename: "a", MountPath: "/devtmp", Content: "x"},
		},
		{
			name:    "root mount path",
			file:    &deployments.ConfigFile{Filename: "a", MountPath: "/", Content: "x"},
			wantErr: deployments.ErrInvalidValue, wantMsg: "root",
		},
		{
			name: "combined path too long",
			file: &deployments.ConfigFile{
				Filename: "abc", MountPath: "/" + long[:deployments.ConfigFilePathMax-3], Content: "x",
			},
			wantErr: deployments.ErrInvalidValue, wantMsg: "mountPath/filename",
		},
		{
			name: "content too large",
			file: &deployments.ConfigFile{
				Filename: "a", MountPath: "/etc",
				Content: strings.Repeat("x", deployments.ConfigFileContentMaxBytes+1),
			},
			wantErr: deployments.ErrInvalidValue, wantMsg: "content",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			check(t, test.file.Validate(), test.wantErr, test.wantMsg)
		})
	}
}

func TestValidatePort(t *testing.T) {
	for _, port := range []int32{0, -1, 65536, 15020} {
		if err := deployments.ValidatePort("p", port); !errors.Is(err, deployments.ErrInvalidValue) {
			t.Errorf("ValidatePort(%d) = %v, want ErrInvalidValue", port, err)
		}
	}
	if err := deployments.ValidatePort("p", 8000); err != nil {
		t.Errorf("ValidatePort(8000) = %v, want nil", err)
	}
}

// check asserts err against the expected sentinel and message fragment.
func check(t *testing.T, err, wantErr error, wantMsg string) {
	t.Helper()
	if wantErr == nil {
		if err != nil {
			t.Fatalf("Validate() = %v, want nil", err)
		}
		return
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("Validate() = %v, want one wrapping %v", err, wantErr)
	}
	if !strings.Contains(err.Error(), wantMsg) {
		t.Fatalf("Validate() = %q, want it to mention %q", err, wantMsg)
	}
}
