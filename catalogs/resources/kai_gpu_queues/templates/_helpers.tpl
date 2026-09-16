{{- /*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/ -}}
{{- define "kaiGpuQueues.unlimited" -}}
quota: {{ .Values.unlimitedQuota | int }}
limit: {{ .Values.unlimitedQuota | int }}
overQuotaWeight: 1
{{- end }}
