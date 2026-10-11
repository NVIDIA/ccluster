# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""The ccluster command line.

main is the click group installed as the `ccluster` executable; run without a
subcommand, it prints its usage. register registers an infra-plane cluster with the
control plane: it creates a cluster-admin ServiceAccount token on the infra plane and
hands it to CCLClient.apply_infraplane on the control plane. Both clusters are reached
through kubeconfig contexts.
"""

from __future__ import annotations

import base64
import json
import time

import click
from kubernetes import client as k8s
from kubernetes import config as k8s_config

from ccluster.client import CCLClient


# What register creates on the infra plane. Argo CD deploys charts that install CRDs,
# namespaces and other cluster-scoped objects, so its credentials are cluster-admin, the
# same rights `argocd cluster add` grants its argocd-manager ServiceAccount.
SERVICE_ACCOUNT_NAMESPACE = "kube-system"
SERVICE_ACCOUNT_NAME = "ccluster-manager"
TOKEN_SECRET_NAME = "ccluster-manager-token"
CLUSTER_ROLE_BINDING_NAME = "ccluster-manager"
CLUSTER_ROLE_NAME = "cluster-admin"

# How long register waits for the token controller to fill in the token Secret.
_TOKEN_TIMEOUT_SECONDS = 30.0
_TOKEN_POLL_SECONDS = 1.0


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
@click.argument("name")
@click.option(
    "--namespace",
    "-ns",
    required=True,
    help="Control-plane namespace for the CCLInfraPlane and its credentials Secret; "
    "created if it does not exist.",
)
@click.option(
    "--control-plane",
    "-cp",
    "control_plane",
    required=True,
    help="Kubeconfig context of the control-plane cluster.",
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
    control_plane: str,
    infra_plane: str,
    server: str | None,
) -> None:
    """Register a cluster with the control plane.

    On the infra plane, creates the ServiceAccount kube-system/ccluster-manager, binds it
    to cluster-admin, and reads its long-lived token. On the control plane, applies the
    Secret NAME-credentials carrying that token and the cluster CA, and the CCLInfraPlane
    NAME pointing at it. Re-running reuses the same token.

    Pass --server when the kubeconfig's address is not reachable from the control plane,
    for example a kind cluster, whose kubeconfig points at 127.0.0.1.
    """
    try:
        infra_api = k8s_config.new_client_from_config(context=infra_plane)
        control_api = k8s_config.new_client_from_config(context=control_plane)

        token, ca_cert = _cluster_admin_token(infra_api)
        server = server or infra_api.configuration.host

        client = CCLClient(namespace, api_client=control_api)
        client.ensure_namespace()
        plane = client.apply_infraplane(
            name=name, server=server, bearer_token=token, ca_cert=ca_cert
        )
    except k8s_config.ConfigException as e:
        raise click.ClickException(f"Kubeconfig could not be loaded: {e}") from e
    except k8s.ApiException as e:
        raise click.ClickException(_api_error(e)) from e
    click.echo(
        f"Registered CCLInfraPlane {plane.metadata.namespace}/{plane.metadata.name} "
        f"for {server}"
    )


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


def _api_error(e: k8s.ApiException) -> str:
    """A one-line description of a failed API request, with the server's message if any."""
    message = e.reason
    try:
        message = json.loads(e.body)["message"]
    except (TypeError, ValueError, KeyError):
        pass
    return f"API request failed ({e.status}): {message}"
