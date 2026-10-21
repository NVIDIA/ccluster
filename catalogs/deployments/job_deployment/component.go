/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package jobdeployment describes the values of catalogs/deployments/job_deployment: an
// indexed batch Job running a user image to completion. The finished Job stays until the
// CCLComponentSet is deleted; its pods stay unless pod garbage collection deletes them first.
package jobdeployment

import (
	"errors"
	"fmt"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
	"github.com/NVIDIA/ccluster/catalogs/deployments"
)

// Chart is the chart directory, relative to the repository root.
const Chart = "catalogs/deployments/job_deployment"

// Values is the top-level values document for catalogs/deployments/job_deployment.
type Values struct {
	deployments.Workload

	// Command overrides the image's entrypoint.
	Command []string `json:"command,omitempty"`

	// Args overrides the image's arguments.
	Args []string `json:"args,omitempty"`

	// Completions is the number of successful pods the Job needs; the Job is indexed,
	// so each pod gets a distinct JOB_COMPLETION_INDEX. The chart ships 1.
	Completions *int32 `json:"completions,omitempty"`

	// Parallelism is the most pods running at once. The chart ships 1.
	Parallelism *int32 `json:"parallelism,omitempty"`

	// BackoffLimit is the number of retries before the Job is failed. The chart
	// ships 3.
	BackoffLimit *int32 `json:"backoffLimit,omitempty"`

	// ActiveDeadlineSeconds fails the Job once it has run this long, retries
	// included. Unset, it runs until it completes or exhausts BackoffLimit.
	ActiveDeadlineSeconds *int64 `json:"activeDeadlineSeconds,omitempty"`

	// ConfigFile is a file mounted into the container from a ConfigMap.
	ConfigFile *deployments.ConfigFile `json:"configFile,omitempty"`

	// VolumeMounts are existing claims mounted into the container.
	VolumeMounts []deployments.VolumeMount `json:"volumeMounts,omitempty"`
}

// New returns the job_deployment chart entry, installed into namespace with values.
func New(
	namespace string,
	values Values,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	return catalogs.Entry(Chart, namespace, values)
}

// chartEnv are the variables templates/job.yaml sets in the container, and
// JOB_COMPLETION_INDEX, which the Job controller sets in each pod of the indexed Job.
var chartEnv = []string{"JOB_COMPLETIONS", "JOB_COMPLETION_INDEX"}

// Validate implements catalogs.Validator: the required values that are unset, and the
// counts that are malformed.
func (v Values) Validate() error {
	errs := []error{
		v.Workload.Validate(),
		deployments.ValidateChartEnv(v.SecretEnvVars, chartEnv),
		v.ConfigFile.Validate(),
		deployments.ValidateVolumeMounts(v.VolumeMounts),
	}
	for _, count := range []struct {
		name  string
		value *int32
		min   int32
	}{
		{"completions", v.Completions, 1},
		{"parallelism", v.Parallelism, 0},
		{"backoffLimit", v.BackoffLimit, 0},
	} {
		if count.value != nil && *count.value < count.min {
			errs = append(errs, fmt.Errorf("%w: %s %d is below %d",
				deployments.ErrInvalidValue, count.name, *count.value, count.min))
		}
	}
	if v.ActiveDeadlineSeconds != nil && *v.ActiveDeadlineSeconds < 1 {
		errs = append(errs, fmt.Errorf("%w: activeDeadlineSeconds %d is below 1",
			deployments.ErrInvalidValue, *v.ActiveDeadlineSeconds))
	}
	return errors.Join(errs...)
}
