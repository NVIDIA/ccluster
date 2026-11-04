/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package deployments

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/NVIDIA/ccluster/catalogs/deployments/deploymentstest"
)

// TestNodeAffinity checks that the workload charts pin pods to nodes only when
// nodeAffinityLabels is set. Kubernetes matches no node to a nodeSelectorTerms entry
// without expressions, so a pod that carries one stays Pending.
func TestNodeAffinity(t *testing.T) {
	const pin = "{nodeAffinityLabels: {node.kubernetes.io/instance-type: a100}}"
	want := []any{map[string]any{"matchExpressions": []any{map[string]any{
		"key":      "node.kubernetes.io/instance-type",
		"operator": "In",
		"values":   []any{"a100"},
	}}}}
	helm := deploymentstest.HelmBinary(t)
	for _, tt := range []struct {
		name   string
		chart  string
		values string // on top of ci/default-values.yaml
		labels string // sets nodeAffinityLabels to pin
		pinned int    // pod templates that carry the labels
	}{
		{name: "inference", chart: "inference_deployment", labels: pin, pinned: 1},
		{name: "compute", chart: "compute_deployment", labels: pin, pinned: 1},
		{name: "job", chart: "job_deployment", labels: pin, pinned: 1},
		{name: "dynamo aggregated", chart: "dynamo_deployment", labels: "workerPool: " + pin, pinned: 1},
		{
			name:  "dynamo disaggregated",
			chart: "dynamo_deployment",
			values: `
servingMode: disaggregated
workerPool: null
prefillPool: &pool
  tensorParallelSize: 1
  resources:
    limits: {cpu: "1", memory: 1Gi, acceleratorCount: 1}
    requests: {cpu: "1", memory: 1Gi, acceleratorCount: 1}
decodePool: *pool
`,
			labels: "{prefillPool: " + pin + ", decodePool: " + pin + "}",
			pinned: 2,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			files := []string{filepath.Join(tt.chart, "ci", "default-values.yaml")}
			if tt.values != "" {
				files = append(files, deploymentstest.WriteValues(t, tt.values))
			}
			if got := nodeSelectorTerms(deploymentstest.RenderChart(t, helm, tt.chart, files...)); len(got) != 0 {
				t.Errorf("without nodeAffinityLabels, nodeSelectorTerms = %v, want none", got)
			}
			withLabels := append(files, deploymentstest.WriteValues(t, tt.labels))
			got := nodeSelectorTerms(deploymentstest.RenderChart(t, helm, tt.chart, withLabels...))
			if len(got) != tt.pinned {
				t.Fatalf("with nodeAffinityLabels, %d nodeSelectorTerms, want %d", len(got), tt.pinned)
			}
			for _, terms := range got {
				if !reflect.DeepEqual(terms, want) {
					t.Errorf("with nodeAffinityLabels, nodeSelectorTerms = %v, want %v", terms, want)
				}
			}
		})
	}
}

// nodeSelectorTerms returns every nodeSelectorTerms list in objects.
func nodeSelectorTerms(objects []map[string]any) []any {
	var lists []any
	for _, obj := range objects {
		deploymentstest.Visit(obj, func(key string, value any) {
			if key == "nodeSelectorTerms" {
				lists = append(lists, value)
			}
		})
	}
	return lists
}
