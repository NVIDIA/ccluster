{{- /*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/ -}}
{{- /*
Every template here reads the including chart's values: deployments.Endpoint holds the
keys only this library reads, and the rest, such as name and containerPort, are the
chart's own.

What differs between Gateway implementations lives in templates/<implementation>/, which
defines the same edge.<implementation>.* templates for each. The edge.* templates below pick
the implementation's, so an including chart never names one.
*/ -}}

{{/* Every object that serves the deployment through the shared Gateway: the listener and
     its route, the bearer token Secret, and what the implementation needs beyond them.
     A chart renders them all from one template, so it cannot leave one out. */}}
{{- define "edge.objects" -}}
{{ include "edge.listener" . }}
{{- with include "edge.bearerTokenSecret" . }}
---
{{ . }}
{{- end }}
{{- with include (printf "edge.%s.objects" (include "edge.gatewayImplementation" .)) . }}
---
{{ . }}
{{- end }}
{{- end -}}

{{/* The host the deployment serves: deploymentUrl, or <name>.gateway.<domain>, under the
     *.gateway.<domain> record the control plane publishes for the shared Gateway. */}}
{{- define "edge.host" -}}
{{- default (printf "%s.gateway.%s" .Values.name .Values.domain) .Values.deploymentUrl -}}
{{- end -}}

{{/* The Gateway implementation to render for; fails on one with no templates here. */}}
{{- define "edge.gatewayImplementation" -}}
{{- $implementation := toString .Values.gatewayImplementation -}}
{{- if not (has $implementation (list "istio")) -}}
{{- fail (printf "gatewayImplementation %q is not supported; the chart supports istio" $implementation) -}}
{{- end -}}
{{- $implementation -}}
{{- end -}}

{{/* The appProtocol of the Service port the HTTPRoute targets. */}}
{{- define "edge.appProtocol" -}}
{{- include (printf "edge.%s.appProtocol" (include "edge.gatewayImplementation" .)) . -}}
{{- end -}}

{{/* The PromQL request rate that wakes a deployment scaled to zero: what the Gateway
     reports for the Service while it has no endpoints. */}}
{{- define "edge.activationQuery" -}}
{{- include (printf "edge.%s.activationQuery" (include "edge.gatewayImplementation" .)) . -}}
{{- end -}}
