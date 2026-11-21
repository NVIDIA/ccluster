/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

import (
	"slices"
	"testing"

	"github.com/NVIDIA/ccluster/catalogs"
	argocdv1alpha1 "github.com/NVIDIA/ccluster/internal/api/argocd/v1alpha1"
)

// TestWithoutOldObjects: the objects a chart no longer renders, and what they own, are
// left out of the observation, and Argo CD's aggregate health only ever improves for it.
func TestWithoutOldObjects(t *testing.T) {
	deployment := catalogs.ObservedResource{
		ResourceRef: catalogs.ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "ns", Name: "web", UID: "d"},
		Health:      "Healthy",
	}
	oldSecret := catalogs.ObservedResource{
		ResourceRef: catalogs.ResourceRef{
			Group: "external-secrets.io", Kind: "ExternalSecret", Namespace: "ns", Name: "web-env-1", UID: "es",
		},
		Health: "Degraded",
	}
	ownedSecret := catalogs.ObservedResource{
		ResourceRef: catalogs.ResourceRef{Kind: "Secret", Namespace: "ns", Name: "web-env-1", UID: "s"},
		Parents:     []catalogs.ResourceRef{oldSecret.ResourceRef},
	}
	oldConfig := catalogs.ObservedResource{
		ResourceRef:   catalogs.ResourceRef{Kind: "ConfigMap", Namespace: "ns", Name: "web-r1", UID: "cm"},
		Health:        "Progressing",
		HealthMessage: "Pending deletion",
	}
	old := func(rs ...catalogs.ObservedResource) []argocdv1alpha1.ResourceStatus {
		statuses := make([]argocdv1alpha1.ResourceStatus, 0, len(rs))
		for _, r := range rs {
			statuses = append(statuses, argocdv1alpha1.ResourceStatus{
				Group: r.Group, Kind: r.Kind, Namespace: r.Namespace, Name: r.Name, RequiresPruning: true,
			})
		}
		return statuses
	}
	with := func(health string, rs ...catalogs.ObservedResource) catalogs.Observation {
		return catalogs.Observation{ApplicationHealth: health, Resources: rs}
	}
	degraded := deployment
	degraded.Health = "Degraded"

	for _, tt := range []struct {
		name       string
		observed   catalogs.Observation
		old        []argocdv1alpha1.ResourceStatus
		wantHealth string
		wantNames  []string
	}{
		{
			name:       "no old objects",
			observed:   with("Degraded", deployment, oldSecret, ownedSecret),
			wantHealth: "Degraded",
			wantNames:  []string{"web", "web-env-1", "web-env-1"},
		},
		{
			name:       "an old ExternalSecret that never became Ready",
			observed:   with("Degraded", deployment, oldSecret, ownedSecret),
			old:        old(oldSecret),
			wantHealth: "Healthy",
			wantNames:  []string{"web"},
		},
		{
			name:       "an old ConfigMap being deleted",
			observed:   with("Progressing", oldConfig, deployment),
			old:        old(oldConfig),
			wantHealth: "Healthy",
			wantNames:  []string{"web"},
		},
		{
			name:       "a current object as bad as the old one",
			observed:   with("Degraded", degraded, oldSecret),
			old:        old(oldSecret),
			wantHealth: "Degraded",
			wantNames:  []string{"web"},
		},
		{
			// Argo CD leaves some objects out of its aggregate, such as hooks.
			name:       "a current object Argo CD does not count",
			observed:   with("Healthy", degraded, oldSecret),
			old:        old(oldSecret),
			wantHealth: "Healthy",
			wantNames:  []string{"web"},
		},
		{
			name:       "an Application Argo CD has not assessed",
			observed:   with("", deployment, oldSecret),
			old:        old(oldSecret),
			wantHealth: "",
			wantNames:  []string{"web"},
		},
		{
			name:       "an owner listed after what it owns",
			observed:   with("Degraded", ownedSecret, deployment, oldSecret),
			old:        old(oldSecret),
			wantHealth: "Healthy",
			wantNames:  []string{"web"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			application := &argocdv1alpha1.Application{
				Status: &argocdv1alpha1.ApplicationStatus{Resources: tt.old},
			}
			got := withoutOldObjects(tt.observed, application)
			var names []string
			for _, r := range got.Resources {
				names = append(names, r.Name)
			}
			if got.ApplicationHealth != tt.wantHealth || !slices.Equal(names, tt.wantNames) {
				t.Errorf("withoutOldObjects = %q with %v, want %q with %v",
					got.ApplicationHealth, names, tt.wantHealth, tt.wantNames)
			}
		})
	}
}
