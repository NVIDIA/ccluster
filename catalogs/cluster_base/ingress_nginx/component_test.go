/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package ingressnginx

import (
	"os"
	"testing"

	"sigs.k8s.io/yaml"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
)

// TestValuesLabelTheLoadBalancer checks that values.yaml ships the controller's Service
// as a LoadBalancer labelled as the cluster's ingress, which the infra-plane operator
// reports for the cluster's DNS.
func TestValuesLabelTheLoadBalancer(t *testing.T) {
	raw, err := os.ReadFile("values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var values struct {
		IngressNginx struct {
			Controller struct {
				Service struct {
					Enabled bool              `json:"enabled"`
					Type    string            `json:"type"`
					Labels  map[string]string `json:"labels"`
				} `json:"service"`
			} `json:"controller"`
		} `json:"ingress-nginx"`
	}
	if err := yaml.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}

	service := values.IngressNginx.Controller.Service
	if !service.Enabled || service.Type != "LoadBalancer" {
		t.Errorf("controller.service is enabled=%t type=%q, want an enabled LoadBalancer",
			service.Enabled, service.Type)
	}
	label, want := cclusternvidiacomv1alpha1.LabelLoadBalancer, "ingress"
	if got := service.Labels[label]; got != want {
		t.Errorf("controller.service.labels[%q] = %q, want %q", label, got, want)
	}
}
