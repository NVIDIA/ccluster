{{- /*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/ -}}
{{/* The deployment's HTTPS listener on the cluster's shared Gateway,
     gateway-system/ccl-gateway, and the route on it: a certificate for the host, issued
     over HTTP-01 through the Gateway's http listener; a ListenerSet that adds an HTTPS
     listener for the host to the Gateway; and an HTTPRoute on that listener to the
     Service <name>. These are Gateway API objects, the same for every implementation. */}}
{{- define "edge.listener" -}}
{{- /* Values.Validate rejects endpointCertificateAuthority, but callers that skip it, such as
       the Python SDK, reach the template directly. The Gateway does not check client
       certificates, so fail rather than serve the deployment without the check. */ -}}
{{- if trim (.Values.endpointCertificateAuthority | default "") }}
{{- fail "endpointCertificateAuthority is not supported: the Gateway does not check client certificates; use endpointBearerToken" }}
{{- end }}
{{- $host := include "edge.host" . }}
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: {{ .Values.name }}-tls
  namespace: {{ .Release.Namespace }}
spec:
  secretName: {{ .Values.name }}-tls
  dnsNames:
    - {{ $host | quote }}
  issuerRef:
    group: cert-manager.io
    kind: ClusterIssuer
    name: acme-ca
---
apiVersion: gateway.networking.k8s.io/v1
kind: ListenerSet
metadata:
  name: {{ .Values.name | quote }}
  namespace: {{ .Release.Namespace }}
spec:
  parentRef:
    group: gateway.networking.k8s.io
    kind: Gateway
    name: ccl-gateway
    namespace: gateway-system
  listeners:
    - name: https
      hostname: {{ $host | quote }}
      protocol: HTTPS
      port: 443
      tls:
        mode: Terminate
        certificateRefs:
          - kind: Secret
            name: {{ .Values.name }}-tls
---
# The Gateway routes to the Service by name, so the Host header reaches the container as
# the client sent it.
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: {{ .Values.name | quote }}
  namespace: {{ .Release.Namespace }}
spec:
  parentRefs:
    - group: gateway.networking.k8s.io
      kind: ListenerSet
      name: {{ .Values.name | quote }}
      sectionName: https
  hostnames:
    - {{ $host | quote }}
  rules:
    - backendRefs:
        - name: {{ .Values.name | quote }}
          port: {{ .Values.containerPort | int }}
{{- end -}}
