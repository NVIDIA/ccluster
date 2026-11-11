/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"

	argocdv1alpha1 "github.com/NVIDIA/ccluster/internal/argocd/v1alpha1"
)

var _ = Describe("applicationChanged", func() {
	It("should drop a refresh as the API server stores it", func() {
		key := client.ObjectKey{Namespace: "default", Name: "refresh-only"}
		application := &argocdv1alpha1.Application{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
			Spec:       argocdv1alpha1.ApplicationSpec{Project: "default"},
		}
		Expect(k8sClient.Create(ctx, application)).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, application))).To(Succeed())
		})
		writeStatus := func(status string) *argocdv1alpha1.Application {
			GinkgoHelper()

			Expect(k8sClient.Patch(ctx, application,
				client.RawPatch(types.MergePatchType, []byte(`{"status":`+status+`}`)))).To(Succeed())
			read := &argocdv1alpha1.Application{}
			Expect(k8sClient.Get(ctx, key, read)).To(Succeed())
			return read
		}

		old := writeStatus(`{"sync":{"status":"Synced"},"health":{"status":"Healthy"},` +
			`"reconciledAt":"2026-11-10T12:00:00Z"}`)
		refreshed := writeStatus(`{"reconciledAt":"2026-11-10T12:03:00Z"}`)
		Expect(refreshed.Generation).To(BeNumerically(">", old.Generation),
			"the status write left generation alone, so this no longer stages what Argo CD's CRD does")
		Expect(applicationChanged.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: refreshed})).To(BeFalse())
	})
})

// TestApplicationChanged: an update that changes only what every write changes, as Argo
// CD's refresh does, is dropped; any change to a field the operator decodes passes.
func TestApplicationChanged(t *testing.T) {
	old := &argocdv1alpha1.Application{
		ObjectMeta: metav1.ObjectMeta{
			Name: "web", Namespace: "team-a", ResourceVersion: "1", Generation: 1,
			Labels: map[string]string{labelComponentSetName: "set"},
		},
		Spec: argocdv1alpha1.ApplicationSpec{Project: "team-a"},
		Status: &argocdv1alpha1.ApplicationStatus{
			Health: argocdv1alpha1.ApplicationHealthStatus{Status: argocdv1alpha1.HealthStatusProgressing},
			Sync:   argocdv1alpha1.ApplicationSyncStatus{Status: argocdv1alpha1.SyncStatusSynced},
		},
	}

	for _, tc := range []struct {
		name   string
		change func(*argocdv1alpha1.Application)
		want   bool
	}{
		{
			name: "refresh only",
			change: func(a *argocdv1alpha1.Application) {
				// The CRD has no status subresource, so a status write moves generation.
				a.Generation = 2
				a.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "argocd-application-controller"}}
			},
			want: false,
		},
		{
			name:   "health",
			change: func(a *argocdv1alpha1.Application) { a.Status.Health.Status = argocdv1alpha1.HealthStatusHealthy },
			want:   true,
		},
		{
			name: "operation",
			change: func(a *argocdv1alpha1.Application) {
				a.Status.OperationState = &argocdv1alpha1.OperationState{Phase: "Running"}
			},
			want: true,
		},
		{
			name:   "spec",
			change: func(a *argocdv1alpha1.Application) { a.Spec.Project = "team-b" },
			want:   true,
		},
		{
			name:   "annotation",
			change: func(a *argocdv1alpha1.Application) { a.Annotations = map[string]string{"note": "x"} },
			want:   true,
		},
		{
			name:   "deletion",
			change: func(a *argocdv1alpha1.Application) { a.DeletionTimestamp = &metav1.Time{} },
			want:   true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			updated := old.DeepCopy()
			updated.ResourceVersion = "2"
			tc.change(updated)

			if got := applicationChanged.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: updated}); got != tc.want {
				t.Errorf("applicationChanged = %v, want %v", got, tc.want)
			}
			if old.ResourceVersion != "1" || old.Generation != 1 || updated.ResourceVersion != "2" {
				t.Errorf("the predicate changed the objects it was given, which are the cache's")
			}
		})
	}
}
