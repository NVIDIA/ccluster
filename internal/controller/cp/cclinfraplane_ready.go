/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

import (
	"fmt"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
)

// readyCondition summarizes a CCLInfraPlane that is not being deleted, from the status
// patchStatus is about to write: True once Available and ClusterConfigApplied are True
// for the plane's generation and the infra-plane operator has verified the cluster
// against the plane's domain. Otherwise False, with a reason naming the first of those
// that is not, and a message carrying that condition's own reason and message.
//
// DNSReady does not count: a control plane without external-dns, or a cluster with no
// LoadBalancer, never publishes records, and is no less usable for it. A plane being
// deleted is reported by waitForComponentSets instead.
func readyCondition(infraPlane *cclusternvidiacomv1alpha1.CCLInfraPlane) metav1.Condition {
	notReady := func(reason, message string) metav1.Condition {
		return metav1.Condition{
			Type:    cclusternvidiacomv1alpha1.CCLInfraPlaneConditionReady,
			Status:  metav1.ConditionFalse,
			Reason:  reason,
			Message: message,
		}
	}

	for _, check := range []struct{ conditionType, reason string }{
		{cclusternvidiacomv1alpha1.CCLInfraPlaneConditionAvailable,
			cclusternvidiacomv1alpha1.CCLInfraPlaneReasonNotAvailable},
		{cclusternvidiacomv1alpha1.CCLInfraPlaneConditionClusterConfigApplied,
			cclusternvidiacomv1alpha1.CCLInfraPlaneReasonClusterConfigNotApplied},
	} {
		condition := apimeta.FindStatusCondition(infraPlane.Status.Conditions, check.conditionType)
		if condition == nil || condition.ObservedGeneration != infraPlane.Generation {
			return notReady(check.reason, fmt.Sprintf("%s has not been reported for this generation",
				check.conditionType))
		}
		if condition.Status != metav1.ConditionTrue {
			return notReady(check.reason, describeCondition(condition))
		}
	}

	// The copy's conditions are the infra-plane operator's and refer to the
	// CCLClusterConfig's generation, so they are taken as they are. Its domain says
	// which domain they were computed for.
	config := infraPlane.Status.ClusterConfig
	var verified *metav1.Condition
	if config != nil {
		verified = apimeta.FindStatusCondition(config.Conditions,
			cclusternvidiacomv1alpha1.CCLClusterConfigConditionVerified)
	}
	switch {
	case verified == nil:
		return notReady(cclusternvidiacomv1alpha1.CCLInfraPlaneReasonNotVerified,
			"The infra-plane operator has not reported Verified yet")
	case config.Domain != infraPlane.Status.Domain:
		return notReady(cclusternvidiacomv1alpha1.CCLInfraPlaneReasonNotVerified, fmt.Sprintf(
			"The infra-plane operator has not verified the cluster against domain %q yet",
			infraPlane.Status.Domain))
	case verified.Status != metav1.ConditionTrue:
		return notReady(cclusternvidiacomv1alpha1.CCLInfraPlaneReasonNotVerified,
			describeCondition(verified))
	}

	return metav1.Condition{
		Type:    cclusternvidiacomv1alpha1.CCLInfraPlaneConditionReady,
		Status:  metav1.ConditionTrue,
		Reason:  cclusternvidiacomv1alpha1.CCLInfraPlaneReasonReady,
		Message: "The cluster is registered with Argo CD, carries the plane's domain and is verified",
	}
}

// describeCondition renders a condition that holds a plane back for Ready's message.
func describeCondition(condition *metav1.Condition) string {
	return fmt.Sprintf("%s is %s (%s): %s", condition.Type, condition.Status, condition.Reason,
		condition.Message)
}
