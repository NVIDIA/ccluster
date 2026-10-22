/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package inferencedeployment

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
			v := Values{HFToken: tt.hfToken}
			v.SecretEnvVars = tt.secretEnvVars
			err := v.Validate()
			got := err != nil && strings.Contains(err.Error(), "secretEnvVars.HF_TOKEN")
			if got != tt.wantErr {
				t.Fatalf("Validate() reports secretEnvVars.HF_TOKEN = %t, want %t: %v", got, tt.wantErr, err)
			}
		})
	}
}

// TestChartEnvMatchesTemplates keeps chartEnv in step with the templates, so
// secretEnvVars cannot repeat a variable the chart sets. HF_TOKEN is left out: the
// template sets it only from hfToken, which Validate checks on its own.
func TestChartEnvMatchesTemplates(t *testing.T) {
	got := slices.Sorted(slices.Values(chartEnv))
	want := slices.DeleteFunc(deploymentstest.TemplateEnv(t, "."), func(name string) bool {
		return name == "HF_TOKEN"
	})
	if !slices.Equal(got, want) {
		t.Fatalf("chartEnv = %v, templates set %v", got, want)
	}
}
