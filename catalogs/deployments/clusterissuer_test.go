/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package deployments

import (
	"path/filepath"
	"testing"
)

// TestIngressClusterIssuer checks that the Ingresses of the inference and dynamo charts
// request the ClusterIssuer that catalogs/cluster_base/final_stack creates. Each chart
// spells the name out, and a mismatch leaves every Ingress without a certificate.
func TestIngressClusterIssuer(t *testing.T) {
	helm := helmBinary(t)

	var issuers []string
	for _, obj := range renderChart(t, helm, filepath.Join("..", "cluster_base", "final_stack")) {
		if obj["kind"] == "ClusterIssuer" {
			metadata, _ := obj["metadata"].(map[string]any)
			name, _ := metadata["name"].(string)
			issuers = append(issuers, name)
		}
	}
	if len(issuers) != 1 {
		t.Fatalf("final_stack renders ClusterIssuers %q, want exactly one", issuers)
	}
	issuer := issuers[0]

	for _, chart := range []string{"inference_deployment", "dynamo_deployment"} {
		t.Run(chart, func(t *testing.T) {
			ingresses := 0
			for _, obj := range renderChart(t, helm, chart, filepath.Join(chart, "ci", "default-values.yaml")) {
				if obj["kind"] != "Ingress" {
					continue
				}
				ingresses++
				metadata, _ := obj["metadata"].(map[string]any)
				annotations, _ := metadata["annotations"].(map[string]any)
				if got := annotations["cert-manager.io/cluster-issuer"]; got != issuer {
					t.Errorf("Ingress %v requests ClusterIssuer %v, want %q from final_stack",
						metadata["name"], got, issuer)
				}
			}
			if ingresses == 0 {
				t.Fatal("no Ingress rendered")
			}
		})
	}
}
