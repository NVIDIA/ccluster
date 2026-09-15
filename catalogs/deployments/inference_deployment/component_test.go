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

package inferencedeployment_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/NVIDIA/ccluster/catalogs/deployments"
	inferencedeployment "github.com/NVIDIA/ccluster/catalogs/deployments/inference_deployment"
)

func ptr[T any](v T) *T { return &v }

func valid() inferencedeployment.Values {
	return inferencedeployment.Values{
		Workload: deployments.Workload{
			Name:         "llama",
			DeploymentID: "1",
			ImageURL:     "registry.example.com/vllm:latest",
			Resources: deployments.ResourceRequirements{
				Limits:   deployments.Resources{CPU: "4", Memory: "16Gi", AcceleratorCount: ptr(int32(1))},
				Requests: deployments.Resources{CPU: "2", Memory: "8Gi", AcceleratorCount: ptr(int32(1))},
			},
		},
		Domain:        "example.com",
		ContainerPort: 8000,
	}
}

func TestNew(t *testing.T) {
	entry, err := inferencedeployment.New("user-7", valid())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if entry.Chart != inferencedeployment.Chart || entry.Namespace != "user-7" {
		t.Errorf("New() = %+v, want chart %q in user-7", entry, inferencedeployment.Chart)
	}

	// The Workload is embedded, so its keys are the chart's top-level keys, and the
	// values left unset stay out of the document for the chart's defaults to apply.
	var doc map[string]any
	if err := json.Unmarshal(entry.Values.Raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"name", "deploymentId", "imageUrl", "resources", "domain", "containerPort"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("values lack %q: %s", key, entry.Values.Raw)
		}
	}
	for _, key := range []string{"hpa", "schedulerName", "healthcheck", "rolloutStrategy", "workload"} {
		if _, ok := doc[key]; ok {
			t.Errorf("values carry %q, want it left to the chart: %s", key, entry.Values.Raw)
		}
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*inferencedeployment.Values)
		wantErr error
		wantMsg string
	}{
		{name: "required only", mutate: func(*inferencedeployment.Values) {}},
		{
			name: "everything optional set",
			mutate: func(v *inferencedeployment.Values) {
				v.HPA = &inferencedeployment.HPA{
					MinReplicas: ptr(int32(0)), MaxReplicas: ptr(int32(4)),
					AverageValue: ptr(int64(10)), CooldownPeriod: ptr(int64(60)),
				}
				v.RolloutStrategy = &inferencedeployment.RolloutStrategy{MaxUnavailable: ptr(int32(0)), MaxSurge: ptr(int32(1))}
				v.Metrics = &inferencedeployment.Metrics{Port: 9000, Path: "/metrics"}
				v.BackendProtocol = ptr(inferencedeployment.GRPC)
				v.ConfigFile = &deployments.ConfigFile{Filename: "c", MountPath: "/etc/c", Content: "x"}
				v.Healthcheck = ptr("")
			},
		},
		{
			name:    "missing domain",
			mutate:  func(v *inferencedeployment.Values) { v.Domain = "" },
			wantErr: deployments.ErrMissingValue, wantMsg: "domain",
		},
		{
			name:    "missing container port",
			mutate:  func(v *inferencedeployment.Values) { v.ContainerPort = 0 },
			wantErr: deployments.ErrMissingValue, wantMsg: "containerPort",
		},
		{
			name:    "container port on the sidecar",
			mutate:  func(v *inferencedeployment.Values) { v.ContainerPort = 15090 },
			wantErr: deployments.ErrInvalidValue, wantMsg: "reserved",
		},
		{
			name:    "workload errors surface",
			mutate:  func(v *inferencedeployment.Values) { v.Name = "" },
			wantErr: deployments.ErrMissingValue, wantMsg: "name",
		},
		{
			name:    "hpa inverted",
			mutate:  func(v *inferencedeployment.Values) { v.HPA = &inferencedeployment.HPA{MinReplicas: ptr(int32(3))} },
			wantErr: deployments.ErrInvalidValue, wantMsg: "hpa.maxReplicas 1 is below hpa.minReplicas 3",
		},
		{
			name:    "hpa negative min",
			mutate:  func(v *inferencedeployment.Values) { v.HPA = &inferencedeployment.HPA{MinReplicas: ptr(int32(-1))} },
			wantErr: deployments.ErrInvalidValue, wantMsg: "negative",
		},
		{
			name: "hpa average value past float64 exactness",
			mutate: func(v *inferencedeployment.Values) {
				v.HPA = &inferencedeployment.HPA{AverageValue: ptr(inferencedeployment.MaxAverageValue + 1)}
			},
			wantErr: deployments.ErrInvalidValue, wantMsg: "averageValue",
		},
		{
			// maxSurge defaults to 0, so zeroing maxUnavailable alone stalls the rollout.
			name: "rollout strategy that cannot progress",
			mutate: func(v *inferencedeployment.Values) {
				v.RolloutStrategy = &inferencedeployment.RolloutStrategy{MaxUnavailable: ptr(int32(0))}
			},
			wantErr: deployments.ErrInvalidValue, wantMsg: "sum to at least 1",
		},
		{
			name: "metrics on a reserved port with a relative path",
			mutate: func(v *inferencedeployment.Values) {
				v.Metrics = &inferencedeployment.Metrics{Port: 15020, Path: "metrics"}
			},
			wantErr: deployments.ErrInvalidValue, wantMsg: "metrics.path",
		},
		{
			name: "unknown backend protocol",
			mutate: func(v *inferencedeployment.Values) {
				v.BackendProtocol = ptr(inferencedeployment.BackendProtocol("TCP"))
			},
			wantErr: deployments.ErrInvalidValue, wantMsg: "backendProtocol",
		},
		{
			name: "bad config file",
			mutate: func(v *inferencedeployment.Values) {
				v.ConfigFile = &deployments.ConfigFile{Filename: "c", MountPath: "/proc/c", Content: "x"}
			},
			wantErr: deployments.ErrInvalidValue, wantMsg: "/proc",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			v := valid()
			test.mutate(&v)
			_, err := inferencedeployment.New("user-7", v)
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
