/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package deployments

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// edgeCharts are the charts that serve through the shared Gateway with the edge library,
// with the name their ci values give the deployment.
var edgeCharts = []struct{ chart, name string }{
	{chart: "inference_deployment", name: "ci-inference"},
	{chart: "dynamo_deployment", name: "ci-dynamo"},
}

// TestGatewayRoute checks how the inference and dynamo charts attach to the shared
// Gateway gateway-system/ccl-gateway: a Certificate for the host, a ListenerSet with an
// HTTPS listener for it, an HTTPRoute on that listener to the chart's Service, and a
// Service that declares the backend protocol to Istio. Nothing is an Ingress.
func TestGatewayRoute(t *testing.T) {
	// The Service port the HTTPRoute targets: both charts' ci values serve on 8000.
	// Numbers decode as float64.
	const port = float64(8000)
	helm := helmBinary(t)
	for _, tt := range edgeCharts {
		for _, c := range []struct {
			name, values, host, appProtocol string
		}{
			{name: "default", host: tt.name + ".gateway.ci.example.com", appProtocol: "http"},
			{
				name:   "deploymentUrl",
				values: "deploymentUrl: model.example.org", host: "model.example.org", appProtocol: "http",
			},
			{
				name:   "GRPC",
				values: "backendProtocol: GRPC", host: tt.name + ".gateway.ci.example.com", appProtocol: "grpc",
			},
		} {
			t.Run(tt.chart+"/"+c.name, func(t *testing.T) {
				files := []string{filepath.Join(tt.chart, "ci", "default-values.yaml")}
				if c.values != "" {
					files = append(files, writeValues(t, c.values))
				}
				objects := renderChart(t, helm, tt.chart, files...)
				for _, obj := range objects {
					if obj["kind"] == "Ingress" {
						t.Errorf("rendered an Ingress %v", get(obj, "metadata", "name"))
					}
					if obj["kind"] == "Service" && get(obj, "spec", "type") == "ExternalName" {
						t.Errorf("rendered an ExternalName Service %v", get(obj, "metadata", "name"))
					}
				}

				certificate := find(t, objects, "Certificate", tt.name+"-tls")
				wantEqual(t, "Certificate spec", certificate["spec"], map[string]any{
					"secretName": tt.name + "-tls",
					"dnsNames":   []any{c.host},
					"issuerRef":  map[string]any{"group": "cert-manager.io", "kind": "ClusterIssuer", "name": "acme-ca"},
				})

				listenerSet := find(t, objects, "ListenerSet", tt.name)
				wantEqual(t, "ListenerSet spec", listenerSet["spec"], map[string]any{
					"parentRef": map[string]any{
						"group": "gateway.networking.k8s.io", "kind": "Gateway",
						"name": "ccl-gateway", "namespace": "gateway-system",
					},
					"listeners": []any{map[string]any{
						"name": "https", "hostname": c.host, "protocol": "HTTPS", "port": float64(443),
						"tls": map[string]any{
							"mode":            "Terminate",
							"certificateRefs": []any{map[string]any{"kind": "Secret", "name": tt.name + "-tls"}},
						},
					}},
				})

				route := find(t, objects, "HTTPRoute", tt.name)
				wantEqual(t, "HTTPRoute spec", route["spec"], map[string]any{
					"parentRefs": []any{map[string]any{
						"group": "gateway.networking.k8s.io", "kind": "ListenerSet",
						"name": tt.name, "sectionName": "https",
					}},
					"hostnames": []any{c.host},
					"rules": []any{map[string]any{
						"backendRefs": []any{map[string]any{"name": tt.name, "port": port}},
					}},
				})

				service := find(t, objects, "Service", tt.name)
				ports, _ := get(service, "spec", "ports").([]any)
				if len(ports) != 1 {
					t.Fatalf("Service %s ports = %v, want one", tt.name, ports)
				}
				servicePort, _ := ports[0].(map[string]any)
				if servicePort["port"] != port || servicePort["appProtocol"] != c.appProtocol {
					t.Errorf("Service %s port = %v, want port %v with appProtocol %s",
						tt.name, servicePort, port, c.appProtocol)
				}
			})
		}
	}
}

// TestBearerTokenSecret checks the Secret the Gateway's authorization service reads:
// rendered only with a token, labelled and annotated with the host, holding the token
// without the "Bearer " prefix, and the only object that carries the token.
func TestBearerTokenSecret(t *testing.T) {
	const token = "s3cret-Token"
	helm := helmBinary(t)
	for _, tt := range edgeCharts {
		t.Run(tt.chart, func(t *testing.T) {
			defaults := filepath.Join(tt.chart, "ci", "default-values.yaml")
			for _, obj := range renderChart(t, helm, tt.chart, defaults) {
				if isBearerTokenSecret(obj) {
					t.Errorf("without endpointBearerToken, rendered Secret %v", get(obj, "metadata", "name"))
				}
			}

			objects := renderChart(t, helm, tt.chart, defaults, writeValues(t, "endpointBearerToken: "+token))
			secret := find(t, objects, "Secret", tt.name+"-bearer-token")
			if !isBearerTokenSecret(secret) {
				t.Errorf("Secret labels = %v, want ccluster.nvidia.com/bearer-token: \"true\"",
					get(secret, "metadata", "labels"))
			}
			wantEqual(t, "Secret annotations", get(secret, "metadata", "annotations"),
				map[string]any{"ccluster.nvidia.com/host": tt.name + ".gateway.ci.example.com"})
			wantEqual(t, "Secret data", secret["data"],
				map[string]any{"token": base64.StdEncoding.EncodeToString([]byte(token))})

			for _, obj := range objects {
				if obj["kind"] == "Secret" && get(obj, "metadata", "name") == tt.name+"-bearer-token" {
					continue
				}
				if strings.Contains(fmt.Sprint(obj), token) {
					t.Errorf("%v %v carries the token", obj["kind"], get(obj, "metadata", "name"))
				}
			}
		})
	}
}

// TestEndpointCertificateAuthorityFailsRender: the Gateway does not check client
// certificates, so a chart given a CA must not render a deployment served without the
// check. Values.Validate rejects it too, but the Python SDK does not run Validate.
func TestEndpointCertificateAuthorityFailsRender(t *testing.T) {
	helm := helmBinary(t)
	for _, tt := range edgeCharts {
		t.Run(tt.chart, func(t *testing.T) {
			defaults := filepath.Join(tt.chart, "ci", "default-values.yaml")
			blank := writeValues(t, `endpointCertificateAuthority: " "`)
			if _, err := helmTemplate(t, helm, tt.chart, defaults, blank); err != nil {
				t.Errorf("a blank endpointCertificateAuthority failed the render: %v", err)
			}
			_, err := helmTemplate(t, helm, tt.chart, defaults,
				writeValues(t, "endpointCertificateAuthority: \"-----BEGIN CERTIFICATE-----\""))
			if err == nil || !strings.Contains(err.Error(), "endpointCertificateAuthority is not supported") {
				t.Errorf("with endpointCertificateAuthority, helm template error = %v, want the chart's failure", err)
			}
		})
	}
}

// TestGatewayImplementation: the charts fail on an implementation the edge library has
// no templates for, rather than render a deployment without that implementation's
// objects.
func TestGatewayImplementation(t *testing.T) {
	helm := helmBinary(t)
	for _, tt := range edgeCharts {
		t.Run(tt.chart, func(t *testing.T) {
			_, err := helmTemplate(t, helm, tt.chart, filepath.Join(tt.chart, "ci", "default-values.yaml"),
				writeValues(t, "gatewayImplementation: envoy-gateway"))
			if err == nil || !strings.Contains(err.Error(), `gatewayImplementation "envoy-gateway" is not supported`) {
				t.Errorf("with gatewayImplementation envoy-gateway, helm template error = %v, want the chart's failure", err)
			}
		})
	}
}

// TestSessionAffinity: with sessionAffinity, inference_deployment renders istio's
// consistent-hash DestinationRule on its Service, keyed on X-Session-Id; without it, no
// DestinationRule.
func TestSessionAffinity(t *testing.T) {
	helm := helmBinary(t)
	defaults := filepath.Join("inference_deployment", "ci", "default-values.yaml")
	for _, obj := range renderChart(t, helm, "inference_deployment", defaults) {
		if obj["kind"] == "DestinationRule" {
			t.Errorf("without sessionAffinity, rendered DestinationRule %v", get(obj, "metadata", "name"))
		}
	}

	objects := renderChart(t, helm, "inference_deployment", defaults, writeValues(t, "sessionAffinity: true"))
	rule := find(t, objects, "DestinationRule", "ci-inference")
	wantEqual(t, "DestinationRule host", get(rule, "spec", "host"), "ci-inference.ccluster-ci.svc.cluster.local")
	wantEqual(t, "DestinationRule consistentHash",
		get(rule, "spec", "trafficPolicy", "loadBalancer", "consistentHash"),
		map[string]any{"httpHeaderName": "X-Session-Id"})
}

// TestActivationIgnoresDeniedRequests: inference_deployment wakes from zero on the
// requests the Gateway reports for its Service, but not on those the Gateway's
// authorization service denied (response flag UAEX), or a client without the bearer
// token could keep the deployment running.
func TestActivationIgnoresDeniedRequests(t *testing.T) {
	helm := helmBinary(t)
	objects := renderChart(t, helm, "inference_deployment",
		filepath.Join("inference_deployment", "ci", "default-values.yaml"))
	scaledObject := find(t, objects, "ScaledObject", "ci-inference-scaledobject")
	triggers, _ := get(scaledObject, "spec", "triggers").([]any)
	for _, trigger := range triggers {
		m, _ := trigger.(map[string]any)
		if get(m, "metadata", "metricName") != "ci-inference-gateway-rps" {
			continue
		}
		query := fmt.Sprint(get(m, "metadata", "query"))
		for _, want := range []string{
			`istio_requests_total{`,
			`destination_service_namespace="ccluster-ci"`,
			`destination_service_name="ci-inference"`,
			`response_flags!~".*UAEX.*"`,
		} {
			if !strings.Contains(query, want) {
				t.Errorf("activation query %q does not contain %s", query, want)
			}
		}
		return
	}
	t.Fatalf("ScaledObject triggers %v have no ci-inference-gateway-rps trigger", triggers)
}

func isBearerTokenSecret(obj map[string]any) bool {
	labels, _ := get(obj, "metadata", "labels").(map[string]any)
	return obj["kind"] == "Secret" && labels["ccluster.nvidia.com/bearer-token"] == "true"
}

// find returns the object of kind and name in objects, failing the test if there is none.
func find(t *testing.T, objects []map[string]any, kind, name string) map[string]any {
	t.Helper()
	for _, obj := range objects {
		if obj["kind"] == kind && get(obj, "metadata", "name") == name {
			return obj
		}
	}
	t.Fatalf("no %s %s rendered", kind, name)
	return nil
}

// get returns the value at path in obj, or nil.
func get(obj map[string]any, path ...string) any {
	var value any = obj
	for _, key := range path {
		m, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = m[key]
	}
	return value
}

func wantEqual(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %#v, want %#v", what, got, want)
	}
}
