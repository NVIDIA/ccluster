/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"k8s.io/apimachinery/pkg/runtime/schema"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
	argocdv1alpha1 "github.com/NVIDIA/ccluster/internal/api/argocd/v1alpha1"
	"github.com/NVIDIA/ccluster/internal/argocd/apiclient"
)

// A prunable object is one a component's chart no longer renders but Argo CD still keeps:
// status.resources marks it requiresPruning, and Prune=false stops Argo CD pruning it at
// the start of the sync. The operator prunes it once the component has finished. See
// DESIGN.md, "Pruning after rollout".

// prunableKinds are the kinds of prunable object the operator prunes, and the only kinds
// the cp chart's policy lets its Argo CD account delete. One of another kind stays: a
// chart that marks one Prune=false means to keep it.
var prunableKinds = []schema.GroupKind{
	{Group: "external-secrets.io", Kind: "ExternalSecret"},
	{Group: "", Kind: "ConfigMap"},
	{Group: "resource.k8s.io", Kind: "ResourceClaimTemplate"},
}

// objectKey identifies an object across status.resources and the resource tree.
type objectKey struct {
	Group, Kind, Namespace, Name string
}

func statusKey(r argocdv1alpha1.ResourceStatus) objectKey {
	return objectKey{Group: r.Group, Kind: r.Kind, Namespace: r.Namespace, Name: r.Name}
}

func treeKey(r catalogs.ResourceRef) objectKey {
	return objectKey{Group: r.Group, Kind: r.Kind, Namespace: r.Namespace, Name: r.Name}
}

// withoutPrunableObjects leaves application's prunable objects, and what they own, out of
// observed. Argo CD's aggregate health still counts them, so where it is worse than every
// rendered object left, it becomes the worst of those. See DESIGN.md, "Pruning after
// rollout".
func withoutPrunableObjects(
	observed catalogs.Observation, application *argocdv1alpha1.Application,
) catalogs.Observation {
	prunable := map[objectKey]bool{}
	for _, r := range application.PrunableObjects() {
		prunable[statusKey(r)] = true
	}
	if len(prunable) == 0 {
		return observed
	}

	leftOut := map[catalogs.ResourceRef]bool{}
	var leaveOut func(ref catalogs.ResourceRef)
	leaveOut = func(ref catalogs.ResourceRef) {
		if leftOut[ref] {
			return
		}
		leftOut[ref] = true
		for _, child := range observed.Resources.ChildrenOf(ref) {
			leaveOut(child.ResourceRef)
		}
	}
	for _, r := range observed.Resources {
		if r.Rendered() && prunable[treeKey(r.ResourceRef)] {
			leaveOut(r.ResourceRef)
		}
	}

	var remaining catalogs.ObservedResources
	worst := cclusternvidiacomv1alpha1.ComponentHealthHealthy
	for _, r := range observed.Resources {
		if leftOut[r.ResourceRef] {
			continue
		}
		remaining = append(remaining, r)
		if health := catalogs.FromArgoCD(r.Health); r.Rendered() && r.Health != "" && catalogs.Worse(health, worst) {
			worst = health
		}
	}
	observed.Resources = remaining
	if observed.ApplicationHealth != "" && catalogs.Worse(catalogs.FromArgoCD(observed.ApplicationHealth), worst) {
		observed.ApplicationHealth = string(worst)
	}
	return observed
}

// pruneFinished prunes the objects of prunableKinds of finished, the Applications of the
// components that have finished rolling out, by deleting them through argocd-server. One
// already being deleted is deleted again, which changes nothing. A delete argocd-server
// refuses is only logged. All its requests share one readTimeout. See DESIGN.md, "Pruning
// after rollout".
func (r *CCLComponentSetReconciler) pruneFinished(
	ctx context.Context, finished []*argocdv1alpha1.Application,
) error {
	if r.ArgoCD == nil {
		return nil
	}
	log := logf.FromContext(ctx)
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()

	var errs []error
	for _, application := range finished {
		for _, obj := range application.PrunableObjects() {
			if !slices.Contains(prunableKinds, schema.GroupKind{Group: obj.Group, Kind: obj.Kind}) {
				continue
			}
			err := r.ArgoCD.DeleteResource(ctx, applicationRef(application), apiclient.ResourceRef{
				Group: obj.Group, Version: obj.Version, Kind: obj.Kind, Namespace: obj.Namespace, Name: obj.Name,
			})
			if errors.Is(err, apiclient.ErrResourceGone) {
				continue
			}
			if errors.Is(err, apiclient.ErrUnauthorized) {
				// A refusal lasts until the account's policy changes, so returning it would
				// only back off every pass of the set. The next pass tries again.
				log.Info("Did not prune Argo CD Application object", "application", application.Name,
					"namespace", application.Namespace, "kind", obj.Kind, "object", obj.Namespace+"/"+obj.Name,
					"error", err.Error())
				continue
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("pruning %s %s/%s of Application %s: %w",
					obj.Kind, obj.Namespace, obj.Name, application.Name, err))
				continue
			}
			log.Info("Requested pruning of Argo CD Application object", "application", application.Name,
				"namespace", application.Namespace, "kind", obj.Kind, "object", obj.Namespace+"/"+obj.Name)
		}
	}
	return errors.Join(errs...)
}
