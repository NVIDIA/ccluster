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
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
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
	// plane's wildcard records.
	conditionTypeDNSReady = "DNSReady"

	reasonDNSPublished            = "Published"
	reasonDNSPending              = "Pending"
	reasonLoadBalancerPending     = "LoadBalancerPending"
	reasonInvalidLoadBalancerName = "InvalidLoadBalancerName"
	reasonDNSEndpointCRDMissing   = "DNSEndpointCRDMissing"
	reasonDNSEndpointConflict     = "DNSEndpointConflict"
	reasonDNSEndpointError        = "DNSEndpointError"
)

// wildcardTTLSeconds is the TTL of the wildcard records, in seconds, short so that
// resolvers follow a LoadBalancer whose address changes quickly. On Route53, external-dns
// turns a CNAME to an AWS load balancer into an alias record and replaces its TTL with
// the alias's own, so it applies there only to A and AAAA records.
const wildcardTTLSeconds = 60

// errDNSEndpointConflict reports that the DNSEndpoint name is already taken by another
// CCLInfraPlane, for the same reason as errClusterSecretConflict.
var errDNSEndpointConflict = errors.New("DNSEndpoint belongs to another CCLInfraPlane")

// dnsEndpointName is the DNSEndpoint written for a CCLInfraPlane: <namespace>-<name>, like
// the mirrored Secret, because DNSEndpoints from every namespace share one.
func dnsEndpointName(namespace, name string) string {
	return namespace + "-" + name
}

// reconcileDNS writes the DNSEndpoint that has external-dns point "*.<name>.<domain>" at
// each LoadBalancer reported in clusterConfig, <name> being its
// ccluster.nvidia.com/loadbalancer label value, or deletes it while none has an address
// to point a record at. It returns the DNSReady condition. Only unexpected Kubernetes API errors
// are returned; everything else is reported in the condition, and none of it needs a
// requeue of its own. An error is returned alongside an Unknown condition carrying it, for
// the caller to write before it returns the error, so that a DNSEndpoint failing to be
// written does not hold back the rest of the status.
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
	// Unknown rather than False: the records last published may still be in place.
	failed := func(err error) (metav1.Condition, error) {
		return condition(metav1.ConditionUnknown, reasonDNSEndpointError, err.Error()), err
	}

	endpoints, invalid := wildcardEndpoints(infraPlane.Spec.Domain, clusterConfig)
	invalidMessage := "no record for LoadBalancers whose name cannot be a DNS label: " +
		strings.Join(invalid, "; ")
	if len(endpoints) == 0 {
		// No requeue: the address arrives through the watch on the CCLClusterConfig.
		if err := r.deleteDNSEndpoint(ctx, infraPlane); err != nil {
			return failed(err)
		}
		if len(invalid) > 0 {
			return condition(metav1.ConditionFalse, reasonInvalidLoadBalancerName, invalidMessage), nil
		}
		return condition(metav1.ConditionFalse, reasonLoadBalancerPending, fmt.Sprintf(
			"waiting for the infra-plane cluster to report an address for a LoadBalancer labelled %s",
			cclusternvidiacomv1alpha1.LabelLoadBalancer)), nil
	}

	dnsEndpoint, op, err := r.applyDNSEndpoint(ctx, infraPlane, endpoints)
	switch {
	case apimeta.IsNoMatchError(err):
		// No requeue of its own: without external-dns this is the steady state. Once it is
		// installed the resync writes the DNSEndpoint, but the DNSEndpoint watch starts only
		// with the operator, so external-dns reading it goes unnoticed until the next
		// resync, or for good with a resync period of 0, unless the operator is restarted.
		log.Info("DNSEndpoint CRD not installed, skipped DNSEndpoint",
			"dnsEndpoint", dnsEndpoint.Name, "dnsEndpointNamespace", dnsEndpoint.Namespace)
		return condition(metav1.ConditionFalse, reasonDNSEndpointCRDMissing,
			"the DNSEndpoint CRD is not installed; install external-dns with the cp chart, "+
				"then restart the operator"), nil
	case errors.Is(err, errDNSEndpointConflict):
		// No requeue: only an edit elsewhere can resolve this, and the DNSEndpoint watch
		// picks that up.
		log.Info("Refused to overwrite DNSEndpoint owned by another CCLInfraPlane",
			"dnsEndpoint", dnsEndpoint.Name, "dnsEndpointNamespace", dnsEndpoint.Namespace)
		return condition(metav1.ConditionFalse, reasonDNSEndpointConflict, fmt.Sprintf(
			"DNSEndpoint %q in namespace %q belongs to another CCLInfraPlane",
			dnsEndpoint.Name, dnsEndpoint.Namespace)), nil
	case err != nil:
		return failed(err)
	case op != controllerutil.OperationResultNone:
		log.Info("Applied DNSEndpoint", "dnsEndpoint", dnsEndpoint.Name,
			"dnsEndpointNamespace", dnsEndpoint.Namespace, "records", describeRecords(endpoints),
			"operation", op)
	}

	records := describeRecords(endpoints)
	switch {
	case dnsEndpoint.Status.ObservedGeneration < dnsEndpoint.Generation:
		// No requeue: external-dns writing the DNSEndpoint's status is an event.
		return condition(metav1.ConditionFalse, reasonDNSPending, fmt.Sprintf(
			"waiting for external-dns to read DNSEndpoint %q in namespace %q: %s",
			dnsEndpoint.Name, dnsEndpoint.Namespace, records)), nil
	case len(invalid) > 0:
		// No requeue: only relabelling the Service fixes this, which reaches the
		// CCLClusterConfig watch.
		return condition(metav1.ConditionFalse, reasonInvalidLoadBalancerName, fmt.Sprintf(
			"external-dns read DNSEndpoint %q in namespace %q: %s; %s",
			dnsEndpoint.Name, dnsEndpoint.Namespace, records, invalidMessage)), nil
	}
	// external-dns marks a DNSEndpoint read before it calls the provider, so this does not
	// say the records were written; see https://github.com/NVIDIA/ccluster/issues/174.
	return condition(metav1.ConditionTrue, reasonDNSPublished, fmt.Sprintf(
		"external-dns read DNSEndpoint %q in namespace %q: %s; it reports whether it wrote "+
			"them only in its logs",
		dnsEndpoint.Name, dnsEndpoint.Namespace, records)), nil
}

// wildcardEndpoints returns a "*.<name>.<domain>" record for each LoadBalancer in
// clusterConfig that has an address, in its order. A LoadBalancer whose name is not a
// DNS label, or makes the record's name too long, gets none and is described in invalid
// instead: its ccluster.nvidia.com/loadbalancer label value may hold characters a DNS
// label may not.
func wildcardEndpoints(
	domain string,
	clusterConfig *cclusternvidiacomv1alpha1.CCLClusterConfigStatus,
) (endpoints []externaldnsv1alpha1.Endpoint, invalid []string) {
	if clusterConfig == nil {
		return nil, nil
	}
	for _, lb := range clusterConfig.LoadBalancers {
		dnsName := "*." + lb.Name + "." + domain
		errs := validation.IsDNS1123Label(lb.Name)
		if len(dnsName) > validation.DNS1123SubdomainMaxLength {
			errs = append(errs, fmt.Sprintf("%s is longer than %d characters",
				dnsName, validation.DNS1123SubdomainMaxLength))
		}
		if len(errs) > 0 {
			invalid = append(invalid, fmt.Sprintf("%q: %s", lb.Name, strings.Join(errs, ", ")))
			continue
		}
		if endpoint := wildcardEndpoint(dnsName, lb); endpoint != nil {
			endpoints = append(endpoints, *endpoint)
		}
	}
	return endpoints, invalid
}

// wildcardEndpoint returns the dnsName record pointing at lb - an A or AAAA record for an
// IPv4 or IPv6 address, otherwise a CNAME for a hostname - or nil if it has no address.
func wildcardEndpoint(
	dnsName string,
	lb cclusternvidiacomv1alpha1.LoadBalancerStatus,
) *externaldnsv1alpha1.Endpoint {
	endpoint := &externaldnsv1alpha1.Endpoint{DNSName: dnsName, RecordTTL: wildcardTTLSeconds}
	switch {
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

// describeRecords lists records as "<name> <type> <target>", separated by commas.
func describeRecords(endpoints []externaldnsv1alpha1.Endpoint) string {
	records := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		records = append(records, fmt.Sprintf("%s %s %s",
			endpoint.DNSName, endpoint.RecordType, strings.Join(endpoint.Targets, ",")))
	}
	return strings.Join(records, ", ")
}

// applyDNSEndpoint creates or updates infraPlane's DNSEndpoint in DNSEndpointNamespace,
// holding endpoints alone. Like the mirrored Secret it cannot carry an owner reference,
// living in another namespace than its CCLInfraPlane, so it is stamped with
// infraPlaneOwner instead.
func (r *CCLInfraPlaneReconciler) applyDNSEndpoint(
	ctx context.Context,
	infraPlane *cclusternvidiacomv1alpha1.CCLInfraPlane,
	endpoints []externaldnsv1alpha1.Endpoint,
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
		dnsEndpoint.Spec.Endpoints = endpoints
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
// external-dns reading it, or drift on it, reconciles the plane.
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
