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

package dynamodeployment_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/NVIDIA/ccluster/catalogs/deployments"
	dynamodeployment "github.com/NVIDIA/ccluster/catalogs/deployments/dynamo_deployment"
)

func ptr[T any](v T) *T { return &v }

func pool(gpus int32) dynamodeployment.WorkerPool {
	return dynamodeployment.WorkerPool{
		Resources: deployments.ResourceRequirements{
			Limits:   deployments.Resources{CPU: "1", Memory: "500Mi", AcceleratorCount: ptr(gpus)},
			Requests: deployments.Resources{CPU: "1", Memory: "250Mi", AcceleratorCount: ptr(gpus)},
		},
	}
}

func static(gpus int32) *dynamodeployment.StaticWorkerPool {
	return &dynamodeployment.StaticWorkerPool{WorkerPool: pool(gpus), TensorParallelSize: gpus}
}

func valid() dynamodeployment.Values {
	p := pool(2)
	return dynamodeployment.Values{
		Name:         "qwen",
		DeploymentID: "1732",
		ImageURL:     "nvcr.io/nvidia/ai-dynamo/vllm-runtime:1.4.0",
		Domain:       "example.com",
		Model:        "Qwen/Qwen3-0.6B",
		WorkerPool:   &p,
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*dynamodeployment.Values)
		wantErr error
		wantMsg string
	}{
		{name: "aggregated", mutate: func(*dynamodeployment.Values) {}},
		{
			name: "disaggregated",
			mutate: func(v *dynamodeployment.Values) {
				v.ServingMode, v.WorkerPool = ptr(dynamodeployment.Disaggregated), nil
				v.PrefillPool, v.DecodePool = static(1), static(2)
				v.BackendProtocol = ptr(deployments.GRPC)
			},
		},
		{
			name:    "blank model",
			mutate:  func(v *dynamodeployment.Values) { v.Model = "  " },
			wantErr: deployments.ErrMissingValue, wantMsg: "model",
		},
		{
			name:    "aggregated without a pool",
			mutate:  func(v *dynamodeployment.Values) { v.WorkerPool = nil },
			wantErr: deployments.ErrMissingValue, wantMsg: "workerPool",
		},
		{
			name:    "aggregated with a disaggregated pool",
			mutate:  func(v *dynamodeployment.Values) { v.DecodePool = static(1) },
			wantErr: deployments.ErrInvalidValue, wantMsg: "only for disaggregated",
		},
		{
			name: "disaggregated missing decode",
			mutate: func(v *dynamodeployment.Values) {
				v.ServingMode, v.WorkerPool, v.PrefillPool = ptr(dynamodeployment.Disaggregated), nil, static(1)
			},
			wantErr: deployments.ErrMissingValue, wantMsg: "decodePool",
		},
		{
			name: "tensor parallel size disagrees with accelerators",
			mutate: func(v *dynamodeployment.Values) {
				v.ServingMode, v.WorkerPool = ptr(dynamodeployment.Disaggregated), nil
				v.PrefillPool, v.DecodePool = static(1), static(2)
				v.DecodePool.TensorParallelSize = 4
			},
			wantErr: deployments.ErrInvalidValue, wantMsg: "decodePool.tensorParallelSize 4",
		},
		{
			name: "pool without accelerators",
			mutate: func(v *dynamodeployment.Values) {
				v.WorkerPool.Resources.Requests.AcceleratorCount = nil
			},
			wantErr: deployments.ErrInvalidValue, wantMsg: "workerPool.resources.requests.acceleratorCount",
		},
		{
			name:    "unknown serving mode",
			mutate:  func(v *dynamodeployment.Values) { v.ServingMode = ptr(dynamodeployment.ServingMode("hybrid")) },
			wantErr: deployments.ErrInvalidValue, wantMsg: "servingMode",
		},
		{
			name:    "scale to zero",
			mutate:  func(v *dynamodeployment.Values) { v.HPA = &deployments.HPA{MinReplicas: ptr(int32(0))} },
			wantErr: deployments.ErrInvalidValue, wantMsg: "does not scale to zero",
		},
		{
			name: "grpc on the metrics port",
			mutate: func(v *dynamodeployment.Values) {
				v.BackendProtocol = ptr(deployments.GRPC)
				v.ContainerPort = ptr(dynamodeployment.KServeGRPCMetricsPort)
			},
			wantErr: deployments.ErrInvalidValue, wantMsg: "KServe gRPC metrics port",
		},
		{
			name: "http on the metrics port is fine",
			mutate: func(v *dynamodeployment.Values) {
				v.ContainerPort = ptr(dynamodeployment.KServeGRPCMetricsPort)
			},
		},
		{
			name:    "privileged port",
			mutate:  func(v *dynamodeployment.Values) { v.ContainerPort = ptr(int32(80)) },
			wantErr: deployments.ErrInvalidValue, wantMsg: "containerPort 80",
		},
		{
			name: "user runner option",
			mutate: func(v *dynamodeployment.Values) {
				v.ExtraArgv = []string{"--max-model-len", "4096", "--runner=pooling"}
			},
			wantErr: deployments.ErrInvalidValue, wantMsg: "--runner generate",
		},
		{
			name:    "abbreviated runner option",
			mutate:  func(v *dynamodeployment.Values) { v.ExtraArgv = []string{"--run", "pooling"} },
			wantErr: deployments.ErrInvalidValue, wantMsg: "--run",
		},
		{
			// After "--" everything is positional, and vLLM never sees it as an option.
			name:   "runner after the separator",
			mutate: func(v *dynamodeployment.Values) { v.ExtraArgv = []string{"--", "--runner"} },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			v := valid()
			test.mutate(&v)
			_, err := dynamodeployment.New("user-7", v)
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
