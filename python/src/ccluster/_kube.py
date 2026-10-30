# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Kubernetes helpers shared by the command line and the dashboard."""

from __future__ import annotations

import json

from kubernetes import client as k8s


def api_error(e: k8s.ApiException) -> str:
    """A one-line description of a failed API request, with the server's message if any."""
    return f"API request failed ({e.status}): {api_error_message(e)}"


def api_error_message(e: k8s.ApiException) -> str:
    """The API server's message for a failed request, else the HTTP reason."""
    try:
        return json.loads(e.body)["message"]
    except (TypeError, ValueError, KeyError):
        return e.reason
