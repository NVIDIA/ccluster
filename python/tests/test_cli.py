# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""The ccluster command prints its usage, init saves the control plane, and register wires
an infra plane's cluster-admin token into a CCLInfraPlane on it, without a cluster."""

import base64
import sys
from types import SimpleNamespace

import pytest
import yaml
from click.testing import CliRunner
from kubernetes.client import ApiException
from kubernetes.config import ConfigException
from kubernetes.dynamic.exceptions import ResourceNotFoundError

import ccluster
from ccluster import cli
from ccluster.cli import main

TOKEN = "infra-token"
CA = b"-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"
KUBECONFIG_SERVER = "https://127.0.0.1:6443"


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
    """Records what register asks of the control plane."""

    instances = []

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
    config_file.parent.mkdir(parents=True)
    config_file.write_text("controlPlane:\n  context: cp\n")
    monkeypatch.setattr(cli.k8s, "CoreV1Api", plane.core)
    monkeypatch.setattr(cli.k8s, "RbacAuthorizationV1Api", plane.rbac)
    monkeypatch.setattr(cli, "CCLClient", FakeCCLClient)
    monkeypatch.setattr(cli, "_TOKEN_TIMEOUT_SECONDS", 0)
    monkeypatch.setattr(cli, "_TOKEN_POLL_SECONDS", 0)
    return plane


def test_register_creates_admin_token_and_infraplane(infra):
    result = invoke(
        "register", "edge-1", "--namespace", "tenant-a", "--infra-plane", "ip",
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


class FakeDashboard:
    """Stands in for ccluster.dashboard: records the app it was asked to build and serve."""

    def __init__(self):
        self.app = None
        self.served = None

    def LocalSource(self, api_client):
        return SimpleNamespace(api_client=api_client)

    def create_app(self, source, *, namespace, allowed_hosts):
        self.app = SimpleNamespace(source=source, namespace=namespace, allowed_hosts=allowed_hosts)
        return self.app

    def serve(self, app, *, host, port, on_started):
        self.served = SimpleNamespace(app=app, host=host, port=port, on_started=on_started)


@pytest.fixture
def dashboard(kubeconfig, monkeypatch, config_file):
    """ccluster dashboard with init already run against the control plane cp, whose
    kubeconfig context names the namespace tenant-a."""
    fake = FakeDashboard()
    config_file.parent.mkdir(parents=True)
    config_file.write_text("controlPlane:\n  context: cp\n")
    monkeypatch.setitem(sys.modules, "ccluster.dashboard", fake)
    monkeypatch.setattr(ccluster, "dashboard", fake, raising=False)
    monkeypatch.setattr(
        cli.k8s_config,
        "list_kube_config_contexts",
        lambda: ([{"name": "cp", "context": {"namespace": "tenant-a"}}, {"name": "ip"}], None),
    )
    opened = []
    monkeypatch.setattr(cli.webbrowser, "open", opened.append)
    fake.opened = opened
    return fake


def test_dashboard_serves_the_contexts_namespace_on_loopback(dashboard):
    result = invoke("dashboard")
    assert result.exit_code == 0, result.output
    assert "namespace tenant-a, at http://127.0.0.1:8080/" in result.output
    assert dashboard.app.source.api_client.context == "cp"
    assert dashboard.app.namespace == "tenant-a"
    assert dashboard.app.allowed_hosts == ["127.0.0.1", "localhost"]
    assert (dashboard.served.host, dashboard.served.port) == ("127.0.0.1", 8080)

    dashboard.served.on_started()
    assert dashboard.opened == ["http://127.0.0.1:8080/"]


def test_dashboard_namespace_and_listen_options(dashboard):
    result = invoke("dashboard", "-n", "tenant-b", "--listen", "localhost:9000", "--no-browser")
    assert result.exit_code == 0, result.output
    assert dashboard.app.namespace == "tenant-b"
    assert (dashboard.served.host, dashboard.served.port) == ("localhost", 9000)
    assert dashboard.served.on_started is None


def test_dashboard_defaults_to_the_default_namespace(dashboard, monkeypatch):
    monkeypatch.setattr(
        cli.k8s_config, "list_kube_config_contexts", lambda: ([{"name": "cp", "context": {}}], None)
    )
    assert invoke("dashboard").exit_code == 0
    assert dashboard.app.namespace == "default"


@pytest.mark.parametrize(
    "listen, error",
    [
        ("0.0.0.0:8080", "'0.0.0.0' is not a loopback address"),
        ("example.com:8080", "is not a loopback address"),
        ("127.0.0.1", "is not HOST:PORT"),
        ("127.0.0.1:http", "is not HOST:PORT"),
        ("127.0.0.1:70000", "is not HOST:PORT"),
    ],
)
def test_dashboard_listens_only_on_loopback(dashboard, listen, error):
    result = invoke("dashboard", "--listen", listen)
    assert result.exit_code == 2
    assert error in result.output
    assert dashboard.served is None


def test_dashboard_needs_init(dashboard, config_file):
    config_file.unlink()
    result = invoke("dashboard")
    assert result.exit_code == 1
    assert "run ccluster init first" in result.output
    assert dashboard.served is None


def test_dashboard_without_the_extra_says_how_to_install_it(dashboard, monkeypatch):
    # Importing the real package now fails on its first third-party import.
    monkeypatch.delitem(sys.modules, "ccluster.dashboard")
    monkeypatch.delattr(ccluster, "dashboard")
    monkeypatch.setitem(sys.modules, "uvicorn", None)

    result = invoke("dashboard")

    assert result.exit_code == 1
    assert "pip install 'ccluster-py[dashboard]'" in result.output
