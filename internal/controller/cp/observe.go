/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

import (
	"context"
	"errors"
	"sync"

	"github.com/NVIDIA/ccluster/catalogs"
	"github.com/NVIDIA/ccluster/internal/argocd/apiclient"
)

// observeConcurrency bounds the requests one observation has in flight. Each object in
// the tree costs two, and a deployment's tree runs to a few dozen objects.
const observeConcurrency = 8

// ResourceObserver reads everything a chart's status function is handed about one
// component: its Application's objects, their live manifests and their events.
type ResourceObserver interface {
	Observe(ctx context.Context, app apiclient.ApplicationRef) (catalogs.ObservedResources, error)
}

// argoCDObserver observes a component through argocd-server.
type argoCDObserver struct {
	client *apiclient.Client
}

// NewArgoCDObserver returns a ResourceObserver that reads through c.
func NewArgoCDObserver(c *apiclient.Client) ResourceObserver {
	return &argoCDObserver{client: c}
}

// Observe reads app's resource tree, then every node's manifest and events.
//
// A node that has gone since the tree was cached keeps its place with a nil manifest
// and no events: that is an observation too, and the chart decides what it means. Any
// other failure fails the whole observation, since a chart handed a partial one would
// report a state nothing supports.
func (o *argoCDObserver) Observe(ctx context.Context, app apiclient.ApplicationRef) (catalogs.ObservedResources, error) {
	tree, err := o.client.ResourceTree(ctx, app)
	if err != nil {
		return nil, err
	}

	observed := make(catalogs.ObservedResources, len(tree.Nodes))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// The first failure cancels the reads still running; theirs are only its echo.
	var (
		first     error
		firstOnce sync.Once
	)
	slots := make(chan struct{}, observeConcurrency)
	var wg sync.WaitGroup
	for i, node := range tree.Nodes {
		observed[i] = observedResource(node)
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			if err := o.read(ctx, app, &observed[i]); err != nil {
				firstOnce.Do(func() { first = err })
				cancel()
			}
		})
	}
	wg.Wait()

	if first != nil {
		return nil, first
	}
	return observed, nil
}

// read fills in resource's manifest and events.
func (o *argoCDObserver) read(ctx context.Context, app apiclient.ApplicationRef, resource *catalogs.ObservedResource) error {
	ref := apiRef(resource.ResourceRef)
	manifest, err := o.client.Resource(ctx, app, ref)
	if errors.Is(err, apiclient.ErrResourceGone) {
		return nil
	}
	if err != nil {
		return err
	}
	events, err := o.client.ResourceEvents(ctx, app, ref)
	if errors.Is(err, apiclient.ErrResourceGone) {
		return nil
	}
	if err != nil {
		return err
	}
	resource.Manifest = manifest
	resource.Events = events
	return nil
}

// observedResource converts a tree node into the catalog's type, without its manifest
// or events.
func observedResource(node apiclient.ResourceNode) catalogs.ObservedResource {
	resource := catalogs.ObservedResource{ResourceRef: catalogRef(node.ResourceRef)}
	for _, parent := range node.ParentRefs {
		resource.Parents = append(resource.Parents, catalogRef(parent))
	}
	if node.Health != nil {
		resource.Health = node.Health.Status
		resource.HealthMessage = node.Health.Message
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
