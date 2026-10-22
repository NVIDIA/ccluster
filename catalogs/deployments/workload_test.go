/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package deployments

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateSecretReferences(t *testing.T) {
	ref := SecretKeyRef{SecretName: "app-env", Key: "api-key"}
	for _, tt := range []struct {
		name    string
		mutate  func(*Workload)
		wantErr error
		// wantKey is the value key the error must name.
		wantKey string
	}{
		{name: "unset", mutate: func(*Workload) {}},
		{
			name: "references",
			mutate: func(w *Workload) {
				w.ImagePullSecretNames = []string{"registry-pull", "registry-pull"}
				w.EnvVars = map[string]string{"FOO": "bar"}
				w.SecretEnvVars = map[string]SecretKeyRef{"API_KEY": ref}
			},
		},
		{
			name:    "empty pull secret name",
			mutate:  func(w *Workload) { w.ImagePullSecretNames = []string{"registry-pull", ""} },
			wantErr: ErrMissingValue,
			wantKey: "imagePullSecretNames[1]",
		},
		{
			name: "missing secret name",
			mutate: func(w *Workload) {
				w.SecretEnvVars = map[string]SecretKeyRef{"API_KEY": {Key: "api-key"}}
			},
			wantErr: ErrMissingValue,
			wantKey: "secretEnvVars.API_KEY.secretName",
		},
		{
			name: "missing key",
			mutate: func(w *Workload) {
				w.SecretEnvVars = map[string]SecretKeyRef{"API_KEY": {SecretName: "app-env"}}
			},
			wantErr: ErrMissingValue,
			wantKey: "secretEnvVars.API_KEY.key",
		},
		{
			name: "variable also in envVars",
			mutate: func(w *Workload) {
				w.EnvVars = map[string]string{"API_KEY": "plain"}
				w.SecretEnvVars = map[string]SecretKeyRef{"API_KEY": ref}
			},
			wantErr: ErrInvalidValue,
			wantKey: "secretEnvVars.API_KEY",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := Workload{
				Name:         "app",
				DeploymentID: "dep-1",
				ImageURL:     "registry.example.com/app:1",
				Resources: ResourceRequirements{
					Limits:   Resources{CPU: "1", Memory: "1Gi"},
					Requests: Resources{CPU: "1", Memory: "1Gi"},
				},
			}
			tt.mutate(&w)
			err := w.Validate()
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) || !strings.Contains(err.Error(), tt.wantKey) {
				t.Fatalf("Validate() = %v, want %v naming %s", err, tt.wantErr, tt.wantKey)
			}
		})
	}
}

func TestValidateChartEnv(t *testing.T) {
	chartEnv := []string{"CHART_VAR"}
	ref := SecretKeyRef{SecretName: "app-env", Key: "api-key"}
	if err := ValidateChartEnv(map[string]SecretKeyRef{"API_KEY": ref}, chartEnv); err != nil {
		t.Fatalf("ValidateChartEnv() = %v, want nil", err)
	}
	err := ValidateChartEnv(map[string]SecretKeyRef{"API_KEY": ref, "CHART_VAR": ref}, chartEnv)
	if !errors.Is(err, ErrInvalidValue) || !strings.Contains(err.Error(), "secretEnvVars.CHART_VAR") ||
		strings.Contains(err.Error(), "API_KEY") {
		t.Fatalf("ValidateChartEnv() = %v, want %v naming only secretEnvVars.CHART_VAR", err, ErrInvalidValue)
	}
}
