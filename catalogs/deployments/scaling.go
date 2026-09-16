/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package deployments

import (
	"errors"
	"fmt"
)

// The autoscaling and rollout knobs shared by the inference and dynamo charts, which
// both scale a worker set with KEDA and roll it with a surge/unavailable pair.

const (
	// DefaultMinReplicas and DefaultMaxReplicas are the charts' HPA bounds: one
	// replica, autoscaling off.
	DefaultMinReplicas int32 = 1
	DefaultMaxReplicas int32 = 1

	// MaxAverageValue is 2^53, the largest integer a float64 holds exactly. KEDA
	// multiplies the threshold by 1000, so anything larger overflows; the charts ship
	// it as the default, which effectively disables the trigger.
	MaxAverageValue int64 = 1 << 53

	// DefaultMaxUnavailable and DefaultMaxSurge are the charts' rollout strategy:
	// replace one pod at a time without surging.
	DefaultMaxUnavailable int32 = 1
	DefaultMaxSurge       int32 = 0
)

// BackendProtocol is the protocol nginx speaks to the serving container. Fields of
// this type carry the enum and a title in their jsonschema tag: the schema reflector
// inlines named string types, and the title is what names the generated Python enum.
type BackendProtocol string

const (
	HTTP BackendProtocol = "HTTP"
	GRPC BackendProtocol = "GRPC"
)

// Validate reports a value that is neither HTTP nor GRPC. A nil receiver is valid.
func (p *BackendProtocol) Validate() error {
	if p != nil && *p != HTTP && *p != GRPC {
		return fmt.Errorf("%w: backendProtocol %q is not HTTP or GRPC", ErrInvalidValue, *p)
	}
	return nil
}

// HPA bounds the KEDA ScaledObject that scales the workers.
type HPA struct {
	// MinReplicas is the lower bound. The chart ships 1. For inference_deployment, 0
	// enables scale-to-zero, which needs the cluster's Prometheus to scrape nginx
	// ingress controller metrics; dynamo_deployment does not support 0.
	MinReplicas *int32 `json:"minReplicas,omitempty"`

	// MaxReplicas is the upper bound, at least MinReplicas. The chart ships 1.
	MaxReplicas *int32 `json:"maxReplicas,omitempty"`

	// AverageValue is the target number of in-flight requests per pod, from 1 to
	// 2^53. The chart ships 2^53, which effectively disables the trigger.
	AverageValue *int64 `json:"averageValue,omitempty"`

	// CooldownPeriod is how many seconds KEDA waits after the last active trigger
	// before scaling down. The chart ships 1800.
	CooldownPeriod *int64 `json:"cooldownPeriod,omitempty"`
}

// Bounds returns the effective replica bounds, with the chart defaults filled in.
func (h *HPA) Bounds() (minReplicas, maxReplicas int32) {
	minReplicas, maxReplicas = DefaultMinReplicas, DefaultMaxReplicas
	if h != nil && h.MinReplicas != nil {
		minReplicas = *h.MinReplicas
	}
	if h != nil && h.MaxReplicas != nil {
		maxReplicas = *h.MaxReplicas
	}
	return minReplicas, maxReplicas
}

// Validate reports bounds that are negative, inverted or outside what KEDA accepts.
// A nil receiver is valid: the chart's defaults are.
func (h *HPA) Validate() error {
	if h == nil {
		return nil
	}
	minReplicas, maxReplicas := h.Bounds()

	var errs []error
	if minReplicas < 0 {
		errs = append(errs, fmt.Errorf("%w: hpa.minReplicas %d is negative",
			ErrInvalidValue, minReplicas))
	}
	if maxReplicas < minReplicas {
		errs = append(errs, fmt.Errorf("%w: hpa.maxReplicas %d is below hpa.minReplicas %d",
			ErrInvalidValue, maxReplicas, minReplicas))
	}
	if h.AverageValue != nil && (*h.AverageValue < 1 || *h.AverageValue > MaxAverageValue) {
		errs = append(errs, fmt.Errorf("%w: hpa.averageValue %d is outside 1-%d",
			ErrInvalidValue, *h.AverageValue, MaxAverageValue))
	}
	if h.CooldownPeriod != nil && *h.CooldownPeriod < 0 {
		errs = append(errs, fmt.Errorf("%w: hpa.cooldownPeriod %d is negative",
			ErrInvalidValue, *h.CooldownPeriod))
	}
	return errors.Join(errs...)
}

// RolloutStrategy bounds a rolling update, as absolute pod counts.
type RolloutStrategy struct {
	// MaxUnavailable is the most pods that may be unavailable during an update. The
	// chart ships 1.
	MaxUnavailable *int32 `json:"maxUnavailable,omitempty"`

	// MaxSurge is the most pods that may be scheduled above the desired count during
	// an update. The chart ships 0.
	MaxSurge *int32 `json:"maxSurge,omitempty"`
}

// Validate reports a negative bound, or a pair that sums to zero and so could never
// make progress. A nil receiver is valid: the chart's defaults are.
func (s *RolloutStrategy) Validate() error {
	if s == nil {
		return nil
	}
	maxUnavailable, maxSurge := DefaultMaxUnavailable, DefaultMaxSurge
	if s.MaxUnavailable != nil {
		maxUnavailable = *s.MaxUnavailable
	}
	if s.MaxSurge != nil {
		maxSurge = *s.MaxSurge
	}
	switch {
	case maxUnavailable < 0 || maxSurge < 0:
		return fmt.Errorf("%w: rolloutStrategy bounds must not be negative", ErrInvalidValue)
	case maxUnavailable+maxSurge < 1:
		return fmt.Errorf("%w: rolloutStrategy.maxUnavailable %d and maxSurge %d must sum to at least 1",
			ErrInvalidValue, maxUnavailable, maxSurge)
	}
	return nil
}
