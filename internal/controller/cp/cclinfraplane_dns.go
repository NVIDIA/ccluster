/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	externaldnsv1alpha1 "github.com/NVIDIA/ccluster/internal/externaldns/v1alpha1"
)

// The DNSReady condition and its reasons.
const (
	// conditionTypeDNSReady reports on the DNSEndpoint that has external-dns publish the
	// plane's wildcard record.
	conditionTypeDNSReady = "DNSReady"

	reasonDNSPublished          = "Published"
	reasonDNSPending            = "Pending"
	reasonLoadBalancerPending   = "LoadBalancerPending"
	reasonDNSEndpointCRDMissing = "DNSEndpointCRDMissing"
	reasonDNSEndpointConflict   = "DNSEndpointConflict"
)

// wildcardTTL is the TTL of the wildcard record, short so that resolvers follow a
// LoadBalancer whose address changes quickly.
const wildcardTTL = 60

// errDNSEndpointConflict reports that the DNSEndpoint name is already taken by another
// CCLInfraPlane, for the same reason as errClusterSecretConflict.
var errDNSEndpointConflict = errors.New("DNSEndpoint belongs to another CCLInfraPlane")

// dnsEndpointName is the DNSEndpoint written for a CCLInfraPlane: <namespace>-<name>, like
// the mirrored Secret, because DNSEndpoints from every namespace share one.
func dnsEndpointName(namespace, name string) string {
	return namespace + "-" + name
}

// reconcileDNS writes the DNSEndpoint that has external-dns point "*.<domain>" at the
// ingress LoadBalancer reported in clusterConfig, or deletes it while there is no address
// to point it at. It returns the DNSReady condition. Only unexpected Kubernetes API errors
// are returned; everything else is reported in the condition, and none of it needs a
// requeue of its own.
//
// Which DNS zone the record lands in is external-dns's choice: the most specific zone it
// manages that the domain falls under. Creating that zone, and delegating it, is the
// user's.
func (r *CCLInfraPlaneReconciler) reconcileDNS(
	ctx context.Context,
	infraPlane *cclusternvidiacomv1alpha1.CCLInfraPlane,
	clusterConfig *cclusternvidiacomv1alpha1.CCLClusterConfigStatus,
) (metav1.Condition, error) {
	log := logf.FromContext(ctx)

	condition := func(status metav1.ConditionStatus, reason, message string) metav1.Condition {
		return metav1.Condition{
			Type:               conditionTypeDNSReady,
			Status:             status,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: infraPlane.Generation,
		}
	}

	endpoint := wildcardEndpoint(infraPlane.Spec.Domain, clusterConfig)
	if endpoint == nil {
		// No requeue: the address arrives through the watch on the CCLClusterConfig.
		if err := r.deleteDNSEndpoint(ctx, infraPlane); err != nil {
			return metav1.Condition{}, err
		}
		return condition(metav1.ConditionFalse, reasonLoadBalancerPending, fmt.Sprintf(
			"waiting for the infra-plane cluster to report an address for its %q LoadBalancer",
			"ingress")), nil
	}

	dnsEndpoint, op, err := r.applyDNSEndpoint(ctx, infraPlane, *endpoint)
	switch {
	case apimeta.IsNoMatchError(err):
		// No requeue of its own: without external-dns this is the steady state, and the
		// resync notices external-dns being installed.
		log.Info("DNSEndpoint CRD not installed, skipped DNSEndpoint",
			"dnsEndpoint", dnsEndpoint.Name, "dnsEndpointNamespace", dnsEndpoint.Namespace)
		return condition(metav1.ConditionFalse, reasonDNSEndpointCRDMissing,
			"the DNSEndpoint CRD is not installed; install external-dns with the cp chart"), nil
	case errors.Is(err, errDNSEndpointConflict):
		// No requeue: only an edit elsewhere can resolve this, and the DNSEndpoint watch
		// picks that up.
		log.Info("Refused to overwrite DNSEndpoint owned by another CCLInfraPlane",
			"dnsEndpoint", dnsEndpoint.Name, "dnsEndpointNamespace", dnsEndpoint.Namespace)
		return condition(metav1.ConditionFalse, reasonDNSEndpointConflict, fmt.Sprintf(
			"DNSEndpoint %q in namespace %q belongs to another CCLInfraPlane",
			dnsEndpoint.Name, dnsEndpoint.Namespace)), nil
	case err != nil:
		return metav1.Condition{}, err
	case op != controllerutil.OperationResultNone:
		log.Info("Applied DNSEndpoint", "dnsEndpoint", dnsEndpoint.Name,
			"dnsEndpointNamespace", dnsEndpoint.Namespace, "dnsName", endpoint.DNSName,
			"recordType", endpoint.RecordType, "targets", endpoint.Targets, "operation", op)
	}

	record := fmt.Sprintf("%s %s %s", endpoint.DNSName, endpoint.RecordType, endpoint.Targets[0])
	if dnsEndpoint.Status.ObservedGeneration < dnsEndpoint.Generation {
		// No requeue: external-dns writing the DNSEndpoint's status is an event.
		return condition(metav1.ConditionFalse, reasonDNSPending, fmt.Sprintf(
			"waiting for external-dns to accept DNSEndpoint %q in namespace %q: %s",
			dnsEndpoint.Name, dnsEndpoint.Namespace, record)), nil
	}
	// external-dns accepts a record that matches none of its zones too, and drops it; see
	// https://github.com/NVIDIA/ccluster/issues/174.
	return condition(metav1.ConditionTrue, reasonDNSPublished, fmt.Sprintf(
		"external-dns accepted DNSEndpoint %q in namespace %q: %s; it is published if "+
			"external-dns manages a zone that %s falls under",
		dnsEndpoint.Name, dnsEndpoint.Namespace, record, infraPlane.Spec.Domain)), nil
}

// wildcardEndpoint returns the "*.<domain>" record pointing at the ingress LoadBalancer
// in clusterConfig - an A or AAAA record for an IPv4 or IPv6 address, otherwise a CNAME
// for a hostname - or nil if it has no address.
func wildcardEndpoint(
	domain string,
	clusterConfig *cclusternvidiacomv1alpha1.CCLClusterConfigStatus,
) *externaldnsv1alpha1.Endpoint {
	if clusterConfig == nil {
		return nil
	}
	lb := clusterConfig.LoadBalancer("ingress")
	endpoint := &externaldnsv1alpha1.Endpoint{DNSName: "*." + domain, RecordTTL: wildcardTTL}
	switch {
	case lb == nil:
		return nil
	case lb.IP != "":
		endpoint.RecordType = "A"
		if addr, err := netip.ParseAddr(lb.IP); err == nil && addr.Unmap().Is6() {
			endpoint.RecordType = "AAAA"
		}
		endpoint.Targets = []string{lb.IP}
	case lb.Hostname != "":
		endpoint.RecordType = "CNAME"
		endpoint.Targets = []string{lb.Hostname}
	default:
		return nil
	}
	return endpoint
}

// applyDNSEndpoint creates or updates infraPlane's DNSEndpoint in DNSEndpointNamespace,
// holding endpoint alone. Like the mirrored Secret it cannot carry an owner reference,
// living in another namespace than its CCLInfraPlane, so it is stamped with
// infraPlaneOwner instead.
func (r *CCLInfraPlaneReconciler) applyDNSEndpoint(
	ctx context.Context,
	infraPlane *cclusternvidiacomv1alpha1.CCLInfraPlane,
	endpoint externaldnsv1alpha1.Endpoint,
) (*externaldnsv1alpha1.DNSEndpoint, controllerutil.OperationResult, error) {
	dnsEndpoint := &externaldnsv1alpha1.DNSEndpoint{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dnsEndpointName(infraPlane.Namespace, infraPlane.Name),
			Namespace: r.DNSEndpointNamespace,
		},
	}
	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, dnsEndpoint, func() error {
		if !infraPlaneOwner.ownedBy(dnsEndpoint, infraPlane) {
			return errDNSEndpointConflict
		}
		infraPlaneOwner.stamp(dnsEndpoint, infraPlane)
		dnsEndpoint.Spec.Endpoints = []externaldnsv1alpha1.Endpoint{endpoint}
		return nil
	})
	return dnsEndpoint, op, err
}

// deleteDNSEndpoint deletes infraPlane's DNSEndpoint if there is one and it is still ours,
// which has external-dns withdraw the record. A missing CRD means there is none.
func (r *CCLInfraPlaneReconciler) deleteDNSEndpoint(
	ctx context.Context,
	infraPlane *cclusternvidiacomv1alpha1.CCLInfraPlane,
) error {
	log := logf.FromContext(ctx)

	var dnsEndpoint externaldnsv1alpha1.DNSEndpoint
	key := client.ObjectKey{
		Namespace: r.DNSEndpointNamespace,
		Name:      dnsEndpointName(infraPlane.Namespace, infraPlane.Name),
	}
	switch err := r.Get(ctx, key, &dnsEndpoint); {
	case apierrors.IsNotFound(err), apimeta.IsNoMatchError(err):
		return nil
	case err != nil:
		return err
	case !infraPlaneOwner.ownedBy(&dnsEndpoint, infraPlane):
		log.Info("Left DNSEndpoint owned by another CCLInfraPlane in place",
			"dnsEndpoint", key.Name, "dnsEndpointNamespace", key.Namespace)
		return nil
	}

	if err := r.Delete(ctx, &dnsEndpoint); client.IgnoreNotFound(err) != nil {
		return err
	}
	log.Info("Deleted DNSEndpoint", "dnsEndpoint", key.Name, "dnsEndpointNamespace", key.Namespace)
	return nil
}

// infraPlanesForDNSEndpoint maps a DNSEndpoint the operator wrote to its CCLInfraPlane, so
// external-dns accepting it, or drift on it, reconciles the plane.
func (r *CCLInfraPlaneReconciler) infraPlanesForDNSEndpoint(_ context.Context, obj client.Object) []reconcile.Request {
	if obj.GetNamespace() != r.DNSEndpointNamespace {
		return nil
	}
	name, hasName := obj.GetAnnotations()[annotationInfraPlaneName]
	namespace, hasNamespace := obj.GetAnnotations()[annotationInfraPlaneNamespace]
	if !hasName || !hasNamespace {
		return nil
	}
	return []reconcile.Request{{NamespacedName: client.ObjectKey{Namespace: namespace, Name: name}}}
}
