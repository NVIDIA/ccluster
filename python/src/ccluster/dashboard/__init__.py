# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""ccluster dashboard: a read-only web view of a control plane.

create_app builds the Starlette application: one server-rendered page summarising the
CCLInfraPlanes and CCLComponentSets of every namespace, or of the one namespace it serves,
refreshed in place by a small script.
It holds no credentials: each request's Kubernetes reads use the ApiClient the
CredentialSource gives it, and Kubernetes RBAC decides what they return. serve runs it
with uvicorn. Needs the dashboard extra (Starlette, Jinja2, uvicorn).
"""

from __future__ import annotations

from collections.abc import Callable

import uvicorn
from jinja2 import Environment, PackageLoader
from starlette.applications import Starlette
from starlette.datastructures import MutableHeaders
from starlette.middleware import Middleware
from starlette.middleware.trustedhost import TrustedHostMiddleware
from starlette.routing import Mount
from starlette.staticfiles import StaticFiles
from starlette.templating import Jinja2Templates
from starlette.types import ASGIApp, Message, Receive, Scope, Send

from ccluster.dashboard.credentials import CredentialSource, LocalSource
from ccluster.dashboard.routes import routes

__all__ = ["CredentialSource", "LocalSource", "create_app", "serve"]

# Pages carry cluster data and are only ever rendered by this origin: no caching, no
# framing, no scripts, styles or requests to anywhere else, and no referrer sent with
# links out.
_SECURITY_HEADERS = {
    "Content-Security-Policy": "default-src 'self'; base-uri 'none'; form-action 'self'; "
    "frame-ancestors 'none'",
    "Referrer-Policy": "no-referrer",
    "X-Content-Type-Options": "nosniff",
    "Cache-Control": "no-store",
}


def create_app(
    source: CredentialSource, *, namespace: str | None, allowed_hosts: list[str]
) -> Starlette:
    """Build the dashboard.

    With namespace None it reads across the cluster and lets the viewer pick any namespace
    holding ccluster objects, or all of them; with a namespace it reads only that one.

    allowed_hosts are the host names requests may address. Anything else is refused, so
    that a web page on another domain cannot reach the dashboard through DNS rebinding.
    The application only serves GET (and HEAD) requests.
    """
    app = Starlette(
        routes=[
            *routes(),
            Mount("/static", StaticFiles(packages=[("ccluster.dashboard", "static")]), name="static"),
        ],
        middleware=[
            Middleware(_SecurityHeaders),
            Middleware(TrustedHostMiddleware, allowed_hosts=allowed_hosts),
        ],
    )
    app.state.source = source
    app.state.namespace = namespace
    app.state.templates = Jinja2Templates(
        env=Environment(loader=PackageLoader("ccluster.dashboard"), autoescape=True)
    )
    return app


def serve(
    app: Starlette, *, host: str, port: int, on_started: Callable[[], None] | None = None
) -> None:
    """Serve app until interrupted, calling on_started once it accepts connections."""
    server = _Server(
        uvicorn.Config(app, host=host, port=port, log_level="warning", access_log=False),
        on_started,
    )
    server.run()


class _Server(uvicorn.Server):
    def __init__(self, config: uvicorn.Config, on_started: Callable[[], None] | None) -> None:
        super().__init__(config)
        self._on_started = on_started

    async def startup(self, sockets=None) -> None:
        await super().startup(sockets=sockets)
        if self.started and self._on_started:
            self._on_started()


class _SecurityHeaders:
    """Adds _SECURITY_HEADERS to every HTTP response that does not set them itself."""

    def __init__(self, app: ASGIApp) -> None:
        self.app = app

    async def __call__(self, scope: Scope, receive: Receive, send: Send) -> None:
        if scope["type"] != "http":
            await self.app(scope, receive, send)
            return

        async def send_with_headers(message: Message) -> None:
            if message["type"] == "http.response.start":
                headers = MutableHeaders(scope=message)
                for name, value in _SECURITY_HEADERS.items():
                    headers.setdefault(name, value)
            await send(message)

        await self.app(scope, receive, send_with_headers)
