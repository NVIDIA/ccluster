/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package kaischeduler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/yaml"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	kaiv1 "github.com/NVIDIA/ccluster/internal/api/kai/v1"
)

func scheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := kaiv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

// config returns a KAI Config named name with global as its spec.global.
func config(name string, global *kaiv1.GlobalConfig) *kaiv1.Config {
	return &kaiv1.Config{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       kaiv1.ConfigSpec{Global: global},
	}
}

func named(scheduler string) *kaiv1.GlobalConfig {
	return &kaiv1.GlobalConfig{SchedulerName: &scheduler}
}

// TestVerify pins the scheduler name to the one the catalog's workloads request, with
// KAI's default standing in for an unset name.
func TestVerify(t *testing.T) {
	tests := []struct {
		name    string
		objects []client.Object
		want    string // "" means "expect nil"
	}{
		{
			// As the chart renders it: no global.schedulerName, so KAI's default.
			name:    "scheduler name unset",
			objects: []client.Object{config("kai-config", nil)},
		},
		{
			name:    "global without a scheduler name",
			objects: []client.Object{config("kai-config", &kaiv1.GlobalConfig{})},
		},
		{
			name:    "kai-scheduler set explicitly",
			objects: []client.Object{config("kai-config", named("kai-scheduler"))},
		},
		{
			name:    "another scheduler name",
			objects: []client.Object{config("kai-config", named("gpu-scheduler"))},
			want:    `KAI Config kai-config sets schedulerName "gpu-scheduler", want "kai-scheduler"`,
		},
		{
			// The chart applies it from a hook Job, which may not have run.
			name: "not found",
			want: "KAI Config kai-config not found",
		},
		{
			// The KAI operator ignores a Config under any other name.
			name:    "Config under another name",
			objects: []client.Object{config("my-config", nil)},
			want:    "KAI Config kai-config not found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(tt.objects...).Build()
			err := Verify(context.Background(), r, cclusternvidiacomv1alpha1.CCLClusterConfigSpec{})
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("want nil, got %v", err)
			case tt.want != "" && (err == nil || err.Error() != tt.want):
				t.Fatalf("want %q, got %v", tt.want, err)
			}
		})
	}
}

// TestVerifyReadErrors: a cluster without KAI has no Config kind, which is reported as
// such, and any other failed read is returned rather than reported as a missing Config.
func TestVerifyReadErrors(t *testing.T) {
	gvk := kaiv1.GroupVersion.WithKind("Config")
	tests := []struct {
		name  string
		err   error
		check func(error) bool
	}{
		{
			name: "kind not served",
			err:  &meta.NoKindMatchError{GroupKind: gvk.GroupKind(), SearchedVersions: []string{gvk.Version}},
			check: func(err error) bool {
				return err != nil && err.Error() == "KAI Config kind not served, is KAI installed?"
			},
		},
		{
			name: "forbidden",
			err: apierrors.NewForbidden(schema.GroupResource{Group: "kai.scheduler", Resource: "configs"},
				"kai-config", errors.New("no RBAC")),
			check: apierrors.IsForbidden,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := fake.NewClientBuilder().WithScheme(scheme(t)).WithInterceptorFuncs(interceptor.Funcs{
				Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
					return tt.err
				},
			}).Build()
			if err := Verify(context.Background(), r, cclusternvidiacomv1alpha1.CCLClusterConfigSpec{}); !tt.check(err) {
				t.Fatalf("unexpected error %v", err)
			}
		})
	}
}

// TestSchedulerNameMatchesDeployments keeps SchedulerName the scheduler every deployments
// chart's pods request by default.
func TestSchedulerNameMatchesDeployments(t *testing.T) {
	files, err := filepath.Glob("../../deployments/*/values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no deployments charts found")
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var values struct {
			SchedulerName string `json:"schedulerName"`
		}
		if err := yaml.Unmarshal(raw, &values); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if values.SchedulerName != SchedulerName {
			t.Errorf("%s: schedulerName is %q, want %q", f, values.SchedulerName, SchedulerName)
		}
	}
}
