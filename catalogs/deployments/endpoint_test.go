/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package deployments_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/NVIDIA/ccluster/catalogs/deployments"
	dynamodeployment "github.com/NVIDIA/ccluster/catalogs/deployments/dynamo_deployment"
	inferencedeployment "github.com/NVIDIA/ccluster/catalogs/deployments/inference_deployment"
)

// TestEndpointValidate: the domain is required, a certificate authority is rejected
// because the Gateway cannot check clients against it, and so is an implementation the
// edge library has no templates for.
func TestEndpointValidate(t *testing.T) {
	const domain = "edge-1.team-a.example.com"
	pem, blank := "-----BEGIN CERTIFICATE-----", " "
	istio, envoy := deployments.Istio, deployments.GatewayImplementation("envoy-gateway")
	for _, tt := range []struct {
		name     string
		endpoint deployments.Endpoint
		wantErr  error // nil for a valid endpoint
	}{
		{name: "domain only", endpoint: deployments.Endpoint{Domain: domain}},
		{name: "no domain", wantErr: deployments.ErrMissingValue},
		{
			name:     "blank certificate authority",
			endpoint: deployments.Endpoint{Domain: domain, EndpointCertificateAuthority: &blank},
		},
		{
			name:     "certificate authority",
			endpoint: deployments.Endpoint{Domain: domain, EndpointCertificateAuthority: &pem},
			wantErr:  deployments.ErrInvalidValue,
		},
		{name: "istio", endpoint: deployments.Endpoint{Domain: domain, GatewayImplementation: &istio}},
		{
			name:     "unsupported implementation",
			endpoint: deployments.Endpoint{Domain: domain, GatewayImplementation: &envoy},
			wantErr:  deployments.ErrInvalidValue,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.endpoint.Validate()
			if tt.wantErr == nil && err != nil || tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("Validate() = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// TestChartsValidateEndpoint: the charts that embed Endpoint report its errors.
func TestChartsValidateEndpoint(t *testing.T) {
	pem := "-----BEGIN CERTIFICATE-----"
	endpoint := deployments.Endpoint{EndpointCertificateAuthority: &pem}
	for chart, validate := range map[string]func() error{
		inferencedeployment.Chart: inferencedeployment.Values{Endpoint: endpoint}.Validate,
		dynamodeployment.Chart:    dynamodeployment.Values{Endpoint: endpoint}.Validate,
	} {
		if err := validate(); err == nil || !strings.Contains(err.Error(), "endpointCertificateAuthority") {
			t.Errorf("%s: Validate() = %v, want the Endpoint's endpointCertificateAuthority error", chart, err)
		}
	}
}
