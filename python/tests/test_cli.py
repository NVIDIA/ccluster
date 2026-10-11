# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""The ccluster command prints its usage, and register wires an infra plane's
cluster-admin token into a CCLInfraPlane on the control plane, without a cluster."""

import base64
from types import SimpleNamespace

import pytest
from click.testing import CliRunner
from kubernetes.client import ApiException
from kubernetes.config import ConfigException

from ccluster import cli
from ccluster.cli import main

TOKEN = "infra-token"
CA = b"-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"
KUBECONFIG_SERVER = "https://127.0.0.1:6443"


def invoke(*args):
    return CliRunner().invoke(main, list(args), prog_name="ccluster")


@pytest.mark.parametrize("args", [[], ["--help"], ["-h"]])
def test_prints_usage(args):
    result = invoke(*args)
    assert result.exit_code == 0, result.output
    assert result.output.startswith("Usage: ccluster")


def test_prints_version():
    result = invoke("--version")
    assert result.exit_code == 0, result.output
    assert "version" in result.output


def test_lists_register():
    result = invoke("--help")
    assert result.exit_code == 0, result.output
    assert "register" in result.output


def test_register_describes_itself():
    result = invoke("register", "--help")
    assert result.exit_code == 0, result.output
    assert "Register a cluster with the control plane." in result.output


class InfraPlane:
    """Fakes the typed CoreV1Api and RbacAuthorizationV1Api of the infra-plane cluster.

    created records each object written, existing names the kinds whose create answers
    409, and populated says whether the token controller has filled in the token Secret.
    """

    def __init__(self):
        self.created = []
        self.existing = set()
        self.populated = True

    def _create(self, kind, body):
        if kind in self.existing:
            raise ApiException(status=409, reason="AlreadyExists")
        self.created.append((kind, body))

    def core(self, api_client):
        return SimpleNamespace(
            create_namespaced_service_account=lambda ns, body: self._create("ServiceAccount", body),
            create_namespaced_secret=lambda ns, body: self._create("Secret", body),
            read_namespaced_secret=self._read_secret,
        )

    def rbac(self, api_client):
        return SimpleNamespace(
            create_cluster_role_binding=lambda body: self._create("ClusterRoleBinding", body),
        )

    def _read_secret(self, name, namespace):
        data = None
        if self.populated:
            data = {
                "token": base64.b64encode(TOKEN.encode()).decode(),
                "ca.crt": base64.b64encode(CA).decode(),
            }
        return SimpleNamespace(data=data)


class FakeCCLClient:
    """Records what register asks of the control plane."""

    instances = []

    def __init__(self, namespace, *, api_client):
        self.namespace = namespace
        self.api_client = api_client
        self.ensured = False
        self.applied = None
        FakeCCLClient.instances.append(self)

    def ensure_namespace(self):
        self.ensured = True

    def apply_infraplane(self, **kwargs):
        self.applied = kwargs
        return SimpleNamespace(
            metadata=SimpleNamespace(name=kwargs["name"], namespace=self.namespace)
        )


@pytest.fixture
def infra(monkeypatch):
    plane = InfraPlane()
    FakeCCLClient.instances = []

    def new_client_from_config(context):
        if context not in ("cp", "ip"):
            raise ConfigException(f"Expected object with name {context}")
        return SimpleNamespace(context=context, configuration=SimpleNamespace(host=KUBECONFIG_SERVER))

    monkeypatch.setattr(cli.k8s_config, "new_client_from_config", new_client_from_config)
    monkeypatch.setattr(cli.k8s, "CoreV1Api", plane.core)
    monkeypatch.setattr(cli.k8s, "RbacAuthorizationV1Api", plane.rbac)
    monkeypatch.setattr(cli, "CCLClient", FakeCCLClient)
    monkeypatch.setattr(cli, "_TOKEN_TIMEOUT_SECONDS", 0)
    monkeypatch.setattr(cli, "_TOKEN_POLL_SECONDS", 0)
    return plane


def test_register_creates_admin_token_and_infraplane(infra):
    result = invoke(
        "register", "edge-1", "--namespace", "tenant-a",
        "--control-plane", "cp", "--infra-plane", "ip",
    )
    assert result.exit_code == 0, result.output
    assert result.output == f"Registered CCLInfraPlane tenant-a/edge-1 for {KUBECONFIG_SERVER}\n"

    kinds = {kind: body for kind, body in infra.created}
    assert set(kinds) == {"ServiceAccount", "ClusterRoleBinding", "Secret"}
    binding = kinds["ClusterRoleBinding"]
    assert binding.role_ref.name == "cluster-admin"
    assert [(s.kind, s.namespace, s.name) for s in binding.subjects] == [
        ("ServiceAccount", "kube-system", "ccluster-manager")
    ]
    secret = kinds["Secret"]
    assert secret.type == "kubernetes.io/service-account-token"
    assert secret.metadata.annotations == {"kubernetes.io/service-account.name": "ccluster-manager"}

    (client,) = FakeCCLClient.instances
    assert client.api_client.context == "cp"
    assert client.namespace == "tenant-a"
    assert client.ensured
    assert client.applied == {
        "name": "edge-1", "server": KUBECONFIG_SERVER, "bearer_token": TOKEN, "ca_cert": CA,
    }


def test_register_short_options_and_server_override(infra):
    result = invoke(
        "register", "edge-1", "-ns", "tenant-a", "-cp", "cp", "-ip", "ip",
        "--server", "https://edge-1:6443",
    )
    assert result.exit_code == 0, result.output
    assert FakeCCLClient.instances[0].applied["server"] == "https://edge-1:6443"


def test_register_again_reuses_existing_objects(infra):
    infra.existing = {"ServiceAccount", "ClusterRoleBinding", "Secret"}
    result = invoke("register", "edge-1", "-ns", "tenant-a", "-cp", "cp", "-ip", "ip")
    assert result.exit_code == 0, result.output
    assert infra.created == []
    assert FakeCCLClient.instances[0].applied["bearer_token"] == TOKEN


def test_register_fails_when_token_never_arrives(infra):
    infra.populated = False
    result = invoke("register", "edge-1", "-ns", "tenant-a", "-cp", "cp", "-ip", "ip")
    assert result.exit_code == 1
    assert "kube-system/ccluster-manager-token was not given a token" in result.output
    assert FakeCCLClient.instances == []


def test_register_reports_unknown_context(infra):
    result = invoke("register", "edge-1", "-ns", "tenant-a", "-cp", "cp", "-ip", "nope")
    assert result.exit_code == 1
    assert "Kubeconfig could not be loaded" in result.output


def test_register_reports_api_errors(infra, monkeypatch):
    def forbidden(self):
        raise ApiException(status=403, reason="Forbidden")

    monkeypatch.setattr(FakeCCLClient, "ensure_namespace", forbidden)
    result = invoke("register", "edge-1", "-ns", "tenant-a", "-cp", "cp", "-ip", "ip")
    assert result.exit_code == 1
    assert "API request failed (403): Forbidden" in result.output


@pytest.mark.parametrize("missing", ["-ns", "-cp", "-ip"])
def test_register_requires_namespace_and_both_planes(infra, missing):
    args = {"-ns": "tenant-a", "-cp": "cp", "-ip": "ip"}
    del args[missing]
    result = invoke("register", "edge-1", *[x for kv in args.items() for x in kv])
    assert result.exit_code == 2
    assert "Missing option" in result.output
