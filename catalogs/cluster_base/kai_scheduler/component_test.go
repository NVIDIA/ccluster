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

// config returns a KAI Config named name with global as its spec.global, at generation 1
// and Available for it, as the KAI operator leaves a healthy one.
func config(name string, global *kaiv1.GlobalConfig) *kaiv1.Config {
	return &kaiv1.Config{
		ObjectMeta: metav1.ObjectMeta{Name: name, Generation: 1},
		Spec:       kaiv1.ConfigSpec{Global: global},
		Status:     kaiv1.ConfigStatus{Conditions: []metav1.Condition{available(metav1.ConditionTrue, 1)}},
	}
}

// configWith returns the kai-config Config, unset scheduler name, at generation 2 with
// conditions.
func configWith(conditions ...metav1.Condition) *kaiv1.Config {
	c := config("kai-config", nil)
	c.Generation = 2
	c.Status.Conditions = conditions
	return c
}

// shard returns a SchedulingShard named name at generation 2 with conditions.
func shard(name string, conditions ...metav1.Condition) *kaiv1.SchedulingShard {
	return &kaiv1.SchedulingShard{
		ObjectMeta: metav1.ObjectMeta{Name: name, Generation: 2},
		Status:     kaiv1.SchedulingShardStatus{Conditions: conditions},
	}
}

// available returns KAI's Available condition with status, set for generation.
func available(status metav1.ConditionStatus, generation int64) metav1.Condition {
	c := metav1.Condition{
		Type: kaiv1.ConditionAvailable, Status: status, ObservedGeneration: generation,
		Reason: "Available", Message: "System available",
	}
	if status != metav1.ConditionTrue {
		c.Message = "SchedulerForShard: not available"
	}
	return c
}

// healthyShard is the chart's default shard as the KAI operator leaves it once the
// scheduler runs.
func healthyShard() *kaiv1.SchedulingShard {
	return shard("default", available(metav1.ConditionTrue, 2))
}

func named(scheduler string) *kaiv1.GlobalConfig {
	return &kaiv1.GlobalConfig{SchedulerName: &scheduler}
}

// TestVerify pins the scheduler name to the one the catalog's workloads request, with
// KAI's default standing in for an unset name, and requires the Config and every
// SchedulingShard to be Available for their current generation.
func TestVerify(t *testing.T) {
	tests := []struct {
		name    string
		objects []client.Object
		want    string // "" means "expect nil"
	}{
		{
			// As the chart renders it: no global.schedulerName, so KAI's default.
			name:    "scheduler name unset",
			objects: []client.Object{config("kai-config", nil), healthyShard()},
		},
		{
			name:    "global without a scheduler name",
			objects: []client.Object{config("kai-config", &kaiv1.GlobalConfig{}), healthyShard()},
		},
		{
			name:    "kai-scheduler set explicitly",
			objects: []client.Object{config("kai-config", named("kai-scheduler")), healthyShard()},
		},
		{
			name:    "another scheduler name",
			objects: []client.Object{config("kai-config", named("gpu-scheduler")), healthyShard()},
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
		{
			// A KAI component such as the binder cannot start.
			name: "Config not available",
			objects: []client.Object{configWith(metav1.Condition{
				Type: kaiv1.ConditionAvailable, Status: metav1.ConditionFalse, ObservedGeneration: 2,
				Reason: "Available", Message: "binder: not available",
			}), healthyShard()},
			want: "KAI Config kai-config is not Available: Available: binder: not available",
		},
		{
			// The spec changed and the KAI operator has not reconciled it, say because it
			// is down after an upgrade: the True is for the old spec.
			name:    "Config available for an older generation",
			objects: []client.Object{configWith(available(metav1.ConditionTrue, 1)), healthyShard()},
			want:    "KAI Config kai-config reports Available for generation 1, not the current 2",
		},
		{
			// The KAI operator has not reconciled it yet, or is not running.
			name:    "Config without conditions",
			objects: []client.Object{configWith(), healthyShard()},
			want:    "KAI Config kai-config does not report Available",
		},
		{
			// The Config reports Available, but KAI runs no scheduler.
			name:    "no shards",
			objects: []client.Object{config("kai-config", nil)},
			want:    "no KAI SchedulingShard found, so no KAI scheduler runs",
		},
		{
			// The scheduler image cannot be pulled, for one.
			name:    "default shard not available",
			objects: []client.Object{config("kai-config", nil), shard("default", available(metav1.ConditionFalse, 2))},
			want: "KAI SchedulingShard default is not Available: Available: " +
				"SchedulerForShard: not available",
		},
		{
			name:    "shard available for an older generation",
			objects: []client.Object{config("kai-config", nil), shard("default", available(metav1.ConditionTrue, 1))},
			want:    "KAI SchedulingShard default reports Available for generation 1, not the current 2",
		},
		{
			// Pods labelled for the gpu-nodes pool would stay Pending.
			name: "another shard not available",
			objects: []client.Object{config("kai-config", nil), healthyShard(),
				shard("gpu-nodes", available(metav1.ConditionFalse, 2))},
			want: "KAI SchedulingShard gpu-nodes is not Available: Available: " +
				"SchedulerForShard: not available",
		},
		{
			// The chart's default shard is not required by name.
			name: "shards available without the default",
			objects: []client.Object{config("kai-config", nil),
				shard("cpu-nodes", available(metav1.ConditionTrue, 2)),
				shard("gpu-nodes", available(metav1.ConditionTrue, 2))},
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

// TestVerifyShardListError: a failed SchedulingShard read is returned, not reported as
// a cluster without shards.
func TestVerifyShardListError(t *testing.T) {
	r := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(config("kai-config", nil)).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
				return apierrors.NewForbidden(schema.GroupResource{Group: "kai.scheduler", Resource: "schedulingshards"},
					"", errors.New("no RBAC"))
			},
		}).Build()
	err := Verify(context.Background(), r, cclusternvidiacomv1alpha1.CCLClusterConfigSpec{})
	if !apierrors.IsForbidden(err) {
		t.Fatalf("want a Forbidden error, got %v", err)
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
