/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package dynamodeployment

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
)

// ReasonGraphUnreadable: the DynamoGraphDeployment cannot be read, or is gone.
const ReasonGraphUnreadable = "GraphUnreadable"

// Values of a DynamoGraphDeployment's status.state, as the Dynamo operator writes them.
const (
	graphStateSuccessful = "successful"
	graphStateFailed     = "failed"
)

// Health judges a dynamo deployment by DefaultHealth and by its DynamoGraphDeployment's
// status.state and Ready condition, because Argo CD has no health check for the graph.
// The worse health wins. When the graph cannot be read, the health stays DefaultHealth's
// and only the reason is added.
func Health(ctx context.Context, entry v1alpha1.ComponentEntry, observed catalogs.Observation) catalogs.Health {
	health := catalogs.DefaultHealth(ctx, entry, observed)
	i := slices.IndexFunc(observed.Resources, func(r catalogs.ObservedResource) bool {
		return r.Group == "nvidia.com" && r.Kind == "DynamoGraphDeployment" && r.Rendered()
	})
	if i < 0 {
		return health
	}
	graph := observed.Resources[i]

	graphHealth, err := judgeGraph(ctx, observed, graph)
	if err != nil {
		if health.Reason == "" {
			health.Reason = ReasonGraphUnreadable
			health.Message = fmt.Sprintf("DynamoGraphDeployment %s could not be read: %v", graph.Name, err)
		}
		return health
	}
	// On a tie the graph explains, unless a Pod already does.
	if catalogs.Worse(graphHealth.Status, health.Status) ||
		(graphHealth.Status == health.Status && health.Reason == "") {
		return graphHealth
	}
	return health
}

// judgeGraph reads a DynamoGraphDeployment and judges it by its status. It returns an
// error when it cannot read the graph, or the graph is gone.
func judgeGraph(
	ctx context.Context, observed catalogs.Observation, graph catalogs.ObservedResource,
) (catalogs.Health, error) {
	obj, err := observed.Reader.Get(ctx, graph.ResourceRef)
	if err != nil {
		return catalogs.Health{}, err
	}
	if obj == nil {
		return catalogs.Health{}, errors.New("it no longer exists")
	}

	var status struct {
		ObservedGeneration int64              `json:"observedGeneration"`
		State              string             `json:"state"`
		Conditions         []metav1.Condition `json:"conditions"`
	}
	raw, _, _ := unstructured.NestedMap(obj.Object, "status")
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &status); err != nil {
		return catalogs.Health{}, err
	}

	// A status from before the latest spec change can say successful for the old spec.
	// Wait until the operator has seen the current spec. An operator that does not set
	// observedGeneration leaves it at 0, and then the check is skipped.
	if status.ObservedGeneration > 0 && status.ObservedGeneration < obj.GetGeneration() {
		return catalogs.Health{
			Status: v1alpha1.ComponentHealthProgressing,
			Message: fmt.Sprintf("DynamoGraphDeployment %s: waiting for the Dynamo operator to see "+
				"the current spec", graph.Name),
		}, nil
	}

	// The Ready condition explains the state. Its reason is in snake case.
	reason, message := "", "state "+status.State
	if ready := apimeta.FindStatusCondition(status.Conditions, "Ready"); ready != nil {
		reason = camelCase(ready.Reason)
		if ready.Message != "" {
			message = ready.Message
		}
	}
	message = fmt.Sprintf("DynamoGraphDeployment %s: %s", graph.Name, message)

	switch status.State {
	case graphStateSuccessful:
		return catalogs.Health{Status: v1alpha1.ComponentHealthHealthy, Message: message}, nil
	case graphStateFailed:
		return catalogs.Health{Status: v1alpha1.ComponentHealthDegraded, Reason: reason, Message: message}, nil
	default:
		// initializing, pending, or a state that a later operator adds.
		return catalogs.Health{Status: v1alpha1.ComponentHealthProgressing, Reason: reason, Message: message}, nil
	}
}

// camelCase turns insufficient_capacity into InsufficientCapacity.
func camelCase(snake string) string {
	var b strings.Builder
	for word := range strings.SplitSeq(snake, "_") {
		if word != "" {
			b.WriteString(strings.ToUpper(word[:1]) + word[1:])
		}
	}
	return b.String()
}
