# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Where the dashboard's Kubernetes credentials come from.

Routes obtain their ApiClient only from a CredentialSource, so every read runs with
whatever credentials the source chooses for that request and Kubernetes RBAC decides what
it may see. The dashboard holds no credentials or permissions of its own.
"""

from __future__ import annotations

from typing import Protocol

from kubernetes import client as k8s
from starlette.requests import Request


class CredentialSource(Protocol):
    """Gives each request the ApiClient its Kubernetes reads are made with."""

    def api_client(self, request: Request) -> k8s.ApiClient: ...


class LocalSource:
    """One ApiClient for every request: the user's own, from their kubeconfig.

    Built once, so a kubeconfig exec plugin, such as a cloud token helper, runs once per
    token rather than once per request.
    """

    def __init__(self, api_client: k8s.ApiClient) -> None:
        self._api_client = api_client

    def api_client(self, request: Request) -> k8s.ApiClient:
        return self._api_client
