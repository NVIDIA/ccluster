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

package kaigpuqueues_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/NVIDIA/ccluster/catalogs/deployments"
	kaigpuqueues "github.com/NVIDIA/ccluster/catalogs/resources/kai_gpu_queues"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		values  kaigpuqueues.Values
		wantErr error
		wantMsg string
	}{
		{name: "root only", values: kaigpuqueues.Values{}},
		{
			name: "leaves",
			values: kaigpuqueues.Values{Queues: []kaigpuqueues.Queue{
				{Name: "org-7", GPUQuota: 8}, {Name: "org-8", GPUQuota: 0}, {Name: "org-9", GPUQuota: kaigpuqueues.Unlimited},
			}},
		},
		{
			name:    "unnamed leaf",
			values:  kaigpuqueues.Values{Queues: []kaigpuqueues.Queue{{GPUQuota: 1}}},
			wantErr: deployments.ErrMissingValue, wantMsg: "queues[0].name",
		},
		{
			name: "duplicate leaf",
			values: kaigpuqueues.Values{Queues: []kaigpuqueues.Queue{
				{Name: "org-7", GPUQuota: 1}, {Name: "org-7", GPUQuota: 2},
			}},
			wantErr: deployments.ErrInvalidValue, wantMsg: "queues[1].name",
		},
		{
			name:    "leaf named like the root",
			values:  kaigpuqueues.Values{Queues: []kaigpuqueues.Queue{{Name: "ccluster-root", GPUQuota: 1}}},
			wantErr: deployments.ErrInvalidValue, wantMsg: "already used",
		},
		{
			name:    "quota below unlimited",
			values:  kaigpuqueues.Values{Queues: []kaigpuqueues.Queue{{Name: "org-7", GPUQuota: -2}}},
			wantErr: deployments.ErrInvalidValue, wantMsg: "gpuQuota -2",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := kaigpuqueues.New("kai-scheduler", test.values)
			if test.wantErr == nil {
				if err != nil {
					t.Fatalf("New() error = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, test.wantErr) || !strings.Contains(err.Error(), test.wantMsg) {
				t.Fatalf("New() error = %v, want one wrapping %v mentioning %q", err, test.wantErr, test.wantMsg)
			}
		})
	}
}
