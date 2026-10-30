# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""The dashboard's pages.

Every page is a GET that reads Kubernetes with the request's credentials, builds a view
model and renders it. static/dashboard.js re-requests the page every few seconds with the
FRAGMENT_HEADER set, and gets back only the part that changes. Handlers are plain
functions, which Starlette runs in its thread pool, because the Kubernetes client blocks.
"""

from __future__ import annotations

import logging
from collections.abc import Callable
from datetime import datetime
from typing import Any

from kubernetes import client as k8s
from starlette.requests import Request
from starlette.responses import RedirectResponse, Response
from starlette.routing import Route
from urllib3.exceptions import HTTPError

from ccluster._kube import api_error_message
from ccluster.dashboard import reads, views

logger = logging.getLogger(__name__)

# Sent by static/dashboard.js on its refresh requests, which want the page's content only.
FRAGMENT_HEADER = "X-Dashboard-Fragment"

# How often static/dashboard.js refreshes a page.
REFRESH_SECONDS = 5


def routes() -> list[Route]:
    return [
        Route("/", home),
        Route("/namespaces/{namespace}", namespace_page),
        Route("/namespaces/{namespace}/infraplanes/{name}", infra_plane_page),
        Route("/namespaces/{namespace}/componentsets/{name}", component_set_page),
    ]


def home(request: Request) -> Response:
    return RedirectResponse(
        request.url_for("namespace_page", namespace=request.app.state.namespace).path,
        status_code=302,
    )


def namespace_page(request: Request) -> Response:
    namespace = request.path_params["namespace"]
    return _render(
        request,
        "namespace.html",
        namespace,
        title=namespace,
        build=lambda api: views.overview(
            reads.list_infraplanes(api, namespace), reads.list_component_sets(api, namespace)
        ),
    )


def infra_plane_page(request: Request) -> Response:
    namespace, name = request.path_params["namespace"], request.path_params["name"]
    return _render(
        request,
        "infraplane.html",
        namespace,
        title=f"CCLInfraPlane {name}",
        build=lambda api: views.infra_plane_detail(reads.get_infraplane(api, namespace, name)),
    )


def component_set_page(request: Request) -> Response:
    namespace, name = request.path_params["namespace"], request.path_params["name"]
    return _render(
        request,
        "componentset.html",
        namespace,
        title=f"CCLComponentSet {name}",
        build=lambda api: views.component_set_detail(
            reads.get_component_set(api, namespace, name)
        ),
    )


def _render(
    request: Request,
    template: str,
    namespace: str,
    *,
    title: str,
    build: Callable[[k8s.ApiClient], Any],
) -> Response:
    """Read and render one page, or an error panel in its place.

    The panel's response carries the API server's status code, so a failed refresh leaves
    the page as it was.
    """
    api_client = request.app.state.source.api_client(request)
    fragment = request.headers.get(FRAGMENT_HEADER) == "1"
    context: dict[str, Any] = {
        "namespace": namespace,
        "title": title,
        "fragment": fragment,
        "refresh_seconds": REFRESH_SECONDS,
        "updated": datetime.now().strftime("%H:%M:%S"),
        # The identity cannot change between refreshes, so only full pages ask for it.
        "user": None if fragment else reads.whoami(api_client),
    }
    status_code = 200
    try:
        context["view"] = build(api_client)
    except k8s.ApiException as e:
        template, status_code = "error.html", e.status or 502
        context["error"] = _api_error_panel(e, namespace)
    except HTTPError as e:
        logger.warning("Could not reach the API server: %s", e)
        template, status_code = "error.html", 502
        context["error"] = {
            "title": "Could not reach the API server",
            "message": str(e),
            "hint": "Check that the control plane is running and that its kubeconfig "
            "context still works, for example with kubectl get namespaces.",
        }
    return request.app.state.templates.TemplateResponse(
        request, template, context, status_code=status_code
    )


def _api_error_panel(e: k8s.ApiException, namespace: str) -> dict[str, str]:
    message = api_error_message(e)
    if e.status == 401:
        return {
            "title": "Not authenticated",
            "message": message,
            "hint": "The API server did not accept the credentials. Log in again for the "
            "control plane's kubeconfig context.",
        }
    if e.status == 403:
        return {
            "title": f"No access to namespace {namespace}",
            "message": message,
            "hint": f"Ask a cluster admin for a RoleBinding in {namespace} to the ccluster-cp "
            "chart's cclinfraplane-viewer-role and cclcomponentset-viewer-role "
            "ClusterRoles, or open a namespace you have access to.",
        }
    if e.status == 404:
        return {"title": "Not found", "message": message, "hint": ""}
    return {"title": f"API request failed ({e.status})", "message": message, "hint": ""}
