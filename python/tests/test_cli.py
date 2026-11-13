# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""The ccluster command prints its usage, init saves the control plane, and register wires
an infra plane's cluster-admin token into a CCLInfraPlane on it, without a cluster."""

import base64
from types import SimpleNamespace

import pytest
import yaml
from click.testing import CliRunner
from kubernetes.client import ApiException
from kubernetes.config import ConfigException
from kubernetes.dynamic.exceptions import ResourceNotFoundError

from ccluster import cli
from ccluster.cli import main
from ccluster.v1alpha1 import CCLInfraPlane

TOKEN = "infra-token"
CA = b"-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"
KUBECONFIG_SERVER = "https://127.0.0.1:6443"

# ClusterConfigApplied as the control-plane operator reports it.
APPLIED = {
    "type": "ClusterConfigApplied",
    "status": "True",
    "reason": "Applied",
    "message": 'CCLClusterConfig "cluster" on the infra-plane cluster has the domain',
    "lastTransitionTime": "2026-01-01T00:00:00Z",
}
UNREACHABLE = {
    "type": "ClusterConfigApplied",
    "status": "False",
    "reason": "ClusterUnreachable",
    "message": f'failed to get server groups: Get "{KUBECONFIG_SERVER}/api": connection refused',
    "lastTransitionTime": "2026-01-01T00:00:00Z",
}


def invoke(*args):
    return CliRunner().invoke(main, list(args), prog_name="ccluster")


@pytest.fixture(autouse=True)
def config_file(tmp_path, monkeypatch):
    """Point every test at its own config file, which does not exist yet."""
    path = tmp_path / "ccluster" / "config.yaml"
    monkeypatch.setenv("CCLUSTER_CONFIG", str(path))
    return path


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
    """Records what register asks of the control plane, and plays its operator.

    reports holds the ClusterConfigApplied conditions get_infraplane returns, one per call,
    the last one from then on; None is a plane with no such condition yet.
    """

    instances = []
    reports = [APPLIED]

    def __init__(self, namespace, *, api_client):
        self.namespace = namespace
        self.api_client = api_client
        self.ensured = False
        self.checked = None
        self.applied = None
        self.deleted = None
        FakeCCLClient.instances.append(self)

    def ensure_namespace(self):
        self.ensured = True

    def check_infraplane(self, *, name):
        self.checked = name

    def delete_infraplane(self, *, name):
        self.deleted = name

    def apply_infraplane(self, **kwargs):
        self.applied = kwargs
        return SimpleNamespace(
            metadata=SimpleNamespace(name=kwargs["name"], namespace=self.namespace)
        )

    def get_infraplane(self, *, name):
        reports = FakeCCLClient.reports
        condition = reports.pop(0) if len(reports) > 1 else reports[0]
        return CCLInfraPlane.model_validate({
            "metadata": {"name": name, "namespace": self.namespace},
            "spec": {"clusterCredentialsRef": {"name": f"{name}-credentials"}},
            "status": {
                "domain": f"{name}.{self.namespace}.example.com",
                "conditions": [condition] if condition else None,
            },
        })


def new_client_from_config(context):
    """Fakes kubeconfig loading: only the contexts cp and ip exist."""
    if context not in ("cp", "ip"):
        raise ConfigException(f"Expected object with name {context}")
    return SimpleNamespace(context=context, configuration=SimpleNamespace(host=KUBECONFIG_SERVER))


class FakeDynamicClient:
    """Fakes API discovery: CCLInfraPlane is served only where serves_ccluster is set."""

    serves_ccluster = True

    def __init__(self, api_client):
        self.resources = SimpleNamespace(get=self._get)

    def _get(self, api_version, kind):
        if not FakeDynamicClient.serves_ccluster:
            raise ResourceNotFoundError(f"No matches found for {api_version}/{kind}")
        return SimpleNamespace(kind=kind)


@pytest.fixture
def kubeconfig(monkeypatch):
    FakeDynamicClient.serves_ccluster = True
    monkeypatch.setattr(cli.k8s_config, "new_client_from_config", new_client_from_config)
    monkeypatch.setattr(cli, "DynamicClient", FakeDynamicClient)


def test_init_saves_the_control_plane(kubeconfig, config_file):
    result = invoke("init", "--control-plane", "cp")
    assert result.exit_code == 0, result.output
    assert result.output == f"Initialized ccluster with control plane cp (saved to {config_file})\n"
    assert yaml.safe_load(config_file.read_text()) == {"controlPlane": {"context": "cp"}}


def test_init_again_replaces_the_control_plane(kubeconfig, config_file):
    assert invoke("init", "-cp", "cp").exit_code == 0
    assert invoke("init", "-cp", "ip").exit_code == 0
    assert yaml.safe_load(config_file.read_text()) == {"controlPlane": {"context": "ip"}}


def test_init_rejects_unknown_context(kubeconfig, config_file):
    result = invoke("init", "-cp", "nope")
    assert result.exit_code == 1
    assert "Kubeconfig could not be loaded" in result.output
    assert not config_file.exists()


def test_init_rejects_cluster_without_ccluster_api(kubeconfig, config_file):
    FakeDynamicClient.serves_ccluster = False
    result = invoke("init", "-cp", "cp")
    assert result.exit_code == 1
    assert "Control plane cp does not serve CCLInfraPlane" in result.output
    assert not config_file.exists()


def test_init_requires_control_plane(kubeconfig):
    result = invoke("init")
    assert result.exit_code == 2
    assert "Missing option" in result.output


def test_config_path_follows_xdg_config_home(tmp_path, monkeypatch):
    monkeypatch.delenv("CCLUSTER_CONFIG")
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path))
    assert cli._config_path() == tmp_path / "ccluster" / "config.yaml"


@pytest.fixture
def infra(kubeconfig, monkeypatch, config_file):
    """An infra plane to register, with init already run against the control plane cp."""
    plane = InfraPlane()
    FakeCCLClient.instances = []
    FakeCCLClient.reports = [APPLIED]
    config_file.parent.mkdir(parents=True)
    config_file.write_text("controlPlane:\n  context: cp\n")
    monkeypatch.setattr(cli.k8s, "CoreV1Api", plane.core)
    monkeypatch.setattr(cli.k8s, "RbacAuthorizationV1Api", plane.rbac)
    monkeypatch.setattr(cli, "CCLClient", FakeCCLClient)
    monkeypatch.setattr(cli, "_TOKEN_TIMEOUT_SECONDS", 0)
    monkeypatch.setattr(cli, "_TOKEN_POLL_SECONDS", 0)
    monkeypatch.setattr(cli, "_CLUSTER_CONFIG_TIMEOUT_SECONDS", 10)
    monkeypatch.setattr(cli, "_CLUSTER_CONFIG_POLL_SECONDS", 0)
    return plane


def test_register_creates_admin_token_and_infraplane(infra):
    result = invoke(
        "register", "edge-1", "--namespace", "tenant-a", "--infra-plane", "ip",
    )
    assert result.exit_code == 0, result.output
    assert result.output == (
        f"Applied CCLInfraPlane tenant-a/edge-1 for {KUBECONFIG_SERVER}; "
        "waiting for the control plane to reach the cluster\n"
        "Registered CCLInfraPlane tenant-a/edge-1; its domain is edge-1.tenant-a.example.com\n"
    )

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
    assert client.checked == "edge-1"
    assert client.applied == {
        "name": "edge-1", "server": KUBECONFIG_SERVER,
        "bearer_token": TOKEN, "ca_cert": CA,
    }


def test_register_short_options_and_server_override(infra):
    result = invoke(
        "register", "edge-1", "-n", "tenant-a", "-ip", "ip",
        "--server", "https://edge-1:6443",
    )
    assert result.exit_code == 0, result.output
    assert FakeCCLClient.instances[0].applied["server"] == "https://edge-1:6443"


def test_register_again_reuses_existing_objects(infra):
    infra.existing = {"ServiceAccount", "ClusterRoleBinding", "Secret"}
    result = invoke("register", "edge-1", "-n", "tenant-a", "-ip", "ip")
    assert result.exit_code == 0, result.output
    assert infra.created == []
    assert FakeCCLClient.instances[0].applied["bearer_token"] == TOKEN


def test_register_fails_with_the_reason_the_cluster_was_not_reached(infra, monkeypatch):
    FakeCCLClient.reports = [UNREACHABLE]
    monkeypatch.setattr(cli, "_CLUSTER_CONFIG_TIMEOUT_SECONDS", 0)
    result = invoke("register", "edge-1", "-n", "tenant-a", "-ip", "ip")
    assert result.exit_code == 1
    assert (
        "CCLInfraPlane tenant-a/edge-1 is applied, but after 0s ClusterConfigApplied is False "
        f"(ClusterUnreachable): {UNREACHABLE['message']}; if the control plane cannot reach "
        f"{KUBECONFIG_SERVER}, run register again with --server"
    ) in result.output


def test_register_fails_without_the_operator(infra, monkeypatch):
    FakeCCLClient.reports = [None]
    monkeypatch.setattr(cli, "_CLUSTER_CONFIG_TIMEOUT_SECONDS", 0)
    result = invoke("register", "edge-1", "-n", "tenant-a", "-ip", "ip")
    assert result.exit_code == 1
    assert (
        "CCLInfraPlane tenant-a/edge-1 is applied, but after 0s it has no ClusterConfigApplied "
        "condition; is the ccluster-cp operator running?"
    ) in result.output


def test_register_again_waits_past_the_old_servers_condition(infra):
    # Registering again with a server that works must not fail on the False the old server
    # left: the operator tries the new one only after register has applied it.
    FakeCCLClient.reports = [UNREACHABLE, UNREACHABLE, APPLIED]
    result = invoke("register", "edge-1", "-n", "tenant-a", "-ip", "ip", "--server", "https://edge-1:6443")
    assert result.exit_code == 0, result.output
    assert result.output.endswith(
        "Registered CCLInfraPlane tenant-a/edge-1; its domain is edge-1.tenant-a.example.com\n"
    )


def test_register_fails_when_token_never_arrives(infra):
    infra.populated = False
    result = invoke("register", "edge-1", "-n", "tenant-a", "-ip", "ip")
    assert result.exit_code == 1
    assert "kube-system/ccluster-manager-token was not given a token" in result.output
    assert FakeCCLClient.instances[0].applied is None


def test_register_creates_nothing_on_the_infra_plane_for_a_refused_plane(infra, monkeypatch):
    # The API server refuses a NAME that is not a DNS label; that must fail register
    # before the cluster-admin ServiceAccount, binding and token exist.
    def invalid(self, *, name):
        raise ApiException(status=422, reason="Invalid")

    monkeypatch.setattr(FakeCCLClient, "check_infraplane", invalid)
    result = invoke("register", "edge.1", "-n", "tenant-a", "-ip", "ip")
    assert result.exit_code == 1
    assert "API request failed (422): Invalid" in result.output
    assert infra.created == []
    assert FakeCCLClient.instances[0].applied is None


def test_register_reports_unknown_context(infra):
    result = invoke("register", "edge-1", "-n", "tenant-a", "-ip", "nope")
    assert result.exit_code == 1
    assert "Kubeconfig could not be loaded" in result.output


def test_register_needs_init(infra, config_file):
    config_file.unlink()
    result = invoke("register", "edge-1", "-n", "tenant-a", "-ip", "ip")
    assert result.exit_code == 1
    assert "No control plane is configured; run ccluster init first" in result.output
    assert infra.created == []


def test_register_rejects_invalid_config(infra, config_file):
    config_file.write_text("controlPlane: [")
    result = invoke("register", "edge-1", "-n", "tenant-a", "-ip", "ip")
    assert result.exit_code == 1
    assert "is not valid YAML" in result.output


def test_register_has_no_domain_option(infra):
    # The operator derives the domain from the plane's name and namespace.
    result = invoke("register", "edge-1", "-n", "tenant-a", "-ip", "ip", "--domain", "x.example.com")
    assert result.exit_code == 2
    assert "No such option '--domain'" in result.output
    assert infra.created == []


def test_register_reports_api_errors(infra, monkeypatch):
    def forbidden(self):
        raise ApiException(status=403, reason="Forbidden")

    monkeypatch.setattr(FakeCCLClient, "ensure_namespace", forbidden)
    result = invoke("register", "edge-1", "-n", "tenant-a", "-ip", "ip")
    assert result.exit_code == 1
    assert "API request failed (403): Forbidden" in result.output


@pytest.mark.parametrize("missing", ["-n", "-ip"])
def test_register_requires_namespace_and_infra_plane(infra, missing):
    args = {"-n": "tenant-a", "-ip": "ip"}
    del args[missing]
    result = invoke("register", "edge-1", *[x for kv in args.items() for x in kv])
    assert result.exit_code == 2
    assert "Missing option" in result.output


def test_unregister_deletes_the_infraplane(infra):
    result = invoke("unregister", "edge-1", "--namespace", "tenant-a")
    assert result.exit_code == 0, result.output
    assert result.output == "Unregistered CCLInfraPlane tenant-a/edge-1\n"
    (client,) = FakeCCLClient.instances
    assert client.api_client.context == "cp"
    assert client.namespace == "tenant-a"
    assert client.deleted == "edge-1"
    assert infra.created == []


def test_unregister_has_no_infra_plane_option(infra):
    result = invoke("unregister", "edge-1", "-n", "tenant-a", "-ip", "ip")
    assert result.exit_code == 2
    assert FakeCCLClient.instances == []


def test_unregister_needs_init(infra, config_file):
    config_file.unlink()
    result = invoke("unregister", "edge-1", "-n", "tenant-a")
    assert result.exit_code == 1
    assert "run ccluster init first" in result.output


def test_unregister_requires_namespace(infra):
    result = invoke("unregister", "edge-1")
    assert result.exit_code == 2
    assert "Missing option" in result.output
