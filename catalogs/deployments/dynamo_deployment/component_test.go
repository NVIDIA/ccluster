/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package dynamodeployment

import (
	"slices"
	"strings"
	"testing"

	"github.com/NVIDIA/ccluster/catalogs/deployments"
	"github.com/NVIDIA/ccluster/catalogs/deployments/deploymentstest"
)

func TestValidateHFTokenFromOneSource(t *testing.T) {
	token := "hf_plain"
	empty := ""
	fromSecret := map[string]deployments.SecretKeyRef{
		"HF_TOKEN": {SecretName: "app-env", Key: "hf-token"},
	}
	for _, tt := range []struct {
		name          string
		hfToken       *string
		secretEnvVars map[string]deployments.SecretKeyRef
		wantErr       bool
	}{
		{name: "hfToken", hfToken: &token},
		{name: "secretEnvVars", secretEnvVars: fromSecret},
		{name: "empty hfToken renders none", hfToken: &empty, secretEnvVars: fromSecret},
		{name: "both", hfToken: &token, secretEnvVars: fromSecret, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := Values{HFToken: tt.hfToken, SecretEnvVars: tt.secretEnvVars}.Validate()
			got := err != nil && strings.Contains(err.Error(), "secretEnvVars.HF_TOKEN")
			if got != tt.wantErr {
				t.Fatalf("Validate() reports secretEnvVars.HF_TOKEN = %t, want %t: %v", got, tt.wantErr, err)
			}
		})
	}
}

// TestValidateSecretEnvVarsChartEnv: DYN_ROUTER_MODE is the caller's router knob and may
// come from a Secret; a variable the chart owns may not.
func TestValidateSecretEnvVarsChartEnv(t *testing.T) {
	for _, tt := range []struct {
		name    string
		wantErr bool
	}{
		{name: "DYN_ROUTER_MODE"},
		{name: "DYN_EVENT_PLANE", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := Values{SecretEnvVars: map[string]deployments.SecretKeyRef{
				tt.name: {SecretName: "app-env", Key: "value"},
			}}.Validate()
			got := err != nil && strings.Contains(err.Error(), "secretEnvVars."+tt.name+" is set by the chart")
			if got != tt.wantErr {
				t.Fatalf("Validate() reports secretEnvVars.%s = %t, want %t: %v", tt.name, got, tt.wantErr, err)
			}
		})
	}
}

// TestChartEnvMatchesTemplates keeps chartEnv in step with the templates, so
// secretEnvVars cannot repeat a variable the chart sets. DYN_ROUTER_MODE is left out:
// the Frontend renders it once, from secretEnvVars or envVars when either sets it.
func TestChartEnvMatchesTemplates(t *testing.T) {
	got := slices.Sorted(slices.Values(chartEnv))
	want := slices.DeleteFunc(deploymentstest.TemplateEnv(t, "."), func(name string) bool {
		return name == "DYN_ROUTER_MODE"
	})
	if !slices.Equal(got, want) {
		t.Fatalf("chartEnv = %v, templates set %v", got, want)
	}
}
