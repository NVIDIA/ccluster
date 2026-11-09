/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package catalogs

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// VerifyServiceReady is a building block for a chart's Verify function: it returns nil
// when the Service namespace/name exists and has at least one ready endpoint, and
// otherwise an error saying which of the two is missing. Use it for a component that
// other catalog charts reach at a fixed address.
//
// r must be able to read Services and discovery.k8s.io EndpointSlices; the ip operator's
// ClusterRole grants both. A read that fails is returned wrapped, not reported as missing.
func VerifyServiceReady(ctx context.Context, r client.Reader, namespace, name string) error {
	var svc corev1.Service
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &svc); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("no Service %s/%s found", namespace, name)
		}
		return fmt.Errorf("reading Service %s/%s: %w", namespace, name, err)
	}

	var slices discoveryv1.EndpointSliceList
	if err := r.List(ctx, &slices, client.InNamespace(namespace),
		client.MatchingLabels{discoveryv1.LabelServiceName: name}); err != nil {
		return fmt.Errorf("listing EndpointSlices of Service %s/%s: %w", namespace, name, err)
	}
	for _, s := range slices.Items {
		for _, e := range s.Endpoints {
			// A nil Ready means unknown, which the API defines as ready.
			if e.Conditions.Ready == nil || *e.Conditions.Ready {
				return nil
			}
		}
	}
	return fmt.Errorf("no ready endpoints for Service %s/%s", namespace, name)
}
