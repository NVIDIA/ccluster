/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package deployments

import (
	"errors"
	"fmt"
	"strings"
)

// Endpoint is how clients reach a deployment through the cluster's shared Gateway: the
// values the library chart catalogs/deployments/edge reads beside the workload's own.
// The inference and dynamo charts embed it at the top level of their Values, so on the
// wire these are the chart's own keys.
type Endpoint struct {
	// Domain is the infra plane's domain, <plane>.<namespace>.<base domain>, which its
	// CCLInfraPlane shows as status.domain. The host is <name>.gateway.<domain>, under
	// the *.gateway.<domain> record the control-plane operator publishes for the shared
	// Gateway gateway-system/ccl-gateway, unless DeploymentURL overrides it. The host
	// does not include the namespace, so deployment names must be unique across the
	// plane. Required.
	Domain string `json:"domain"`

	// DeploymentURL replaces the derived host with a full hostname.
	DeploymentURL *string `json:"deploymentUrl,omitempty"`

	// EndpointCertificateAuthority is no longer supported: the Gateway does not ask
	// clients for certificates. Validate rejects a non-empty value, and the chart fails
	// to render with one, rather than serve the deployment without the check.
	EndpointCertificateAuthority *string `json:"endpointCertificateAuthority,omitempty"`

	// EndpointBearerToken, when set, is stored in the Secret <name>-bearer-token, from
	// which the Gateway's authorization service rejects requests to the host whose
	// Authorization header is not "Bearer <token>". Unset, the host needs no token.
	EndpointBearerToken *string `json:"endpointBearerToken,omitempty"`

	// GatewayImplementation is the Gateway API implementation behind the cluster's
	// shared Gateway, and selects its templates in catalogs/deployments/edge. It must
	// match what the cluster runs: the chart cannot read the cluster. The chart ships
	// istio, the only one supported.
	//nolint:lll // A struct tag cannot wrap.
	GatewayImplementation *GatewayImplementation `json:"gatewayImplementation,omitempty" jsonschema:"title=GatewayImplementation,enum=istio"`
}

// Validate reports a missing domain, a certificate authority the Gateway cannot check
// clients against, and an implementation the edge library has no templates for.
func (e Endpoint) Validate() error {
	errs := []error{
		MissingError(Missing(Field{"domain", e.Domain})),
		e.GatewayImplementation.Validate(),
	}
	if e.EndpointCertificateAuthority != nil && strings.TrimSpace(*e.EndpointCertificateAuthority) != "" {
		errs = append(errs, fmt.Errorf("%w: endpointCertificateAuthority; client certificates are not supported, "+
			"use endpointBearerToken", ErrInvalidValue))
	}
	return errors.Join(errs...)
}

// GatewayImplementation is the Gateway API implementation that runs the shared Gateway
// gateway-system/ccl-gateway. See DESIGN.md §3, "DNS: a wildcard record per LoadBalancer".
// Fields of this type carry the enum and a title in their jsonschema tag; see
// BackendProtocol for why.
type GatewayImplementation string

// Istio is istiod's Gateway API implementation. It is the charts' default and the only
// one they support.
const Istio GatewayImplementation = "istio"

// Validate reports an implementation the charts have no templates for. A nil receiver
// is valid: the charts ship istio.
func (g *GatewayImplementation) Validate() error {
	if g != nil && *g != Istio {
		return fmt.Errorf("%w: gatewayImplementation %q is not supported; the charts support %s",
			ErrInvalidValue, *g, Istio)
	}
	return nil
}
