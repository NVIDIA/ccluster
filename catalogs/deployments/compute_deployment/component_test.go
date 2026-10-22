/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package computedeployment

import (
	"os"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/NVIDIA/ccluster/catalogs/deployments/deploymentstest"
)

// Catalogs are read from git at a revision, so the default launcher tag cannot be stamped
// at release time like the operator images. The release PR writes it next to VERSION
// instead, and this test fails the PR when the two disagree.
func TestDefaultLauncherMatchesVersion(t *testing.T) {
	version, err := os.ReadFile("../../../VERSION")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var values struct {
		SSHInjectorImage string `json:"sshInjectorImage"`
	}
	if err := yaml.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}
	want := "ghcr.io/nvidia/ccluster-compute-ssh:" + strings.TrimSpace(string(version))
	if values.SSHInjectorImage != want {
		t.Fatalf("values.yaml sshInjectorImage is %q, want %q (the tag must equal VERSION)",
			values.SSHInjectorImage, want)
	}
}

func TestValidateSSHInjectorImage(t *testing.T) {
	empty := ""
	image := "registry.example.com/compute-ssh:1"
	for _, tt := range []struct {
		name    string
		image   *string
		wantErr bool
	}{
		{name: "unset uses the chart default", image: nil},
		{name: "override", image: &image},
		{name: "empty", image: &empty, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := Values{SSHInjectorImage: tt.image}.Validate()
			got := err != nil && strings.Contains(err.Error(), "sshInjectorImage")
			if got != tt.wantErr {
				t.Fatalf("Validate() reports sshInjectorImage = %t, want %t: %v", got, tt.wantErr, err)
			}
		})
	}
}

// TestChartEnvMatchesTemplates keeps chartEnv in step with the templates, so
// secretEnvVars cannot repeat a variable the chart sets.
func TestChartEnvMatchesTemplates(t *testing.T) {
	got := slices.Sorted(slices.Values(chartEnv))
	if want := deploymentstest.TemplateEnv(t, "."); !slices.Equal(got, want) {
		t.Fatalf("chartEnv = %v, templates set %v", got, want)
	}
}
