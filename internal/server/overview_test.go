/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package server

import (
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
)

func cond(conditionType string, status metav1.ConditionStatus, reason string, generation int64) metav1.Condition {
	return metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            conditionType + " " + reason,
		ObservedGeneration: generation,
	}
}

func componentSet(namespace, name string, generation int64, conditions ...metav1.Condition) v1alpha1.CCLComponentSet {
	return v1alpha1.CCLComponentSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Generation: generation},
		Spec: v1alpha1.CCLComponentSetSpec{Components: []v1alpha1.ComponentSpec{
			{Name: "a", InfraPlaneRef: v1alpha1.LocalObjectReference{Name: "plane"}, ComponentEntry: v1alpha1.ComponentEntry{Chart: "catalogs/a"}},
			{Name: "b", InfraPlaneRef: v1alpha1.LocalObjectReference{Name: "plane"}, ComponentEntry: v1alpha1.ComponentEntry{Chart: "catalogs/b"}},
		}},
		Status: v1alpha1.CCLComponentSetStatus{Conditions: conditions},
	}
}

func finishedConditions(generation int64) []metav1.Condition {
	return []metav1.Condition{
		cond(v1alpha1.CCLComponentSetConditionValidated, metav1.ConditionTrue, v1alpha1.CCLComponentSetReasonComponentsAccepted, generation),
		cond(v1alpha1.CCLComponentSetConditionComponentsApplied, metav1.ConditionTrue, v1alpha1.CCLComponentSetReasonApplicationsCreated, generation),
		cond(v1alpha1.CCLComponentSetConditionProgressing, metav1.ConditionFalse, v1alpha1.CCLComponentSetReasonRolloutComplete, generation),
	}
}

func TestComponentSetStatus(t *testing.T) {
	now := metav1.Now()
	terminating := componentSet("ns", "s", 2, finishedConditions(2)...)
	terminating.DeletionTimestamp = &now

	tests := []struct {
		name string
		set  v1alpha1.CCLComponentSet
		want SetStatus
	}{
		{"no status yet", componentSet("ns", "s", 1), SetStatusRollingOut},
		{"finished", componentSet("ns", "s", 2, finishedConditions(2)...), SetStatusFinished},
		{"terminating wins over finished", terminating, SetStatusTerminating},
		{
			"finished conditions of an older generation are rolling out",
			componentSet("ns", "s", 3, finishedConditions(2)...),
			SetStatusRollingOut,
		},
		{
			"validation failed",
			componentSet("ns", "s", 1,
				cond(v1alpha1.CCLComponentSetConditionValidated, metav1.ConditionFalse, v1alpha1.CCLComponentSetReasonInfraPlaneNotFound, 1)),
			SetStatusFailed,
		},
		{
			"stale validation failure is not current",
			componentSet("ns", "s", 2,
				cond(v1alpha1.CCLComponentSetConditionValidated, metav1.ConditionFalse, v1alpha1.CCLComponentSetReasonInfraPlaneNotFound, 1)),
			SetStatusRollingOut,
		},
		{
			"applications failed",
			componentSet("ns", "s", 1,
				cond(v1alpha1.CCLComponentSetConditionValidated, metav1.ConditionTrue, v1alpha1.CCLComponentSetReasonComponentsAccepted, 1),
				cond(v1alpha1.CCLComponentSetConditionComponentsApplied, metav1.ConditionFalse, v1alpha1.CCLComponentSetReasonApplicationConflict, 1)),
			SetStatusFailed,
		},
		{
			"wave sync failed",
			componentSet("ns", "s", 1,
				cond(v1alpha1.CCLComponentSetConditionValidated, metav1.ConditionTrue, v1alpha1.CCLComponentSetReasonComponentsAccepted, 1),
				cond(v1alpha1.CCLComponentSetConditionComponentsApplied, metav1.ConditionTrue, v1alpha1.CCLComponentSetReasonApplicationsCreated, 1),
				cond(v1alpha1.CCLComponentSetConditionProgressing, metav1.ConditionTrue, v1alpha1.CCLComponentSetReasonWaveSyncFailed, 1)),
			SetStatusFailed,
		},
		{
			"wave syncing",
			componentSet("ns", "s", 1,
				cond(v1alpha1.CCLComponentSetConditionValidated, metav1.ConditionTrue, v1alpha1.CCLComponentSetReasonComponentsAccepted, 1),
				cond(v1alpha1.CCLComponentSetConditionComponentsApplied, metav1.ConditionTrue, v1alpha1.CCLComponentSetReasonApplicationsCreated, 1),
				cond(v1alpha1.CCLComponentSetConditionProgressing, metav1.ConditionTrue, v1alpha1.CCLComponentSetReasonWaveSyncing, 1)),
			SetStatusRollingOut,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := componentSetStatus(&test.set); got != test.want {
				t.Errorf("componentSetStatus = %s, want %s", got, test.want)
			}
		})
	}
}

func TestComponentRowsMarkUnreleasedComponents(t *testing.T) {
	set := componentSet("ns", "s", 1)
	set.Status.ComponentHealthStatuses = []v1alpha1.ComponentHealthStatus{
		{Name: "a", Health: v1alpha1.ComponentHealthDegraded, Reason: "CrashLoopBackOff", Message: "back-off"},
	}
	set.Status.ComponentSyncStatuses = []v1alpha1.ComponentSyncStatus{{Name: "a", Sync: "Synced"}}

	want := []ComponentRow{
		{Name: "a", Chart: "catalogs/a", InfraPlane: "plane", Health: "Degraded", Reason: "CrashLoopBackOff", Message: "back-off", Sync: "Synced"},
		{Name: "b", Chart: "catalogs/b", InfraPlane: "plane", Health: HealthNotReleased},
	}
	if got := componentRows(&set); !reflect.DeepEqual(got, want) {
		t.Errorf("componentRows =\n%+v\nwant\n%+v", got, want)
	}
}

func infraPlane(namespace, name string, generation int64, conditions ...metav1.Condition) v1alpha1.CCLInfraPlane {
	return v1alpha1.CCLInfraPlane{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Generation: generation},
		Status:     v1alpha1.CCLInfraPlaneStatus{Conditions: conditions, Domain: name + "." + namespace + ".example.com"},
	}
}

func TestBuildOverviewSummarisesAndListsBothKinds(t *testing.T) {
	healthy := infraPlane("team-a", "gpu", 1,
		cond(v1alpha1.CCLInfraPlaneConditionAvailable, metav1.ConditionTrue, v1alpha1.CCLInfraPlaneReasonClusterSecretCreated, 1))
	healthy.Status.ClusterConfig = &v1alpha1.CCLClusterConfigStatus{Conditions: []metav1.Condition{
		cond(v1alpha1.CCLClusterConfigConditionVerified, metav1.ConditionTrue, v1alpha1.CCLClusterConfigReasonComponentsVerified, 7),
	}}
	broken := infraPlane("team-b", "cpu", 1,
		cond(v1alpha1.CCLInfraPlaneConditionAvailable, metav1.ConditionFalse, v1alpha1.CCLInfraPlaneReasonSecretNotFound, 1))

	finished := componentSet("team-a", "base", 1, finishedConditions(1)...)
	finished.Status.ComponentHealthStatuses = []v1alpha1.ComponentHealthStatus{
		{Name: "a", Health: v1alpha1.ComponentHealthHealthy},
		{Name: "b", Health: v1alpha1.ComponentHealthHealthy},
	}
	failed := componentSet("team-b", "apps", 1,
		cond(v1alpha1.CCLComponentSetConditionValidated, metav1.ConditionFalse, v1alpha1.CCLComponentSetReasonInfraPlaneNotReady, 1))
	degraded := componentSet("team-a", "jobs", 1)
	degraded.Status.ComponentHealthStatuses = []v1alpha1.ComponentHealthStatus{
		{Name: "a", Health: v1alpha1.ComponentHealthDegraded, Reason: "CrashLoopBackOff", Message: "back-off"},
	}

	planes := []v1alpha1.CCLInfraPlane{broken, healthy}
	sets := []v1alpha1.CCLComponentSet{failed, finished, degraded}

	t.Run("all namespaces", func(t *testing.T) {
		got := buildOverview(planes, sets, "", nil)

		if want := []string{"team-a", "team-b"}; !reflect.DeepEqual(got.Namespaces, want) {
			t.Errorf("Namespaces = %v, want %v", got.Namespaces, want)
		}
		wantSummary := Summary{
			InfraPlanes:   InfraPlaneCounts{Total: 2, Available: 1, Verified: 1},
			ComponentSets: ComponentSetCounts{Total: 3, Finished: 1, RollingOut: 1, Failed: 1},
			Components: []HealthCount{
				{Health: "Degraded", Count: 1},
				{Health: HealthNotReleased, Count: 3},
				{Health: "Healthy", Count: 2},
			},
		}
		if !reflect.DeepEqual(got.Summary, wantSummary) {
			t.Errorf("Summary =\n%+v\nwant\n%+v", got.Summary, wantSummary)
		}
		if row := got.ComponentSets[2]; row.Status != SetStatusFailed || row.Message != "Validated InfraPlaneNotReady" {
			t.Errorf("team-b/apps = %s %q, want Failed with the Validated message", row.Status, row.Message)
		}
		if names := setNames(got.ComponentSets); !reflect.DeepEqual(names, []string{"team-a/base", "team-a/jobs", "team-b/apps"}) {
			t.Errorf("component sets = %v, want sorted by namespace and name", names)
		}
		if row := got.InfraPlanes[0]; row.Verified == nil || row.Verified.Status != metav1.ConditionTrue {
			t.Errorf("team-a/gpu Verified = %+v, want the cluster config's True", row.Verified)
		}
	})

	t.Run("one namespace selected", func(t *testing.T) {
		got := buildOverview(planes, sets, "team-a", nil)

		if want := []string{"team-a", "team-b"}; !reflect.DeepEqual(got.Namespaces, want) {
			t.Errorf("Namespaces = %v, want every namespace still offered: %v", got.Namespaces, want)
		}
		if names := setNames(got.ComponentSets); !reflect.DeepEqual(names, []string{"team-a/base", "team-a/jobs"}) {
			t.Errorf("component sets = %v, want only team-a's", names)
		}
		if got.Summary.InfraPlanes.Total != 1 || got.Summary.ComponentSets.Failed != 0 {
			t.Errorf("Summary = %+v, want only team-a counted", got.Summary)
		}
	})

	t.Run("selected namespace without objects is offered", func(t *testing.T) {
		got := buildOverview(planes, sets, "team-c", nil)
		if want := []string{"team-a", "team-b", "team-c"}; !reflect.DeepEqual(got.Namespaces, want) {
			t.Errorf("Namespaces = %v, want %v", got.Namespaces, want)
		}
		if len(got.InfraPlanes) != 0 || len(got.ComponentSets) != 0 {
			t.Errorf("overview of an empty namespace = %+v", got)
		}
	})
}

func TestInfraPlaneConditionsOfAnOlderGenerationArePending(t *testing.T) {
	plane := infraPlane("ns", "p", 2,
		cond(v1alpha1.CCLInfraPlaneConditionAvailable, metav1.ConditionFalse, v1alpha1.CCLInfraPlaneReasonSecretNotFound, 1))
	row := infraPlaneRow(&plane)
	if row.Available != nil {
		t.Errorf("Available = %+v, want nil for a condition of generation 1 on generation 2", row.Available)
	}
}

func setNames(rows []ComponentSetRow) []string {
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, row.Namespace+"/"+row.Name)
	}
	return names
}
