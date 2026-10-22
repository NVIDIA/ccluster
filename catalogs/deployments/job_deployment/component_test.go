/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package jobdeployment

import (
	"slices"
	"testing"

	"github.com/NVIDIA/ccluster/catalogs/deployments/deploymentstest"
)

// TestChartEnvMatchesTemplates keeps chartEnv in step with the templates, so
// secretEnvVars cannot repeat a variable the chart sets. JOB_COMPLETION_INDEX is added:
// the Job controller sets it, not the templates.
func TestChartEnvMatchesTemplates(t *testing.T) {
	got := slices.Sorted(slices.Values(chartEnv))
	want := slices.Sorted(slices.Values(append(deploymentstest.TemplateEnv(t, "."), "JOB_COMPLETION_INDEX")))
	if !slices.Equal(got, want) {
		t.Fatalf("chartEnv = %v, templates set %v", got, want)
	}
}
