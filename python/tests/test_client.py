# Copyright 2026 NVIDIA CORPORATION & AFFILIATES.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""CCLClient writes the Secret and CCLInfraPlane the operator expects, without a cluster."""

import base64
import json
from types import SimpleNamespace

import pytest
from kubernetes.dynamic.exceptions import NotFoundError

from ccluster import client as client_module
from ccluster.client import CCLClient
from ccluster.v1alpha1 import CCLInfraPlane

CA = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"


class FakeResource:
    def __init__(self, kind):
        self.kind = kind


class FakeDynamicClient:
    """Records server_side_apply and delete calls and echoes applied bodies back."""

    def __init__(self, api_client):
        self.api_client = api_client
        self.applied = []
        self.deleted = []
        self.missing = set()
        self.resources = SimpleNamespace(
            get=lambda api_version, kind: FakeResource(kind)
        )

    def server_side_apply(self, resource, body, namespace, **kwargs):
        self.applied.append((resource.kind, namespace, body, kwargs))
        return SimpleNamespace(
            to_dict=lambda: {**body, "metadata": {**body["metadata"], "uid": "1"}}
        )

    def delete(self, resource, name, namespace):
        if (resource.kind, name) in self.missing:
            raise NotFoundError(
                SimpleNamespace(status=404, reason="NotFound", body=b"{}", headers={})
            )
        self.deleted.append((resource.kind, name, namespace))


@pytest.fixture
def fake(monkeypatch):
    holder = {}

    def factory(api_client):
        holder["client"] = FakeDynamicClient(api_client)
        return holder["client"]

    monkeypatch.setattr(client_module, "DynamicClient", factory)
    return holder


def make_client(fake, namespace="tenant"):
    c = CCLClient(namespace=namespace, api_client=object())
    return c, fake["client"]


def test_register_cluster_applies_secret_then_infra_plane(fake):
    c, dyn = make_client(fake)

    plane = c.register_cluster("edge-1", "https://10.0.0.1:6443", "tok", CA)

    kinds = [kind for kind, *_ in dyn.applied]
    assert kinds == ["Secret", "CCLInfraPlane"]
    for _, namespace, body, kwargs in dyn.applied:
        assert namespace == "tenant"
        assert body["metadata"]["namespace"] == "tenant"
        assert kwargs == {"field_manager": "ccluster-py", "force_conflicts": True}

    secret = dyn.applied[0][2]
    assert secret["metadata"]["name"] == "edge-1-credentials"
    assert secret["type"] == "Opaque"
    assert secret["stringData"]["server"] == "https://10.0.0.1:6443"
    assert set(secret["stringData"]) == {"server", "config"}
    assert json.loads(secret["stringData"]["config"]) == {
        "bearerToken": "tok",
        "tlsClientConfig": {
            "insecure": False,
            "caData": base64.b64encode(CA.encode()).decode(),
        },
    }

    infra_plane = dyn.applied[1][2]
    assert infra_plane["apiVersion"] == "ccluster.nvidia.com/v1alpha1"
    assert infra_plane["kind"] == "CCLInfraPlane"
    assert infra_plane["metadata"] == {"name": "edge-1", "namespace": "tenant"}
    assert infra_plane["spec"] == {
        "clusterCredentialsRef": {"name": "edge-1-credentials"}
    }
    assert "status" not in infra_plane

    assert isinstance(plane, CCLInfraPlane)
    assert plane.metadata.name == "edge-1"
    assert plane.spec.cluster_credentials_ref.name == "edge-1-credentials"


def test_register_cluster_accepts_pem_bytes(fake):
    c, dyn = make_client(fake)
    c.register_cluster("edge-1", "https://10.0.0.1:6443", "tok", CA.encode())
    config = json.loads(dyn.applied[0][2]["stringData"]["config"])
    assert config["tlsClientConfig"]["caData"] == base64.b64encode(CA.encode()).decode()


def test_unregister_cluster_deletes_infra_plane_before_secret(fake):
    c, dyn = make_client(fake)
    c.unregister_cluster("edge-1")
    assert dyn.deleted == [
        ("CCLInfraPlane", "edge-1", "tenant"),
        ("Secret", "edge-1-credentials", "tenant"),
    ]


def test_unregister_cluster_ignores_missing_objects(fake):
    c, dyn = make_client(fake)
    dyn.missing = {("CCLInfraPlane", "edge-1"), ("Secret", "edge-1-credentials")}
    c.unregister_cluster("edge-1")
    assert dyn.deleted == []


def test_defaults_to_in_cluster_config_and_namespace(fake, monkeypatch, tmp_path):
    loaded = []
    monkeypatch.setattr(
        client_module.config, "load_incluster_config", lambda: loaded.append(True)
    )
    monkeypatch.setattr(client_module.k8s, "ApiClient", lambda: "in-cluster-api-client")
    ns_file = tmp_path / "namespace"
    ns_file.write_text("pod-namespace\n")
    monkeypatch.setattr(client_module, "SERVICE_ACCOUNT_NAMESPACE", ns_file)

    c = CCLClient()

    assert loaded == [True]
    assert c.namespace == "pod-namespace"
    assert fake["client"].api_client == "in-cluster-api-client"


def test_namespace_required_outside_a_cluster(fake, monkeypatch, tmp_path):
    monkeypatch.setattr(client_module, "SERVICE_ACCOUNT_NAMESPACE", tmp_path / "absent")
    with pytest.raises(ValueError, match="namespace is required"):
        CCLClient(api_client=object())
