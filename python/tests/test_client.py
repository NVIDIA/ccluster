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

"""CCLClient writes the Secret, CCLInfraPlane and CCLComponentSet the operator expects, without a cluster."""

import base64
import json
from types import SimpleNamespace

import pytest
from kubernetes import client as k8s
from kubernetes.dynamic.exceptions import NotFoundError
from pydantic import ValidationError

from ccluster import catalogs
from ccluster import client as client_module
from ccluster.catalogs.cluster_base import argo_rollouts, keda
from ccluster.client import CCLClient
from ccluster.v1alpha1 import CCLComponentSet, CCLInfraPlane, Kind

CA = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"


class FakeResource:
    def __init__(self, kind):
        self.kind = kind


class FakeDynamicClient:
    """Records server_side_apply and delete calls and echoes applied bodies back."""

    def __init__(self, api_client):
        self.api_client = api_client
        self.namespaces = set()
        self.created = []
        self.applied = []
        self.deleted = []
        self.missing = set()
        self.resources = SimpleNamespace(
            get=lambda api_version, kind: FakeResource(kind)
        )

    def get(self, resource, name):
        if resource.kind == "Namespace" and name not in self.namespaces:
            raise NotFoundError(
                SimpleNamespace(status=404, reason="NotFound", body=b"{}", headers={})
            )
        return SimpleNamespace(to_dict=lambda: {"metadata": {"name": name}})

    def create(self, resource, body):
        self.created.append((resource.kind, body))
        if resource.kind == "Namespace":
            self.namespaces.add(body["metadata"]["name"])

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
    c = CCLClient(namespace, api_client=k8s.ApiClient())
    return c, fake["client"]


def test_constructing_creates_a_missing_namespace(fake):
    _, dyn = make_client(fake)
    assert dyn.created == [
        (
            "Namespace",
            {"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": "tenant"}},
        )
    ]


def test_constructing_leaves_an_existing_namespace_alone(fake, monkeypatch):
    original = FakeDynamicClient.__init__

    def with_tenant(self, api_client):
        original(self, api_client)
        self.namespaces.add("tenant")

    monkeypatch.setattr(FakeDynamicClient, "__init__", with_tenant)
    _, dyn = make_client(fake)
    assert dyn.created == []


def test_register_infraplane_applies_secret_then_infra_plane(fake):
    c, dyn = make_client(fake)

    plane = c.register_infraplane("edge-1", "https://10.0.0.1:6443", "tok", CA)

    kinds = [kind for kind, *_ in dyn.applied]
    assert kinds == ["Secret", Kind.CCL_INFRA_PLANE]
    for _, ns_arg, _, kwargs in dyn.applied:
        assert ns_arg == "tenant"
        assert kwargs == {"field_manager": "ccluster-py", "force_conflicts": True}

    secret = dyn.applied[0][2]
    assert secret["apiVersion"] == "v1"
    assert secret["kind"] == "Secret"
    assert secret["metadata"] == {"name": "edge-1-credentials", "namespace": "tenant"}
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
    assert infra_plane["kind"] == Kind.CCL_INFRA_PLANE
    assert infra_plane["metadata"] == {"name": "edge-1", "namespace": "tenant"}
    assert infra_plane["spec"] == {
        "clusterCredentialsRef": {"name": "edge-1-credentials"}
    }
    assert "status" not in infra_plane

    # The client hands back the applied object as the server holds it, typed.
    assert isinstance(plane, CCLInfraPlane)
    assert plane.metadata.name == "edge-1"
    assert plane.spec.cluster_credentials_ref.name == "edge-1-credentials"


def test_register_infraplane_defaults_to_in_cluster_credential_free(fake):
    c, dyn = make_client(fake)

    c.register_infraplane("edge-1")

    secret = dyn.applied[0][2]
    assert secret["stringData"]["server"] == "https://kubernetes.default.svc"
    assert json.loads(secret["stringData"]["config"]) == {
        "tlsClientConfig": {"insecure": False}
    }


def test_register_infraplane_accepts_pem_bytes(fake):
    c, dyn = make_client(fake)
    c.register_infraplane("edge-1", "https://10.0.0.1:6443", "tok", CA.encode())
    config = json.loads(dyn.applied[0][2]["stringData"]["config"])
    assert config["tlsClientConfig"]["caData"] == base64.b64encode(CA.encode()).decode()


def test_unregister_infraplane_deletes_infra_plane_before_secret(fake):
    c, dyn = make_client(fake)
    c.unregister_infraplane("edge-1")
    assert dyn.deleted == [
        (Kind.CCL_INFRA_PLANE, "edge-1", "tenant"),
        ("Secret", "edge-1-credentials", "tenant"),
    ]


def test_unregister_infraplane_ignores_missing_objects(fake):
    c, dyn = make_client(fake)
    dyn.missing = {(Kind.CCL_INFRA_PLANE, "edge-1"), ("Secret", "edge-1-credentials")}
    c.unregister_infraplane("edge-1")
    assert dyn.deleted == []


def test_create_component_set_applies_the_set(fake):
    c, dyn = make_client(fake)
    components = [
        catalogs.component(
            "argo-rollouts", "edge-1", argo_rollouts.CHART, "argo-rollouts"
        ),
        catalogs.component(
            "keda",
            "edge-1",
            keda.CHART,
            "keda",
            keda.Values(keda=keda.ChartValues(operator=keda.Replicas(replica_count=1))),
        ),
    ]

    component_set = c.create_component_set("local-base", components)

    assert [kind for kind, *_ in dyn.applied] == [Kind.CCL_COMPONENT_SET]
    _, ns_arg, body, kwargs = dyn.applied[0]
    assert ns_arg == "tenant"
    assert kwargs == {"field_manager": "ccluster-py", "force_conflicts": True}
    assert body["apiVersion"] == "ccluster.nvidia.com/v1alpha1"
    assert body["kind"] == Kind.CCL_COMPONENT_SET
    assert body["metadata"] == {"name": "local-base", "namespace": "tenant"}
    assert body["spec"] == {
        "components": [
            {
                "name": "argo-rollouts",
                "infraPlaneRef": {"name": "edge-1"},
                "chart": argo_rollouts.CHART,
                "namespace": "argo-rollouts",
            },
            {
                "name": "keda",
                "infraPlaneRef": {"name": "edge-1"},
                "chart": keda.CHART,
                "namespace": "keda",
                "values": {"keda": {"operator": {"replicaCount": 1}}},
            },
        ]
    }
    assert "status" not in body

    assert isinstance(component_set, CCLComponentSet)
    assert component_set.metadata.name == "local-base"
    assert [x.name for x in component_set.spec.components] == ["argo-rollouts", "keda"]
    assert component_set.spec.components[1].infra_plane_ref.name == "edge-1"


def test_create_component_set_rejects_empty_components(fake):
    c, dyn = make_client(fake)
    # minItems: 1 comes from the CRD, so Pydantic rejects it before any request is made.
    with pytest.raises(ValidationError):
        c.create_component_set("local-base", [])
    assert dyn.applied == []


def test_delete_component_set_deletes_the_set(fake):
    c, dyn = make_client(fake)
    c.delete_component_set("local-base")
    assert dyn.deleted == [(Kind.CCL_COMPONENT_SET, "local-base", "tenant")]


def test_delete_component_set_ignores_a_missing_set(fake):
    c, dyn = make_client(fake)
    dyn.missing = {(Kind.CCL_COMPONENT_SET, "local-base")}
    c.delete_component_set("local-base")
    assert dyn.deleted == []


def test_defaults_to_in_cluster_config(fake, monkeypatch):
    loaded = []
    monkeypatch.setattr(
        client_module.config, "load_incluster_config", lambda: loaded.append(True)
    )
    in_cluster = k8s.ApiClient()
    monkeypatch.setattr(client_module.k8s, "ApiClient", lambda: in_cluster)

    c = CCLClient("tenant")

    assert loaded == [True]
    assert c.namespace == "tenant"
    assert fake["client"].api_client is in_cluster


def test_namespace_is_required(fake):
    with pytest.raises(TypeError):
        CCLClient(api_client=k8s.ApiClient())
