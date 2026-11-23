/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
)

// The states envtest does not cheaply reach: conditions of an older generation, and the
// order in which several problems are reported.
func TestReadyCondition(t *testing.T) {
	const domain = "edge.team-a.example.com"
	condition := func(conditionType string, status metav1.ConditionStatus, generation int64) metav1.Condition {
		return metav1.Condition{Type: conditionType, Status: status, Reason: "Test",
			Message: "set by the test", ObservedGeneration: generation}
	}
	available := func(status metav1.ConditionStatus, generation int64) metav1.Condition {
		return condition(cclusternvidiacomv1alpha1.CCLInfraPlaneConditionAvailable, status, generation)
	}
	applied := func(status metav1.ConditionStatus, generation int64) metav1.Condition {
		return condition(cclusternvidiacomv1alpha1.CCLInfraPlaneConditionClusterConfigApplied, status, generation)
	}
	verified := func(status metav1.ConditionStatus, configDomain string) *cclusternvidiacomv1alpha1.CCLClusterConfigStatus {
		return &cclusternvidiacomv1alpha1.CCLClusterConfigStatus{
			Domain: configDomain,
			// The infra-plane operator's generation, unrelated to the plane's.
			Conditions: []metav1.Condition{
				condition(cclusternvidiacomv1alpha1.CCLClusterConfigConditionVerified, status, 9),
			},
		}
	}
	dnsFailing := condition(cclusternvidiacomv1alpha1.CCLInfraPlaneConditionDNSReady, metav1.ConditionFalse, 2)

	for _, test := range []struct {
		name          string
		conditions    []metav1.Condition
		clusterConfig *cclusternvidiacomv1alpha1.CCLClusterConfigStatus
		status        metav1.ConditionStatus
		reason        string
		message       string
	}{{
		name:       "nothing reported",
		conditions: nil,
		status:     metav1.ConditionFalse,
		reason:     cclusternvidiacomv1alpha1.CCLInfraPlaneReasonNotAvailable,
		message:    "has not been reported",
	}, {
		name:          "Available of an older generation",
		conditions:    []metav1.Condition{available(metav1.ConditionTrue, 1), applied(metav1.ConditionTrue, 2)},
		clusterConfig: verified(metav1.ConditionTrue, domain),
		status:        metav1.ConditionFalse,
		reason:        cclusternvidiacomv1alpha1.CCLInfraPlaneReasonNotAvailable,
		message:       "has not been reported for this generation",
	}, {
		name:          "Available False wins over every later problem",
		conditions:    []metav1.Condition{available(metav1.ConditionFalse, 2), applied(metav1.ConditionUnknown, 2)},
		clusterConfig: verified(metav1.ConditionFalse, domain),
		status:        metav1.ConditionFalse,
		reason:        cclusternvidiacomv1alpha1.CCLInfraPlaneReasonNotAvailable,
		message:       "Available is False (Test): set by the test",
	}, {
		name:          "ClusterConfigApplied Unknown",
		conditions:    []metav1.Condition{available(metav1.ConditionTrue, 2), applied(metav1.ConditionUnknown, 2)},
		clusterConfig: verified(metav1.ConditionTrue, domain),
		status:        metav1.ConditionFalse,
		reason:        cclusternvidiacomv1alpha1.CCLInfraPlaneReasonClusterConfigNotApplied,
		message:       "ClusterConfigApplied is Unknown",
	}, {
		name:       "no copy of the cluster's status",
		conditions: []metav1.Condition{available(metav1.ConditionTrue, 2), applied(metav1.ConditionTrue, 2)},
		status:     metav1.ConditionFalse,
		reason:     cclusternvidiacomv1alpha1.CCLInfraPlaneReasonNotVerified,
		message:    "has not reported Verified",
	}, {
		name:          "verified against another domain",
		conditions:    []metav1.Condition{available(metav1.ConditionTrue, 2), applied(metav1.ConditionTrue, 2)},
		clusterConfig: verified(metav1.ConditionTrue, "old."+domain),
		status:        metav1.ConditionFalse,
		reason:        cclusternvidiacomv1alpha1.CCLInfraPlaneReasonNotVerified,
		message:       domain,
	}, {
		name:          "ready, whatever DNSReady says",
		conditions:    []metav1.Condition{available(metav1.ConditionTrue, 2), applied(metav1.ConditionTrue, 2), dnsFailing},
		clusterConfig: verified(metav1.ConditionTrue, domain),
		status:        metav1.ConditionTrue,
		reason:        cclusternvidiacomv1alpha1.CCLInfraPlaneReasonReady,
	}} {
		t.Run(test.name, func(t *testing.T) {
			infraPlane := &cclusternvidiacomv1alpha1.CCLInfraPlane{
				ObjectMeta: metav1.ObjectMeta{Generation: 2},
				Status: cclusternvidiacomv1alpha1.CCLInfraPlaneStatus{
					Conditions:    test.conditions,
					Domain:        domain,
					ClusterConfig: test.clusterConfig,
				},
			}
			got := readyCondition(infraPlane)
			if got.Type != cclusternvidiacomv1alpha1.CCLInfraPlaneConditionReady ||
				got.Status != test.status || got.Reason != test.reason ||
				!strings.Contains(got.Message, test.message) {
				t.Errorf("readyCondition() = %s/%s %q, want %s/%s containing %q",
					got.Status, got.Reason, got.Message, test.status, test.reason, test.message)
			}
		})
	}
}
