/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

import (
	"context"
	"errors"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/NVIDIA/ccluster/catalogs"
	"github.com/NVIDIA/ccluster/internal/argocd/apiclient"
)

// ArgoCDServer is the part of argocd-server's API that the controllers read to judge
// component health. *apiclient.Client implements it. It is an interface so that tests
// can use a fake.
type ArgoCDServer interface {
	ResourceTree(ctx context.Context, app apiclient.ApplicationRef) (*apiclient.ResourceTree, error)
	Resource(
		ctx context.Context, app apiclient.ApplicationRef, ref apiclient.ResourceRef,
	) (*unstructured.Unstructured, error)
	ResourceEvents(ctx context.Context, app apiclient.ApplicationRef, ref apiclient.ResourceRef) ([]corev1.Event, error)
}

// applicationReader is the catalogs.ResourceReader for one Application. argocd-server
// reads every object through an Application, so the reader keeps the Application.
// Each read is a request to the infra-plane cluster.
type applicationReader struct {
	argoCD ArgoCDServer
	app    apiclient.ApplicationRef
}

// Get returns the live object. It returns nil, nil if the object is gone, such as a Pod
// that was replaced after Argo CD cached the tree.
func (r applicationReader) Get(ctx context.Context, ref catalogs.ResourceRef) (*unstructured.Unstructured, error) {
	obj, err := r.argoCD.Resource(ctx, r.app, apiRef(ref))
	if errors.Is(err, apiclient.ErrResourceGone) {
		return nil, nil
	}
	return obj, err
}

// Events returns the object's events. It returns none if the object is gone.
func (r applicationReader) Events(ctx context.Context, ref catalogs.ResourceRef) ([]corev1.Event, error) {
	events, err := r.argoCD.ResourceEvents(ctx, r.app, apiRef(ref))
	if errors.Is(err, apiclient.ErrResourceGone) {
		return nil, nil
	}
	return events, err
}

// observedResources converts the tree to the catalogs type, so that catalogs does not
// import Argo CD's types.
func observedResources(tree *apiclient.ResourceTree) catalogs.ObservedResources {
	resources := make(catalogs.ObservedResources, 0, len(tree.Nodes))
	for _, node := range tree.Nodes {
		resources = append(resources, observedResource(node))
	}
	return resources
}

func observedResource(node apiclient.ResourceNode) catalogs.ObservedResource {
	resource := catalogs.ObservedResource{ResourceRef: catalogRef(node.ResourceRef)}
	for _, parent := range node.ParentRefs {
		resource.Parents = append(resource.Parents, catalogRef(parent))
	}
	if node.Health != nil {
		resource.Health = node.Health.Status
		resource.HealthMessage = node.Health.Message
	}
	if len(node.Info) > 0 {
		resource.Info = make(map[string]string, len(node.Info))
		for _, item := range node.Info {
			resource.Info[item.Name] = item.Value
		}
	}
	return resource
}

func catalogRef(r apiclient.ResourceRef) catalogs.ResourceRef {
	return catalogs.ResourceRef{
		Group: r.Group, Version: r.Version, Kind: r.Kind, Namespace: r.Namespace, Name: r.Name, UID: r.UID,
	}
}

func apiRef(r catalogs.ResourceRef) apiclient.ResourceRef {
	return apiclient.ResourceRef{
		Group: r.Group, Version: r.Version, Kind: r.Kind, Namespace: r.Namespace, Name: r.Name, UID: r.UID,
	}
}
