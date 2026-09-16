/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
)

func ownerInfraPlane(name, namespace string) *cclusternvidiacomv1alpha1.CCLInfraPlane {
	return &cclusternvidiacomv1alpha1.CCLInfraPlane{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	}
}

func secretWith(annotations map[string]string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "plane-a",
			Namespace:   "argocd",
			Annotations: annotations,
		},
	}
}

func TestOwnedBy(t *testing.T) {
	owner := ownerInfraPlane("plane-a", "default")

	tests := []struct {
		name        string
		annotations map[string]string
		want        bool
	}{
		{
			// A Secret created by hand carries no back-reference, so the operator may
			// adopt it and recover from a partial write.
			name:        "adopts an object carrying neither annotation",
			annotations: nil,
			want:        true,
		},
		{
			name:        "adopts an object with an empty annotation map",
			annotations: map[string]string{},
			want:        true,
		},
		{
			name: "accepts an object back-referencing this owner",
			annotations: map[string]string{
				annotationInfraPlaneName:      "plane-a",
				annotationInfraPlaneNamespace: "default",
			},
			want: true,
		},
		{
			name: "rejects an object back-referencing another name",
			annotations: map[string]string{
				annotationInfraPlaneName:      "plane-b",
				annotationInfraPlaneNamespace: "default",
			},
			want: false,
		},
		{
			// The entire reason the mechanism exists: two same-named CCLInfraPlanes in
			// different namespaces map onto one Secret in the Argo CD namespace.
			name: "rejects an object claimed by a same-named resource in another namespace",
			annotations: map[string]string{
				annotationInfraPlaneName:      "plane-a",
				annotationInfraPlaneNamespace: "other",
			},
			want: false,
		},
		{
			// Partial-annotation asymmetry. The unclaimed short-circuit requires BOTH
			// annotations to be absent, so a half-stamped object falls through to the
			// equality check and compares the missing namespace against "". It is
			// therefore NOT adoptable, and the operator refuses to repair it. That
			// reads as a contradiction of the doc comment's "created by hand ... is
			// therefore adoptable", so it is pinned here deliberately: a half-written
			// object is treated as someone else's, not as ours.
			name: "rejects an object carrying only the name annotation",
			annotations: map[string]string{
				annotationInfraPlaneName: "plane-a",
			},
			want: false,
		},
		{
			name: "rejects an object carrying only the namespace annotation",
			annotations: map[string]string{
				annotationInfraPlaneNamespace: "default",
			},
			want: false,
		},
		{
			// Safe only while the two key pairs apply to disjoint kinds, which
			// ownership.go asserts in a comment rather than in code. The day a kind
			// carries both pairs, this expectation is what fails and points at it.
			name: "treats an object stamped by the other key pair as unclaimed",
			annotations: map[string]string{
				annotationComponentSetName:      "some-componentset",
				annotationComponentSetNamespace: "default",
			},
			want: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := infraPlaneOwner.ownedBy(secretWith(test.annotations), owner); got != test.want {
				t.Errorf("ownedBy() = %v, want %v", got, test.want)
			}
		})
	}
}

// TestNamespaceOwner covers the single-key owner the AppProject uses. Its adopt-if-unclaimed
// rule is what lets the operator take over Argo CD's built-in "default" project.
func TestNamespaceOwner(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		want        bool
	}{
		{name: "adopts an unclaimed object", annotations: nil, want: true},
		{
			name:        "accepts an object claimed by this namespace",
			annotations: map[string]string{annotationInfraPlaneNamespace: "default"},
			want:        true,
		},
		{
			name:        "rejects an object claimed by another namespace",
			annotations: map[string]string{annotationInfraPlaneNamespace: "other"},
			want:        false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := projectOwner.ownedBy(secretWith(test.annotations), "default"); got != test.want {
				t.Errorf("ownedBy() = %v, want %v", got, test.want)
			}
		})
	}

	t.Run("makes a stamped object pass its own ownedBy check", func(t *testing.T) {
		obj := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "default"}}
		projectOwner.stamp(obj, "default")
		if !projectOwner.ownedBy(obj, "default") {
			t.Error("ownedBy() = false after stamp(), want true")
		}
		if projectOwner.ownedBy(obj, "other") {
			t.Error("ownedBy() = true for another namespace, want false")
		}
		if obj.Labels[labelManagedBy] != labelManagedByValue {
			t.Errorf("%s = %q, want %q", labelManagedBy, obj.Labels[labelManagedBy], labelManagedByValue)
		}
	})
}

func TestStamp(t *testing.T) {
	owner := ownerInfraPlane("plane-a", "default")

	t.Run("initialises nil label and annotation maps", func(t *testing.T) {
		// reconcileApplication writes into obj.Labels directly on the line after it
		// calls stamp, so a stamp that stopped allocating would nil-map panic there.
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "plane-a"}}
		infraPlaneOwner.stamp(secret, owner)

		if secret.Annotations[annotationInfraPlaneName] != "plane-a" {
			t.Errorf("name annotation = %q, want %q",
				secret.Annotations[annotationInfraPlaneName], "plane-a")
		}
		if secret.Annotations[annotationInfraPlaneNamespace] != "default" {
			t.Errorf("namespace annotation = %q, want %q",
				secret.Annotations[annotationInfraPlaneNamespace], "default")
		}
		if secret.Labels[labelName] != labelNameValue {
			t.Errorf("%s = %q, want %q", labelName, secret.Labels[labelName], labelNameValue)
		}
		if secret.Labels[labelManagedBy] != labelManagedByValue {
			t.Errorf("%s = %q, want %q", labelManagedBy,
				secret.Labels[labelManagedBy], labelManagedByValue)
		}
	})

	t.Run("preserves labels and annotations it does not own", func(t *testing.T) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "plane-a",
				Labels:      map[string]string{"keep": "me"},
				Annotations: map[string]string{"keep": "me"},
			},
		}
		infraPlaneOwner.stamp(secret, owner)

		if secret.Labels["keep"] != "me" {
			t.Errorf("label keep = %q, want %q", secret.Labels["keep"], "me")
		}
		if secret.Annotations["keep"] != "me" {
			t.Errorf("annotation keep = %q, want %q", secret.Annotations["keep"], "me")
		}
	})

	t.Run("makes a stamped object pass its own ownedBy check", func(t *testing.T) {
		secret := secretWith(nil)
		infraPlaneOwner.stamp(secret, owner)
		if !infraPlaneOwner.ownedBy(secret, owner) {
			t.Error("ownedBy() = false after stamp(), want true")
		}
		if infraPlaneOwner.ownedBy(secret, ownerInfraPlane("plane-a", "other")) {
			t.Error("ownedBy() = true for a same-named owner in another namespace, want false")
		}
	})
}
