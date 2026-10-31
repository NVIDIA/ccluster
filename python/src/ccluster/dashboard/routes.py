# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""The dashboard's one page, GET /.

It summarises the infra planes and component sets of the selected namespace, or of every
namespace, and lists them. Which namespaces it may show depends on how the dashboard was
started:

- Without a namespace (app.state.namespace is None), it reads across the cluster. Its
  namespace dropdown lists every namespace holding an infra plane or component set, and
  All is the default. This needs read access in every namespace.
- With a namespace, it reads that namespace only, and the dropdown holds just that one.

static/dashboard.js re-requests the page every few seconds with the FRAGMENT_HEADER set,
and gets back only the part that changes. Handlers are plain functions, which Starlette
runs in its thread pool, because the Kubernetes client blocks.
"""

from __future__ import annotations

import logging
from datetime import datetime
from typing import Any

from kubernetes import client as k8s
from starlette.requests import Request
from starlette.responses import Response
from starlette.routing import Route
from urllib3.exceptions import HTTPError

from ccluster._kube import api_error_message
from ccluster.dashboard import reads, views

logger = logging.getLogger(__name__)

# Sent by static/dashboard.js on its refresh requests, which want the page's content only.
FRAGMENT_HEADER = "X-Dashboard-Fragment"

# How often static/dashboard.js refreshes the page.
REFRESH_SECONDS = 5

# The query parameter the namespace dropdown sets; empty selects all namespaces.
NAMESPACE_PARAM = "namespace"


def routes() -> list[Route]:
    return [Route("/", overview_page)]


def overview_page(request: Request) -> Response:
    served = request.app.state.namespace
    requested = request.query_params.get(NAMESPACE_PARAM) or None
    fragment = request.headers.get(FRAGMENT_HEADER) == "1"

    if served is not None and requested not in (None, served):
        return _render(
            request,
            "error.html",
            selected=served,
            options=[served],
            fragment=fragment,
            status_code=404,
            error={
                "title": f"Namespace {requested} is not served here",
                "message": f"This dashboard was started for namespace {served}.",
                "hint": "Start ccluster dashboard without -n to see every namespace you may read.",
            },
        )

    selected = served if served is not None else requested
    api_client = request.app.state.source.api_client(request)
    # Whether the read that failed, if one does, spanned every namespace.
    cluster_wide = selected is None
    try:
        planes = reads.list_infraplanes(api_client, selected)
        sets = reads.list_component_sets(api_client, selected)
        if served is not None:
            options = [served]
        elif fragment:
            # Refreshes replace the content only, not the header holding the dropdown.
            options = []
        elif selected is None:
            options = views.namespaces(planes, sets)
        else:
            cluster_wide = True
            options = views.namespaces(
                reads.list_infraplanes(api_client, None),
                reads.list_component_sets(api_client, None),
            )
            if selected not in options:
                options = sorted([*options, selected])
    except k8s.ApiException as e:
        return _render(
            request,
            "error.html",
            selected=selected,
            options=[served] if served is not None else [],
            fragment=fragment,
            status_code=e.status or 502,
            error=_api_error_panel(e, selected, cluster_wide),
        )
    except HTTPError as e:
        logger.warning("Could not reach the API server: %s", e)
        return _render(
            request,
            "error.html",
            selected=selected,
            options=[served] if served is not None else [],
            fragment=fragment,
            status_code=502,
            error={
                "title": "Could not reach the API server",
                "message": str(e),
                "hint": "Check that the control plane is running and that its kubeconfig "
                "context still works, for example with kubectl get namespaces.",
            },
        )

    return _render(
        request,
        "overview.html",
        selected=selected,
        options=options,
        fragment=fragment,
        view=views.overview(planes, sets),
    )


def _render(
    request: Request,
    template: str,
    *,
    selected: str | None,
    options: list[str],
    fragment: bool,
    status_code: int = 200,
    **context: Any,
) -> Response:
    """Render the page, or the content alone for a refresh.

    An error panel's response carries the API server's status code, so a failed refresh
    leaves the page as it was.
    """
    served = request.app.state.namespace
    context.update(
        selected=selected,
        options=options,
        # Admin mode offers every namespace at once; user mode only its own.
        offer_all=served is None,
        fragment=fragment,
        title=selected or "All namespaces",
        refresh_seconds=REFRESH_SECONDS,
        updated=datetime.now().strftime("%H:%M:%S"),
        # The identity cannot change between refreshes, so only full pages ask for it.
        user=None
        if fragment
        else reads.whoami(request.app.state.source.api_client(request)),
    )
    return request.app.state.templates.TemplateResponse(
        request, template, context, status_code=status_code
    )


def _api_error_panel(
    e: k8s.ApiException, namespace: str | None, cluster_wide: bool
) -> dict[str, str]:
    message = api_error_message(e)
    if e.status == 401:
        return {
            "title": "Not authenticated",
            "message": message,
            "hint": "The API server did not accept the credentials. Log in again for the "
            "control plane's kubeconfig context.",
        }
    if e.status == 403 and cluster_wide:
        return {
            "title": "Cannot read all namespaces",
            "message": message,
            "hint": "Showing every namespace needs read access across the cluster: a "
            "ClusterRoleBinding to the ccluster-cp chart's cclinfraplane-viewer-role and "
            "cclcomponentset-viewer-role ClusterRoles. To see one namespace, run "
            "ccluster dashboard -n NAMESPACE.",
        }
    if e.status == 403:
        return {
            "title": f"No access to namespace {namespace}",
            "message": message,
            "hint": f"Ask a cluster admin for a RoleBinding in {namespace} to the ccluster-cp "
            "chart's cclinfraplane-viewer-role and cclcomponentset-viewer-role "
            "ClusterRoles.",
        }
    return {"title": f"API request failed ({e.status})", "message": message, "hint": ""}
