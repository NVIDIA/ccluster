/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package server

import (
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/pkg/cclclient"
)

// Overview is the body of GET /api/v1/overview: everything the overview page shows, read
// in one pass so the summary and the tables always agree.
type Overview struct {
	// Namespaces are the dropdown's choices: every namespace holding an infra plane or a
	// component set, plus the selected one. With --namespace, only that namespace.
	Namespaces []string `json:"namespaces"`
	// Selected is the namespace shown, or "" for all of them.
	Selected      string            `json:"selected"`
	Summary       Summary           `json:"summary"`
	InfraPlanes   []InfraPlaneRow   `json:"infraPlanes"`
	ComponentSets []ComponentSetRow `json:"componentSets"`
}

// Summary counts what the tables list.
type Summary struct {
	InfraPlanes   InfraPlaneCounts   `json:"infraPlanes"`
	ComponentSets ComponentSetCounts `json:"componentSets"`
}

// InfraPlaneCounts counts infra planes by their Available and Verified conditions.
type InfraPlaneCounts struct {
	Total     int `json:"total"`
	Available int `json:"available"`
	Verified  int `json:"verified"`
}

// ComponentSetCounts counts component sets by SetStatus.
type ComponentSetCounts struct {
	Total       int `json:"total"`
	Finished    int `json:"finished"`
	RollingOut  int `json:"rollingOut"`
	Failed      int `json:"failed"`
	Terminating int `json:"terminating"`
}

// Condition is the part of a metav1.Condition the page shows.
type Condition struct {
	Status  metav1.ConditionStatus `json:"status"`
	Reason  string                 `json:"reason,omitempty"`
	Message string                 `json:"message,omitempty"`
}

// InfraPlaneRow is one row of the infra planes table, after the CRD's printer columns.
// A condition the operator has not reported for the current generation is nil.
type InfraPlaneRow struct {
	Namespace            string     `json:"namespace"`
	Name                 string     `json:"name"`
	Domain               string     `json:"domain,omitempty"`
	Available            *Condition `json:"available"`
	ClusterConfigApplied *Condition `json:"clusterConfigApplied"`
	Verified             *Condition `json:"verified"`
	DNSReady             *Condition `json:"dnsReady"`
	Created              time.Time  `json:"created"`
}

// SetStatus is a component set's status on the page, judged by componentSetStatus.
type SetStatus string

const (
	SetStatusTerminating SetStatus = "Terminating"
	SetStatusFinished    SetStatus = "Finished"
	SetStatusFailed      SetStatus = "Failed"
	SetStatusRollingOut  SetStatus = "RollingOut"
)

// HealthNotReleased is the health shown for a component whose Argo CD Application the
// operator has not created yet, because an earlier component is not Healthy and Synced.
const HealthNotReleased = "NotReleased"

// ComponentSetRow is one row of the component sets table.
type ComponentSetRow struct {
	Namespace string    `json:"namespace"`
	Name      string    `json:"name"`
	Status    SetStatus `json:"status"`
	// Applied and Progressing are the current generation's conditions, as in the CRD's
	// printer columns; nil when not reported for it.
	Applied     *Condition `json:"applied"`
	Progressing *Condition `json:"progressing"`
	// Message explains Status: the failing condition's message, or the rollout's.
	Message    string         `json:"message,omitempty"`
	Components []ComponentRow `json:"components"`
	Created    time.Time      `json:"created"`
}

// ComponentRow is one component of a set, in rollout order.
type ComponentRow struct {
	Name       string `json:"name"`
	Chart      string `json:"chart"`
	InfraPlane string `json:"infraPlane"`
	// Health is a cclusternvidiacomv1alpha1.ComponentHealth, or HealthNotReleased.
	Health  string `json:"health"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
	// Sync is Argo CD's sync status of the component's Application, if released.
	Sync string `json:"sync,omitempty"`
	// Removal is set for a component removed from the spec whose Application the
	// operator is still deleting: a cclusternvidiacomv1alpha1.ComponentRemoval.
	Removal string `json:"removal,omitempty"`
}

// buildOverview turns the listed objects into the page. planes and sets are those of
// every namespace offered; selected, when not empty, narrows the summary and tables to
// one of them. namespaces is the dropdown's fixed choice, or nil to derive it from the
// objects.
func buildOverview(
	planes []cclusternvidiacomv1alpha1.CCLInfraPlane,
	sets []cclusternvidiacomv1alpha1.CCLComponentSet,
	selected string,
	namespaces []string,
) Overview {
	if namespaces == nil {
		namespaces = namespacesOf(planes, sets, selected)
	}
	overview := Overview{
		Namespaces:    namespaces,
		Selected:      selected,
		InfraPlanes:   []InfraPlaneRow{},
		ComponentSets: []ComponentSetRow{},
	}

	for i := range planes {
		plane := &planes[i]
		if selected != "" && plane.Namespace != selected {
			continue
		}
		row := infraPlaneRow(plane)
		overview.InfraPlanes = append(overview.InfraPlanes, row)
		overview.Summary.InfraPlanes.Total++
		if isTrue(row.Available) {
			overview.Summary.InfraPlanes.Available++
		}
		if isTrue(row.Verified) {
			overview.Summary.InfraPlanes.Verified++
		}
	}

	for i := range sets {
		set := &sets[i]
		if selected != "" && set.Namespace != selected {
			continue
		}
		row := componentSetRow(set)
		overview.ComponentSets = append(overview.ComponentSets, row)
		counts := &overview.Summary.ComponentSets
		counts.Total++
		switch row.Status {
		case SetStatusTerminating:
			counts.Terminating++
		case SetStatusFinished:
			counts.Finished++
		case SetStatusFailed:
			counts.Failed++
		default:
			counts.RollingOut++
		}
	}

	slices.SortFunc(overview.InfraPlanes, func(a, b InfraPlaneRow) int {
		return compareNames(a.Namespace, a.Name, b.Namespace, b.Name)
	})
	slices.SortFunc(overview.ComponentSets, func(a, b ComponentSetRow) int {
		return compareNames(a.Namespace, a.Name, b.Namespace, b.Name)
	})
	return overview
}

func infraPlaneRow(plane *cclusternvidiacomv1alpha1.CCLInfraPlane) InfraPlaneRow {
	current := currentConditions(plane.Generation, plane.Status.Conditions)
	row := InfraPlaneRow{
		Namespace:            plane.Namespace,
		Name:                 plane.Name,
		Domain:               plane.Status.Domain,
		Available:            current[cclusternvidiacomv1alpha1.CCLInfraPlaneConditionAvailable],
		ClusterConfigApplied: current[cclusternvidiacomv1alpha1.CCLInfraPlaneConditionClusterConfigApplied],
		DNSReady:             current[cclusternvidiacomv1alpha1.CCLInfraPlaneConditionDNSReady],
		Created:              plane.CreationTimestamp.UTC(),
	}
	// The cluster config's conditions are the infra-plane operator's and refer to the
	// CCLClusterConfig's generation, not this object's, so they are taken as they are.
	if config := plane.Status.ClusterConfig; config != nil {
		row.Verified = condition(meta.FindStatusCondition(
			config.Conditions, cclusternvidiacomv1alpha1.CCLClusterConfigConditionVerified))
	}
	return row
}

func componentSetRow(set *cclusternvidiacomv1alpha1.CCLComponentSet) ComponentSetRow {
	current := cclclient.ComponentSetCurrentConditions(set)
	row := ComponentSetRow{
		Namespace:   set.Namespace,
		Name:        set.Name,
		Status:      componentSetStatus(set),
		Applied:     conditionOf(current, cclusternvidiacomv1alpha1.CCLComponentSetConditionComponentsApplied),
		Progressing: conditionOf(current, cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing),
		Components:  componentRows(set),
		Created:     set.CreationTimestamp.UTC(),
	}
	switch row.Status {
	case SetStatusFailed:
		row.Message = failedCondition(set).Message
	case SetStatusRollingOut:
		if row.Progressing != nil {
			row.Message = row.Progressing.Message
		}
	}
	return row
}

// componentSetStatus judges a set: Terminating while it is being deleted, else Finished
// when cclclient.ComponentSetFinished says so, else Failed when the current generation
// has a condition saying the rollout cannot go on by itself, else Rolling out.
func componentSetStatus(set *cclusternvidiacomv1alpha1.CCLComponentSet) SetStatus {
	switch {
	case set.DeletionTimestamp != nil:
		return SetStatusTerminating
	case cclclient.ComponentSetFinished(set):
		return SetStatusFinished
	case failedCondition(set) != nil:
		return SetStatusFailed
	default:
		return SetStatusRollingOut
	}
}

// failedCondition returns the current condition that makes a set Failed: one saying the
// rollout cannot go on until someone acts. That is Progressing with reason
// WaveSyncFailed, or ComponentsApplied False because Argo CD's Application CRD is
// missing or another set owns an Application. nil if none. A set waiting on an infra
// plane (Validated False) or on a retried apply error is still rolling out: the operator
// carries on by itself once the plane is available or the API server answers.
func failedCondition(set *cclusternvidiacomv1alpha1.CCLComponentSet) *metav1.Condition {
	current := cclclient.ComponentSetCurrentConditions(set)
	if condition, ok := current[cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing]; ok &&
		condition.Reason == cclusternvidiacomv1alpha1.CCLComponentSetReasonWaveSyncFailed {
		return &condition
	}
	if condition, ok := current[cclusternvidiacomv1alpha1.CCLComponentSetConditionComponentsApplied]; ok &&
		condition.Status == metav1.ConditionFalse && slices.Contains([]string{
		cclusternvidiacomv1alpha1.CCLComponentSetReasonApplicationCRDMissing,
		cclusternvidiacomv1alpha1.CCLComponentSetReasonApplicationConflict,
	}, condition.Reason) {
		return &condition
	}
	return nil
}

// componentRows lists the set's components in rollout order with their reported health
// and sync status, then the components removed from the spec whose Applications are
// still being deleted, in the order the operator deletes them. A component without a
// health entry has not been released.
func componentRows(set *cclusternvidiacomv1alpha1.CCLComponentSet) []ComponentRow {
	health := map[string]cclusternvidiacomv1alpha1.ComponentHealthStatus{}
	for _, status := range set.Status.ComponentHealthStatuses {
		health[status.Name] = status
	}
	sync := map[string]cclusternvidiacomv1alpha1.ComponentSyncStatus{}
	for _, status := range set.Status.ComponentSyncStatuses {
		sync[status.Name] = status
	}
	row := func(name string) ComponentRow {
		row := ComponentRow{Name: name, Health: HealthNotReleased}
		if status, ok := health[name]; ok {
			row.Health = string(status.Health)
			row.Reason = status.Reason
			row.Message = status.Message
		}
		if status, ok := sync[name]; ok {
			row.Sync = status.Sync
			row.Removal = string(status.Removal)
			if row.Message == "" {
				row.Message = status.Message
			}
		}
		return row
	}

	// A status written before a removed component was added back can still mark it
	// removed; the spec wins.
	inSpec := map[string]bool{}
	rows := make([]ComponentRow, 0, len(set.Status.ComponentSyncStatuses)+len(set.Spec.Components))
	for _, component := range set.Spec.Components {
		inSpec[component.Name] = true
		r := row(component.Name)
		r.Chart = component.Chart
		r.InfraPlane = component.InfraPlaneRef.Name
		r.Removal = ""
		rows = append(rows, r)
	}
	for _, status := range set.Status.ComponentSyncStatuses {
		if status.Removal != "" && !inSpec[status.Name] {
			rows = append(rows, row(status.Name))
		}
	}
	return rows
}

// currentConditions returns the conditions computed from generation, keyed by type, as
// cclclient.ComponentSetCurrentConditions does for a component set: one the operator has
// not rewritten since the spec changed describes an older spec.
func currentConditions(generation int64, conditions []metav1.Condition) map[string]*Condition {
	current := map[string]*Condition{}
	for i := range conditions {
		if conditions[i].ObservedGeneration == generation {
			current[conditions[i].Type] = condition(&conditions[i])
		}
	}
	return current
}

func conditionOf(conditions map[string]metav1.Condition, conditionType string) *Condition {
	if c, ok := conditions[conditionType]; ok {
		return condition(&c)
	}
	return nil
}

func condition(c *metav1.Condition) *Condition {
	if c == nil {
		return nil
	}
	return &Condition{Status: c.Status, Reason: c.Reason, Message: c.Message}
}

func isTrue(c *Condition) bool {
	return c != nil && c.Status == metav1.ConditionTrue
}

// namespacesOf returns, sorted, every namespace holding one of the objects, and
// selected.
func namespacesOf(
	planes []cclusternvidiacomv1alpha1.CCLInfraPlane,
	sets []cclusternvidiacomv1alpha1.CCLComponentSet,
	selected string,
) []string {
	namespaces := []string{}
	if selected != "" {
		namespaces = append(namespaces, selected)
	}
	for i := range planes {
		namespaces = append(namespaces, planes[i].Namespace)
	}
	for i := range sets {
		namespaces = append(namespaces, sets[i].Namespace)
	}
	slices.Sort(namespaces)
	return slices.Compact(namespaces)
}

func compareNames(namespaceA, nameA, namespaceB, nameB string) int {
	if c := strings.Compare(namespaceA, namespaceB); c != 0 {
		return c
	}
	return strings.Compare(nameA, nameB)
}
