/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package kaigpuqueues describes the values of catalogs/resources/kai_gpu_queues: the
// KAI scheduler's GPU quota tree. The chart renders one unlimited root Queue and one
// leaf Queue per tenant, each capping the GPUs its pods may hold. Workload charts join a
// leaf through their kaiQueue value. Queues are cluster-scoped, so the chart is
// installed once per infra plane; the namespace passed to New is where Argo CD records
// the release and nothing else.
package kaigpuqueues

import (
	"errors"
	"fmt"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
	"github.com/NVIDIA/ccluster/catalogs/deployments"
)

// Chart is the chart directory, relative to the repository root.
const Chart = "catalogs/resources/kai_gpu_queues"

// Unlimited is the quota that means "no cap". The chart ships it as unlimitedQuota.
const Unlimited int64 = -1

// Values is the top-level values document for catalogs/resources/kai_gpu_queues.
type Values struct {
	// RootQueue names the unlimited parent of every leaf. The chart ships
	// ccluster-root.
	RootQueue *string `json:"rootQueue,omitempty"`

	// Queues are the leaf queues. Remove a tenant's GPUs by setting its quota to 0
	// rather than dropping the entry, so the Queue outlives the pods still in it.
	Queues []Queue `json:"queues,omitempty"`
}

// Queue is one leaf of the quota tree.
type Queue struct {
	// Name is the Queue's name, which pods reference as kai.scheduler/queue. Required.
	Name string `json:"name"`

	// GPUQuota is both the queue's GPU quota and its limit, a hard cap. 0 grants none
	// and Unlimited (-1) lifts the cap. Required.
	GPUQuota int64 `json:"gpuQuota"`
}

// New returns the kai_gpu_queues chart entry.
func New(
	namespace string,
	values Values,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	return catalogs.Entry(Chart, namespace, values)
}

// Validate implements catalogs.Validator: every leaf is named, once, with a quota that
// is non-negative or Unlimited, and no leaf reuses the root's name.
func (v Values) Validate() error {
	root := "ccluster-root"
	if v.RootQueue != nil {
		root = *v.RootQueue
	}
	var errs []error
	if root == "" {
		errs = append(errs, deployments.MissingError([]string{"rootQueue"}))
	}
	seen := map[string]bool{root: true}
	for i, q := range v.Queues {
		prefix := fmt.Sprintf("queues[%d]", i)
		switch {
		case q.Name == "":
			errs = append(errs, deployments.MissingError([]string{prefix + ".name"}))
		case seen[q.Name]:
			errs = append(errs, fmt.Errorf("%w: %s.name %q is already used",
				deployments.ErrInvalidValue, prefix, q.Name))
		default:
			seen[q.Name] = true
		}
		if q.GPUQuota < Unlimited {
			errs = append(errs, fmt.Errorf("%w: %s.gpuQuota %d is neither non-negative nor %d",
				deployments.ErrInvalidValue, prefix, q.GPUQuota, Unlimited))
		}
	}
	return errors.Join(errs...)
}
