/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

import (
	"context"
	"errors"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/internal/cloud/aws/route53"
)

// The DNSReady condition and its reasons.
const (
	// conditionTypeDNSReady reports on the plane's Route53 hosted zone and the wildcard
	// record in it.
	conditionTypeDNSReady = "DNSReady"

	reasonDNSPublished            = "Published"
	reasonDNSNotConfigured        = "NotConfigured"
	reasonDomainOutsideParentZone = "DomainOutsideParentZone"
	reasonRoute53Error            = "Route53Error"
	reasonLoadBalancerPending     = "LoadBalancerPending"
)

// dnsRetryInterval is how soon a failed Route53 call is tried again. Route53 recovering
// does not arrive as an event.
const dnsRetryInterval = time.Minute

// reconcileDNS creates the Route53 hosted zone for infraPlane's domain, delegated from the
// parent zone, and points "*.<domain>" in it at the ingress LoadBalancer reported in
// clusterConfig. It returns the DNSReady condition, the zone when it was ensured - nil
// otherwise, keeping the last one recorded - and when to look again. Failures are
// reported in the condition, not returned.
//
// The wildcard record is deleted while there is no address to point it at. The zone stays
// for as long as the CCLInfraPlane does.
func (r *CCLInfraPlaneReconciler) reconcileDNS(
	ctx context.Context,
	infraPlane *cclusternvidiacomv1alpha1.CCLInfraPlane,
	clusterConfig *cclusternvidiacomv1alpha1.CCLClusterConfigStatus,
) (metav1.Condition, *cclusternvidiacomv1alpha1.DNSStatus, ctrl.Result) {
	log := logf.FromContext(ctx)
	domain := infraPlane.Spec.Domain

	condition := func(status metav1.ConditionStatus, reason, message string) metav1.Condition {
		return metav1.Condition{
			Type:               conditionTypeDNSReady,
			Status:             status,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: infraPlane.Generation,
		}
	}

	if r.Zones == nil {
		return condition(metav1.ConditionFalse, reasonDNSNotConfigured,
			"the control-plane operator runs without --dns-parent-zone-id"), nil, ctrl.Result{}
	}

	zone, err := r.Zones.EnsureZone(ctx, domain, zoneOwner(infraPlane), recordedZoneID(infraPlane))
	switch {
	case errors.Is(err, route53.ErrOutsideParentZone):
		// No requeue: the domain is immutable, and the parent zone changes only with a
		// restart.
		log.Info("Skipped Route53 hosted zone for domain outside the parent zone",
			"domain", domain, "reason", err.Error())
		return condition(metav1.ConditionFalse, reasonDomainOutsideParentZone, err.Error()),
			nil, ctrl.Result{}
	case err != nil:
		log.Error(err, "Failed to ensure Route53 hosted zone", "domain", domain)
		return condition(metav1.ConditionFalse, reasonRoute53Error, err.Error()),
			nil, ctrl.Result{RequeueAfter: dnsRetryInterval}
	}
	dns := &cclusternvidiacomv1alpha1.DNSStatus{ZoneID: zone.ID, NameServers: zone.NameServers}

	record := wildcardRecord(clusterConfig)
	changed, err := r.Zones.SetWildcard(ctx, zone.ID, domain, record)
	if err != nil {
		log.Error(err, "Failed to set wildcard record", "domain", domain, "zoneID", zone.ID)
		return condition(metav1.ConditionFalse, reasonRoute53Error, err.Error()),
			dns, ctrl.Result{RequeueAfter: dnsRetryInterval}
	}

	name := "*." + domain
	if record == nil {
		if changed {
			log.Info("Deleted wildcard record", "name", name, "zoneID", zone.ID)
		}
		// No requeue: the address arrives through the watch on the CCLClusterConfig.
		return condition(metav1.ConditionFalse, reasonLoadBalancerPending, fmt.Sprintf(
				"hosted zone %s is delegated; waiting for the infra-plane cluster to report an address for its %q LoadBalancer",
				zone.ID, "ingress")),
			dns, ctrl.Result{}
	}
	if changed {
		log.Info("Set wildcard record", "name", name, "zoneID", zone.ID,
			"type", record.Type, "value", record.Value)
	}
	return condition(metav1.ConditionTrue, reasonDNSPublished, fmt.Sprintf(
			"%s %s %s in hosted zone %s", name, record.Type, record.Value, zone.ID)),
		dns, ctrl.Result{}
}

// wildcardRecord returns what the wildcard record points at: the ingress LoadBalancer in
// clusterConfig, as an A record for an IP or else a CNAME for a hostname, or nil if it has
// no address.
func wildcardRecord(clusterConfig *cclusternvidiacomv1alpha1.CCLClusterConfigStatus) *route53.Record {
	if clusterConfig == nil {
		return nil
	}
	lb := clusterConfig.LoadBalancer("ingress")
	switch {
	case lb == nil:
		return nil
	case lb.IP != "":
		return &route53.Record{Type: "A", Value: lb.IP}
	case lb.Hostname != "":
		return &route53.Record{Type: "CNAME", Value: lb.Hostname}
	default:
		return nil
	}
}

// finalizeDNS deletes infraPlane's hosted zone, with its records and the parent zone's
// delegation to it. Unlike an unreachable infra-plane cluster, a Route53 failure blocks the
// finalizer: a delegation left pointing at a deleted zone lets whoever creates a zone on
// the same name servers take over the domain.
func (r *CCLInfraPlaneReconciler) finalizeDNS(
	ctx context.Context,
	infraPlane *cclusternvidiacomv1alpha1.CCLInfraPlane,
) error {
	if r.Zones == nil || infraPlane.Spec.Domain == "" {
		// A zone created before DNS was turned off stays behind.
		return nil
	}
	return r.Zones.DeleteZone(ctx, infraPlane.Spec.Domain, zoneOwner(infraPlane), recordedZoneID(infraPlane))
}

// zoneOwner identifies infraPlane to the ZoneManager.
func zoneOwner(infraPlane *cclusternvidiacomv1alpha1.CCLInfraPlane) route53.Owner {
	return route53.Owner{
		UID:       string(infraPlane.UID),
		Namespace: infraPlane.Namespace,
		Name:      infraPlane.Name,
	}
}

// recordedZoneID returns the hosted zone ID last recorded in infraPlane's status, or "".
func recordedZoneID(infraPlane *cclusternvidiacomv1alpha1.CCLInfraPlane) string {
	if infraPlane.Status.DNS == nil {
		return ""
	}
	return infraPlane.Status.DNS.ZoneID
}
