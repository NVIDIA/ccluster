/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

import (
	"context"
	"fmt"
	"sync"
	"time"

	logf "sigs.k8s.io/controller-runtime/pkg/log"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
	"github.com/NVIDIA/ccluster/internal/argocd/apiclient"
	argocdv1alpha1 "github.com/NVIDIA/ccluster/internal/argocd/v1alpha1"
	healthregistry "github.com/NVIDIA/ccluster/internal/registry/health_registry"
)

const (
	// readTimeout limits how long one component's reads take: the tree, and the reads
	// of its chart's health function. The rollout waits for them, so it is short.
	readTimeout = 10 * time.Second

	// maxHealthMessage limits the length of a health message.
	maxHealthMessage = 1024
)

// judgeAll judges the component that each of applications deploys, all at the same time,
// so a pass waits for at most one readTimeout. applications is keyed by Application name.
// Both controllers use it: ComponentHealthReconciler reports the result, and the rollout
// judges again before it releases the next component.
func judgeAll(
	ctx context.Context,
	argoCD ArgoCDServer,
	healthFuncs *healthregistry.Registry,
	componentSet *cclusternvidiacomv1alpha1.CCLComponentSet,
	applications map[string]*argocdv1alpha1.Application,
) componentHealths {
	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		healths = componentHealths{}
	)
	for _, component := range componentSet.Spec.Components {
		application := applications[applicationName(componentSet.Name, component.Name)]
		if application == nil {
			continue
		}
		wg.Go(func() {
			health := judge(ctx, argoCD, healthFuncs, application, component.ComponentEntry)
			mu.Lock()
			defer mu.Unlock()
			healths[component.Name] = health
		})
	}
	wg.Wait()
	return healths
}

// judge returns the health of the component that application deploys, as its chart's
// health function judges it.
//
// It never fails. When it cannot read the tree, or the chart's function panics or
// returns a bad value, it returns Argo CD's health of the Application, with a reason
// that says why. Without argoCD it returns that health with no reason.
func judge(
	ctx context.Context,
	argoCD ArgoCDServer,
	healthFuncs *healthregistry.Registry,
	application *argocdv1alpha1.Application,
	entry cclusternvidiacomv1alpha1.ComponentEntry,
) catalogs.Health {
	aggregate := application.HealthStatus()
	if argoCD == nil {
		return catalogs.Health{Status: catalogs.FromArgoCD(aggregate)}
	}

	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	app := apiclient.ApplicationRef{
		Namespace: application.Namespace,
		Name:      application.Name,
		Project:   application.Spec.Project,
	}
	tree, err := argoCD.ResourceTree(ctx, app)
	if err != nil {
		logf.FromContext(ctx).Info("Did not read resource tree of Application",
			"application", application.Name, "error", err.Error())
		return aggregateHealth(aggregate, cclusternvidiacomv1alpha1.ComponentHealthReasonResourceTreeUnavailable,
			fmt.Sprintf("Argo CD's aggregate health: the resource tree could not be read: %v", err))
	}

	healthFunc := healthregistry.HealthFunc(catalogs.DefaultHealth)
	if healthFuncs != nil {
		if f, ok := healthFuncs.Lookup(entry.Chart); ok {
			healthFunc = f
		}
	}
	return callHealthFunc(ctx, healthFunc, entry, catalogs.Observation{
		ApplicationHealth: aggregate,
		Resources:         observedResources(tree),
		Reader:            applicationReader{argoCD: argoCD, app: app},
	})
}

// callHealthFunc calls a chart's health function. If the function panics, or returns a
// health the CRD does not allow, callHealthFunc returns Argo CD's health of the
// Application instead.
func callHealthFunc(
	ctx context.Context,
	healthFunc healthregistry.HealthFunc,
	entry cclusternvidiacomv1alpha1.ComponentEntry,
	observed catalogs.Observation,
) (health catalogs.Health) {
	defer func() {
		if p := recover(); p != nil {
			health = aggregateHealth(observed.ApplicationHealth, cclusternvidiacomv1alpha1.ComponentHealthReasonHealthFuncPanicked,
				fmt.Sprintf("Argo CD's aggregate health: the chart's health function panicked: %v", p))
		}
	}()
	health = healthFunc(ctx, entry, observed)
	if !catalogs.ValidHealth(health.Status) {
		return aggregateHealth(observed.ApplicationHealth, cclusternvidiacomv1alpha1.ComponentHealthReasonInvalidHealth,
			fmt.Sprintf("Argo CD's aggregate health: the chart's health function reported %q", health.Status))
	}
	health.Message = truncate(health.Message)
	return health
}

// aggregateHealth returns Argo CD's health of the Application, with a reason and message.
func aggregateHealth(aggregate, reason, message string) catalogs.Health {
	return catalogs.Health{Status: catalogs.FromArgoCD(aggregate), Reason: reason, Message: truncate(message)}
}

// healthy reports whether a component is healthy enough for the next one to start:
// Healthy, or Succeeded for a component that runs to completion, such as a Job.
func healthy(health cclusternvidiacomv1alpha1.ComponentHealth) bool {
	return health == cclusternvidiacomv1alpha1.ComponentHealthHealthy ||
		health == cclusternvidiacomv1alpha1.ComponentHealthSucceeded
}

// settled reports whether a health is not expected to change soon: healthy, or Failed.
func settled(health cclusternvidiacomv1alpha1.ComponentHealth) bool {
	return healthy(health) || health == cclusternvidiacomv1alpha1.ComponentHealthFailed
}

// truncate cuts a message to maxHealthMessage bytes.
func truncate(message string) string {
	if len(message) <= maxHealthMessage {
		return message
	}
	return message[:maxHealthMessage-3] + "..."
}

// componentHealths holds the healths judged in one pass, by component name.
type componentHealths map[string]catalogs.Health

// of returns the health judged in this pass. For a component not judged, it returns Argo
// CD's health of the Application.
func (h componentHealths) of(component string, application *argocdv1alpha1.Application) catalogs.Health {
	if health, ok := h[component]; ok {
		return health
	}
	return catalogs.Health{Status: catalogs.FromArgoCD(application.HealthStatus())}
}
