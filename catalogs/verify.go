/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package catalogs

import (
	"context"
	"fmt"
	"strings"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// VerifyCRDExists is a building block for a chart's Verify function: it returns nil when
// the CustomResourceDefinition name, such as "rollouts.argoproj.io", exists, and
// otherwise an error saying it is missing. Use it for a component whose consumers in the
// catalog render one of its custom resources. Existence is all it checks: not that the
// CRD is Established, nor that a controller reconciles its resources.
//
// It reads the CRD's metadata only, so the ip operator's cache holds the metadata of
// every CRD in the cluster rather than their schemas, which run to hundreds of kilobytes
// each. r must be able to read apiextensions.k8s.io CustomResourceDefinitions; the ip
// operator's ClusterRole grants that. A read that fails is returned wrapped, not
// reported as missing.
func VerifyCRDExists(ctx context.Context, r client.Reader, name string) error {
	crd := &metav1.PartialObjectMetadata{}
	crd.SetGroupVersionKind(apiextensionsv1.SchemeGroupVersion.WithKind("CustomResourceDefinition"))
	if err := r.Get(ctx, client.ObjectKey{Name: name}, crd); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("CustomResourceDefinition %s not found", name)
		}
		return fmt.Errorf("reading CustomResourceDefinition %s: %w", name, err)
	}
	return nil
}

// LeaseSkew is how much older than its lease duration a Lease's renewTime may be and
// still count as renewed in VerifyLeaseRenewed. It allows for clock skew between the
// holder's node and the operator's, and for the time between a renewal and the read.
const LeaseSkew = 30 * time.Second

// VerifyLeaseRenewed is a building block for a chart's Verify function: it returns nil
// when a coordination.k8s.io Lease named name, in any namespace, is held and was renewed
// within its leaseDurationSeconds plus LeaseSkew, and otherwise an error naming each
// Lease found and why it does not count. Use it for a component whose controller runs
// leader election under a fixed Lease name: a held, renewed Lease means one of its
// replicas is running and leading. A controller that stopped without releasing its
// Lease leaves it behind with an old renewTime, so a Lease that merely exists proves
// nothing. Check the component's source before relying on it: leader election must be
// on by default, and the name must not depend on how it was installed.
//
// It searches every namespace because the Lease lives in the controller's, which
// follows the installation. r must serve Leases from the API server, not a cache: the
// read is a list filtered by metadata.name, which the API server answers but an
// informer cache cannot without an index, and caching Leases would mean watching every
// node's heartbeat. The ip operator's client reads Leases uncached and its ClusterRole
// grants get and list. A read that fails is returned wrapped, not reported as missing.
func VerifyLeaseRenewed(ctx context.Context, r client.Reader, name string) error {
	var leases coordinationv1.LeaseList
	if err := r.List(ctx, &leases, client.MatchingFields{"metadata.name": name}); err != nil {
		return fmt.Errorf("listing Leases %s: %w", name, err)
	}
	if len(leases.Items) == 0 {
		return fmt.Errorf("no Lease %s found in any namespace", name)
	}
	now := time.Now()
	reasons := make([]string, 0, len(leases.Items))
	for _, l := range leases.Items {
		key := l.Namespace + "/" + l.Name
		switch {
		case l.Spec.HolderIdentity == nil || *l.Spec.HolderIdentity == "":
			reasons = append(reasons, fmt.Sprintf("Lease %s has no holder", key))
		case l.Spec.RenewTime == nil:
			reasons = append(reasons, fmt.Sprintf("Lease %s was never renewed", key))
		default:
			var duration time.Duration
			if l.Spec.LeaseDurationSeconds != nil {
				duration = time.Duration(*l.Spec.LeaseDurationSeconds) * time.Second
			}
			renewed := l.Spec.RenewTime.Time
			if now.Sub(renewed) <= duration+LeaseSkew {
				return nil
			}
			// The absolute time, not an age, so the message, and with it the status,
			// does not change on every pass while the Lease stays stale.
			reasons = append(reasons, fmt.Sprintf("Lease %s last renewed at %s",
				key, renewed.UTC().Format(time.RFC3339)))
		}
	}
	return fmt.Errorf("no controller holds Lease %s: %s", name, strings.Join(reasons, "; "))
}

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
