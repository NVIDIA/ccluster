# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""The ccluster command line.

main is the click group installed as the `ccluster` executable; run without a
subcommand, it prints its usage. init saves the kubeconfig context of the one control
plane the user works with, after checking it serves the ccluster API, to a small config
file; the other commands read it from there. register registers an infra-plane cluster
with that control plane: it creates a cluster-admin ServiceAccount token on the infra
plane and hands it to CCLClient.apply_infraplane on the control plane. Both clusters are
reached through kubeconfig contexts, so no credentials are stored by ccluster itself.
unregister reverses register on the control plane and leaves the infra plane alone.
dashboard serves a read-only web view of the control plane on the loopback interface; it
needs the dashboard extra, which is imported only when the command runs.
"""

from __future__ import annotations

import base64
import contextlib
import os
import time
import webbrowser
from collections.abc import Iterator
from pathlib import Path

import click
import yaml
from kubernetes import client as k8s
from kubernetes import config as k8s_config
from kubernetes.dynamic import DynamicClient
from kubernetes.dynamic.exceptions import ResourceNotFoundError

from ccluster._kube import api_error
from ccluster.client import CCLClient
from ccluster.v1alpha1 import API_VERSION, Kind


# What register creates on the infra plane. Argo CD deploys charts that install CRDs,
# namespaces and other cluster-scoped objects, so its credentials are cluster-admin, the
# same rights `argocd cluster add` grants its argocd-manager ServiceAccount.
SERVICE_ACCOUNT_NAMESPACE = "kube-system"
SERVICE_ACCOUNT_NAME = "ccluster-manager"
TOKEN_SECRET_NAME = "ccluster-manager-token"
CLUSTER_ROLE_BINDING_NAME = "ccluster-manager"
CLUSTER_ROLE_NAME = "cluster-admin"

# The config file init writes: $CCLUSTER_CONFIG, else ccluster/config.yaml under
# $XDG_CONFIG_HOME or ~/.config.
CONFIG_ENV = "CCLUSTER_CONFIG"

# How long register waits for the token controller to fill in the token Secret.
_TOKEN_TIMEOUT_SECONDS = 30.0
_TOKEN_POLL_SECONDS = 1.0

# The addresses dashboard may listen on. It acts with the user's kubeconfig credentials, so
# it must not be reachable from other machines; the dashboard also rejects requests whose
# Host header names anything else, against DNS rebinding.
_LOOPBACK_HOSTS = ("127.0.0.1", "localhost")

# The top-level modules of the dashboard extra.
_DASHBOARD_MODULES = {"jinja2", "starlette", "uvicorn"}


@click.group(
    context_settings={"help_option_names": ["-h", "--help"]},
    invoke_without_command=True,
)
@click.version_option(package_name="ccluster-py")
@click.pass_context
def main(ctx: click.Context) -> None:
    """Manage ccluster infra planes and component sets."""
    if ctx.invoked_subcommand is None:
        click.echo(ctx.get_help())


@main.command()
@click.option(
    "--control-plane",
    "-cp",
    "control_plane",
    required=True,
    help="Kubeconfig context of the control-plane cluster.",
)
def init(control_plane: str) -> None:
    """Set the control plane the other commands work with.

    Checks that the kubeconfig context exists and that its cluster serves the ccluster
    API, then saves the context name to the ccluster config file. Running it again
    replaces the saved control plane. Only the context name is saved; credentials stay in
    the kubeconfig.
    """
    with _kube_errors():
        api_client = k8s_config.new_client_from_config(context=control_plane)
        try:
            DynamicClient(api_client).resources.get(
                api_version=API_VERSION, kind=Kind.CCL_INFRA_PLANE.value
            )
        except ResourceNotFoundError as e:
            raise click.ClickException(
                f"Control plane {control_plane} does not serve {Kind.CCL_INFRA_PLANE.value}; "
                "is the ccluster-cp operator installed?"
            ) from e
    path = _save_control_plane(control_plane)
    click.echo(f"Initialized ccluster with control plane {control_plane} (saved to {path})")


@main.command()
@click.argument("name")
@click.option(
    "--namespace",
    "-n",
    required=True,
    help="Control-plane namespace for the CCLInfraPlane and its credentials Secret; "
    "created if it does not exist.",
)
@click.option(
    "--infra-plane",
    "-ip",
    "infra_plane",
    required=True,
    help="Kubeconfig context of the infra-plane cluster to register.",
)
@click.option(
    "--server",
    default=None,
    help="Infra-plane API server URL that Argo CD in the control plane connects to. "
    "Defaults to the infra-plane context's server in the kubeconfig.",
)
def register(
    name: str,
    namespace: str,
    infra_plane: str,
    server: str | None,
) -> None:
    """Register a cluster with the control plane.

    The control plane is the one saved by ccluster init. On the infra plane, creates the
    ServiceAccount kube-system/ccluster-manager, binds it to cluster-admin, and reads its
    long-lived token. On the control plane, applies the Secret NAME-credentials carrying
    that token and the cluster CA, and the CCLInfraPlane NAME pointing at it. The
    control-plane operator then writes the plane's domain, NAME.NAMESPACE.<its base domain>,
    to the infra plane's CCLClusterConfig, so NAME must be a DNS label. The control plane
    checks the CCLInfraPlane first, so a NAME it refuses creates nothing on the infra plane.
    Re-running reuses the same token.

    Pass --server when the kubeconfig's address is not reachable from the control plane,
    for example a kind cluster, whose kubeconfig points at 127.0.0.1.
    """
    control_plane = _load_control_plane()
    with _kube_errors():
        infra_api = k8s_config.new_client_from_config(context=infra_plane)
        control_api = k8s_config.new_client_from_config(context=control_plane)

        # The API server checks the plane, NAME included, before anything is created on
        # the infra plane, so a plane it would refuse leaves no cluster-admin token behind.
        client = CCLClient(namespace, api_client=control_api)
        client.ensure_namespace()
        client.check_infraplane(name=name)

        token, ca_cert = _cluster_admin_token(infra_api)
        server = server or infra_api.configuration.host

        plane = client.apply_infraplane(
            name=name, server=server, bearer_token=token, ca_cert=ca_cert
        )
    click.echo(
        f"Registered CCLInfraPlane {plane.metadata.namespace}/{plane.metadata.name} "
        f"for {server}"
    )


@main.command()
@click.argument("name")
@click.option(
    "--namespace",
    "-n",
    required=True,
    help="Control-plane namespace of the CCLInfraPlane.",
)
def unregister(name: str, namespace: str) -> None:
    """Unregister a cluster from the control plane.

    Deletes the CCLInfraPlane NAME and then its Secret NAME-credentials from the control
    plane saved by ccluster init; the operator's finalizer removes the copy it made for
    Argo CD. The namespace is left in place, and objects already gone are not an error.

    The infra plane is not touched: its ccluster-manager ServiceAccount and token stay, so
    other registrations of the same cluster keep working and registering it again reuses
    the token.
    """
    control_plane = _load_control_plane()
    with _kube_errors():
        control_api = k8s_config.new_client_from_config(context=control_plane)
        CCLClient(namespace, api_client=control_api).delete_infraplane(name=name)
    click.echo(f"Unregistered CCLInfraPlane {namespace}/{name}")


@main.command()
@click.option(
    "--namespace",
    "-n",
    default=None,
    help="Control-plane namespace to show. Defaults to the namespace of the control "
    "plane's kubeconfig context, else default.",
)
@click.option(
    "--listen",
    default=f"{_LOOPBACK_HOSTS[0]}:8080",
    show_default=True,
    callback=lambda ctx, param, value: _parse_listen(value),
    help="HOST:PORT to serve on. HOST must be 127.0.0.1 or localhost.",
)
@click.option("--no-browser", is_flag=True, help="Do not open the dashboard in a browser.")
def dashboard(namespace: str | None, listen: tuple[str, int], no_browser: bool) -> None:
    """Serve a read-only web view of the control plane.

    Shows the CCLInfraPlanes and CCLComponentSets of one namespace of the control plane
    saved by ccluster init, refreshed every few seconds, and opens it in a browser. Other
    namespaces are at /namespaces/NAMESPACE. Runs until interrupted.

    Every request to Kubernetes uses the control plane's kubeconfig credentials, so the
    dashboard shows what they may read, and anyone who can reach it acts with them. It
    therefore only listens on the loopback interface.
    """
    try:
        from ccluster import dashboard as dashboard_app
    except ImportError as e:
        if e.name not in _DASHBOARD_MODULES:
            raise
        raise click.ClickException(
            "ccluster dashboard needs the dashboard extra: pip install 'ccluster-py[dashboard]'"
        ) from e

    host, port = listen
    control_plane = _load_control_plane()
    with _kube_errors():
        api_client = k8s_config.new_client_from_config(context=control_plane)
        namespace = namespace or _context_namespace(control_plane)

    app = dashboard_app.create_app(
        dashboard_app.LocalSource(api_client),
        namespace=namespace,
        allowed_hosts=list(_LOOPBACK_HOSTS),
    )
    url = f"http://{host}:{port}/"
    click.echo(
        f"Serving the dashboard for control plane {control_plane}, namespace {namespace}, "
        f"at {url} (Ctrl+C to stop)"
    )
    dashboard_app.serve(
        app, host=host, port=port, on_started=None if no_browser else lambda: webbrowser.open(url)
    )


def _parse_listen(value: str) -> tuple[str, int]:
    host, sep, port = value.rpartition(":")
    if not sep or not port.isdigit() or int(port) > 65535:
        raise click.BadParameter(f"{value!r} is not HOST:PORT")
    if host not in _LOOPBACK_HOSTS:
        raise click.BadParameter(
            f"{host!r} is not a loopback address; use {' or '.join(_LOOPBACK_HOSTS)}"
        )
    return host, int(port)


def _context_namespace(context: str) -> str:
    """Return the namespace the kubeconfig context names, else default, as kubectl does."""
    contexts, _ = k8s_config.list_kube_config_contexts()
    for entry in contexts:
        if entry.get("name") == context:
            return (entry.get("context") or {}).get("namespace") or "default"
    return "default"


def _config_path() -> Path:
    explicit = os.environ.get(CONFIG_ENV)
    if explicit:
        return Path(explicit)
    base = os.environ.get("XDG_CONFIG_HOME") or Path.home() / ".config"
    return Path(base) / "ccluster" / "config.yaml"


def _load_control_plane() -> str:
    """Return the control plane's kubeconfig context saved by init."""
    path = _config_path()
    try:
        config = yaml.safe_load(path.read_text())
    except FileNotFoundError:
        config = None
    except yaml.YAMLError as e:
        raise click.ClickException(f"Config file {path} is not valid YAML: {e}") from e
    context = None
    if isinstance(config, dict) and isinstance(config.get("controlPlane"), dict):
        context = config["controlPlane"].get("context")
    if not context:
        raise click.ClickException("No control plane is configured; run ccluster init first")
    return context


def _save_control_plane(context: str) -> Path:
    path = _config_path()
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(yaml.safe_dump({"controlPlane": {"context": context}}))
    return path


@contextlib.contextmanager
def _kube_errors() -> Iterator[None]:
    """Turn kubeconfig and API errors into one-line click errors."""
    try:
        yield
    except k8s_config.ConfigException as e:
        raise click.ClickException(f"Kubeconfig could not be loaded: {e}") from e
    except k8s.ApiException as e:
        raise click.ClickException(api_error(e)) from e


def _cluster_admin_token(api_client: k8s.ApiClient) -> tuple[str, bytes]:
    """Create the cluster-admin ServiceAccount and its token Secret; return token and CA.

    Each object that already exists is kept, so a second registration of the same cluster
    returns the same token. The CA is the cluster's, PEM, as the token controller copies
    it into the Secret.
    """
    core = k8s.CoreV1Api(api_client)
    rbac = k8s.RbacAuthorizationV1Api(api_client)

    _create_if_absent(
        core.create_namespaced_service_account,
        SERVICE_ACCOUNT_NAMESPACE,
        k8s.V1ServiceAccount(
            metadata=k8s.V1ObjectMeta(
                name=SERVICE_ACCOUNT_NAME, namespace=SERVICE_ACCOUNT_NAMESPACE
            ),
        ),
    )
    _create_if_absent(
        rbac.create_cluster_role_binding,
        k8s.V1ClusterRoleBinding(
            metadata=k8s.V1ObjectMeta(name=CLUSTER_ROLE_BINDING_NAME),
            role_ref=k8s.V1RoleRef(
                api_group="rbac.authorization.k8s.io",
                kind="ClusterRole",
                name=CLUSTER_ROLE_NAME,
            ),
            subjects=[
                k8s.RbacV1Subject(
                    kind="ServiceAccount",
                    name=SERVICE_ACCOUNT_NAME,
                    namespace=SERVICE_ACCOUNT_NAMESPACE,
                )
            ],
        ),
    )
    # ServiceAccounts no longer get a token Secret of their own, but the token
    # controller still fills in one created with this type and annotation.
    _create_if_absent(
        core.create_namespaced_secret,
        SERVICE_ACCOUNT_NAMESPACE,
        k8s.V1Secret(
            metadata=k8s.V1ObjectMeta(
                name=TOKEN_SECRET_NAME,
                namespace=SERVICE_ACCOUNT_NAMESPACE,
                annotations={"kubernetes.io/service-account.name": SERVICE_ACCOUNT_NAME},
            ),
            type="kubernetes.io/service-account-token",
        ),
    )

    deadline = time.monotonic() + _TOKEN_TIMEOUT_SECONDS
    while True:
        secret = core.read_namespaced_secret(TOKEN_SECRET_NAME, SERVICE_ACCOUNT_NAMESPACE)
        data = secret.data or {}
        if data.get("token") and data.get("ca.crt"):
            return (
                base64.b64decode(data["token"]).decode(),
                base64.b64decode(data["ca.crt"]),
            )
        if time.monotonic() >= deadline:
            raise click.ClickException(
                f"Secret {SERVICE_ACCOUNT_NAMESPACE}/{TOKEN_SECRET_NAME} was not given a "
                f"token within {_TOKEN_TIMEOUT_SECONDS:g}s"
            )
        time.sleep(_TOKEN_POLL_SECONDS)


def _create_if_absent(create, *args) -> None:
    """Call a typed client's create method, treating AlreadyExists as success."""
    try:
        create(*args)
    except k8s.ApiException as e:
        if e.status != 409:
            raise
