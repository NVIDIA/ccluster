# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""CCLClient writes the Secret, CCLInfraPlane and CCLComponentSet the operator expects, without a cluster."""

import base64
import json
from types import SimpleNamespace

import pytest
from kubernetes import client as k8s
from kubernetes.dynamic.exceptions import ConflictError, NotFoundError, UnprocessibleEntityError
from pydantic import ValidationError

from ccluster import catalogs
from ccluster import client as client_module
from ccluster.catalogs.cluster_base import argo_rollouts, keda
from ccluster.client import CCLClient, component_set_current_conditions
from ccluster.v1alpha1 import CCLComponentSet, CCLInfraPlane, Kind

NS = "tenant"
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
        self.rejected = set()  # kinds the API server refuses to apply
        self.racing = set()  # namespaces another caller creates between get and create
        self.component_set = None
        self.infra_plane = None
        self.items = {}  # kind -> the objects a list returns, across namespaces
        self.reads = []
        self.resources = SimpleNamespace(
            get=lambda api_version, kind: FakeResource(kind)
        )

    def get(self, resource, name=None, namespace=None):
        self.reads.append((resource.kind, name, namespace))
        if name is None:
            items = [
                item
                for item in self.items.get(resource.kind, [])
                if namespace is None or item["metadata"]["namespace"] == namespace
            ]
            return SimpleNamespace(to_dict=lambda: {"items": items})
        if (resource.kind, name) in self.missing:
            raise NotFoundError(
                SimpleNamespace(status=404, reason="NotFound", body=b"{}", headers={})
            )
        if resource.kind == "Namespace" and name not in self.namespaces:
            raise NotFoundError(
                SimpleNamespace(status=404, reason="NotFound", body=b"{}", headers={})
            )
        if resource.kind == Kind.CCL_COMPONENT_SET.value:
            return SimpleNamespace(to_dict=lambda: self.component_set)
        if resource.kind == Kind.CCL_INFRA_PLANE.value:
            return SimpleNamespace(to_dict=lambda: self.infra_plane)
        return SimpleNamespace(to_dict=lambda: {"metadata": {"name": name}})

    def create(self, resource, body):
        if resource.kind == "Namespace" and body["metadata"]["name"] in self.racing:
            raise ConflictError(
                SimpleNamespace(status=409, reason="AlreadyExists", body=b"{}", headers={})
            )
        self.created.append((resource.kind, body))
        if resource.kind == "Namespace":
            self.namespaces.add(body["metadata"]["name"])

    def server_side_apply(self, resource, body, namespace, **kwargs):
        if resource.kind in self.rejected:
            raise UnprocessibleEntityError(
                SimpleNamespace(status=422, reason="Invalid", body=b"{}", headers={})
            )
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

    holder["built"] = 0

    def factory(api_client):
        holder["client"] = FakeDynamicClient(api_client)
        holder["built"] += 1
        return holder["client"]

    monkeypatch.setattr(client_module, "DynamicClient", factory)
    return holder


def make_client(fake):
    c = CCLClient(NS, api_client=k8s.ApiClient())
    return c, fake["client"]


def test_constructing_reads_and_writes_no_object(fake):
    _, dyn = make_client(fake)
    assert (dyn.reads, dyn.created, dyn.applied) == ([], [], [])


def test_an_empty_namespace_is_rejected(fake):
    # The Kubernetes client would read "" as every namespace.
    with pytest.raises(ValueError, match="namespace must not be empty"):
        CCLClient("", api_client=k8s.ApiClient())


def test_ensure_namespace_creates_a_missing_namespace(fake):
    c, dyn = make_client(fake)

    c.ensure_namespace()

    assert dyn.created == [
        (
            "Namespace",
            {"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": "tenant"}},
        )
    ]


def test_ensure_namespace_leaves_an_existing_namespace_alone(fake):
    c, dyn = make_client(fake)
    dyn.namespaces.add("tenant")

    c.ensure_namespace()

    assert dyn.reads == [("Namespace", "tenant", None)]
    assert dyn.created == []


def test_ensure_namespace_tolerates_a_concurrent_create(fake):
    c, dyn = make_client(fake)
    dyn.racing.add(NS)

    c.ensure_namespace()

    assert dyn.reads == [("Namespace", "tenant", None)]


def test_no_write_creates_a_namespace(fake):
    # ensure_namespace is the only method that does. A CCLInfraPlane is the first object
    # written to a namespace; a CCLComponentSet needs one already Available there, and a
    # component's own namespace is created on its infra plane by Argo CD.
    c, dyn = make_client(fake)

    c.apply_infraplane(
        name="edge-1",
        server="https://10.0.0.1:6443",
        bearer_token="tok",
        ca_cert=CA,
    )
    c.apply_component_set(
        name="local-base",
        components=[catalogs.component("keda", "edge-1", keda.CHART, "keda")],
    )

    assert dyn.created == []
    assert dyn.reads == []


def test_clients_sharing_an_api_client_discover_once(fake):
    api_client = k8s.ApiClient()
    a = CCLClient("tenant-a", api_client=api_client)
    b = CCLClient("tenant-b", api_client=api_client)

    assert fake["built"] == 1
    # Each still writes only into its own namespace.
    keda_component = [catalogs.component("keda", "edge-1", keda.CHART, "keda")]
    a.apply_component_set(name="base", components=keda_component)
    b.delete_component_set(name="base")
    dyn = fake["client"]
    assert [(ns, body["metadata"]["namespace"]) for _, ns, body, _ in dyn.applied] == [
        ("tenant-a", "tenant-a")
    ]
    assert dyn.deleted == [(Kind.CCL_COMPONENT_SET, "base", "tenant-b")]


def test_each_api_client_runs_its_own_discovery(fake):
    CCLClient(NS, api_client=k8s.ApiClient())
    CCLClient(NS, api_client=k8s.ApiClient())

    assert fake["built"] == 2


def test_apply_infraplane_applies_infra_plane_then_secret(fake):
    c, dyn = make_client(fake)

    plane = c.apply_infraplane(
        name="edge-1",
        server="https://10.0.0.1:6443",
        bearer_token="tok",
        ca_cert=CA,
    )

    kinds = [kind for kind, *_ in dyn.applied]
    assert kinds == [Kind.CCL_INFRA_PLANE, "Secret"]
    for _, ns_arg, _, kwargs in dyn.applied:
        assert ns_arg == "tenant"
        assert kwargs == {"field_manager": "ccluster", "force_conflicts": True}

    secret = dyn.applied[1][2]
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

    infra_plane = dyn.applied[0][2]
    assert infra_plane["apiVersion"] == "ccluster.nvidia.com/v1alpha1"
    assert infra_plane["kind"] == Kind.CCL_INFRA_PLANE
    assert infra_plane["metadata"] == {"name": "edge-1", "namespace": "tenant"}
    assert infra_plane["spec"] == {
        "clusterCredentialsRef": {"name": "edge-1-credentials"},
    }
    assert "status" not in infra_plane

    # The client hands back the applied object as the server holds it, typed.
    assert isinstance(plane, CCLInfraPlane)
    assert plane.metadata.name == "edge-1"
    assert plane.spec.cluster_credentials_ref.name == "edge-1-credentials"


def test_apply_infraplane_defaults_to_in_cluster_credential_free(fake):
    c, dyn = make_client(fake)

    c.apply_infraplane(name="edge-1")

    secret = dyn.applied[1][2]
    assert secret["stringData"]["server"] == "https://kubernetes.default.svc"
    assert json.loads(secret["stringData"]["config"]) == {
        "tlsClientConfig": {"insecure": False}
    }


def test_apply_infraplane_accepts_pem_bytes(fake):
    c, dyn = make_client(fake)
    c.apply_infraplane(
        name="edge-1",
        server="https://10.0.0.1:6443",
        bearer_token="tok",
        ca_cert=CA.encode(),
    )
    config = json.loads(dyn.applied[1][2]["stringData"]["config"])
    assert config["tlsClientConfig"]["caData"] == base64.b64encode(CA.encode()).decode()


def test_apply_infraplane_writes_no_secret_when_the_plane_is_rejected(fake):
    # The API server refuses a name that is not a DNS label, which the plane's domain is
    # derived from. Credentials for a plane that does not exist must not be written.
    c, dyn = make_client(fake)
    dyn.rejected = {Kind.CCL_INFRA_PLANE.value}
    with pytest.raises(UnprocessibleEntityError):
        c.apply_infraplane(name="edge.1", bearer_token="tok")
    assert dyn.applied == []


def test_check_infraplane_dry_runs_the_plane_apply_infraplane_writes(fake):
    c, dyn = make_client(fake)

    c.check_infraplane(name="edge-1")
    c.apply_infraplane(name="edge-1")

    (_, ns_arg, checked, kwargs), (_, _, applied, _), _ = dyn.applied
    assert ns_arg == "tenant"
    assert kwargs == {
        "field_manager": "ccluster", "force_conflicts": True, "dry_run": "All",
    }
    assert checked == applied


def test_check_infraplane_raises_when_the_plane_is_rejected(fake):
    c, dyn = make_client(fake)
    dyn.rejected = {Kind.CCL_INFRA_PLANE.value}
    with pytest.raises(UnprocessibleEntityError):
        c.check_infraplane(name="edge.1")


def test_delete_infraplane_deletes_infra_plane_before_secret(fake):
    c, dyn = make_client(fake)
    c.delete_infraplane(name="edge-1")
    assert dyn.deleted == [
        (Kind.CCL_INFRA_PLANE, "edge-1", "tenant"),
        ("Secret", "edge-1-credentials", "tenant"),
    ]


def test_delete_infraplane_ignores_missing_objects(fake):
    c, dyn = make_client(fake)
    dyn.missing = {(Kind.CCL_INFRA_PLANE, "edge-1"), ("Secret", "edge-1-credentials")}
    c.delete_infraplane(name="edge-1")
    assert dyn.deleted == []


def test_get_infraplane_returns_the_plane_with_its_status(fake):
    c, dyn = make_client(fake)
    dyn.infra_plane = {
        "apiVersion": "ccluster.nvidia.com/v1alpha1",
        "kind": "CCLInfraPlane",
        "metadata": {"name": "edge-1", "namespace": "tenant", "generation": 1},
        "spec": {
            "clusterCredentialsRef": {"name": "edge-1-credentials"},
        },
        "status": {
            "conditions": [
                {
                    "type": "Available",
                    "status": "True",
                    "reason": "ClusterRegistered",
                    "message": "Cluster secret mirrored",
                    "lastTransitionTime": "2026-01-01T00:00:00Z",
                    "observedGeneration": 1,
                }
            ]
        },
    }

    plane = c.get_infraplane(name="edge-1")

    assert isinstance(plane, CCLInfraPlane)
    assert plane.spec.cluster_credentials_ref.name == "edge-1-credentials"
    assert plane.status.conditions[0].type == "Available"
    assert dyn.reads[-1] == (Kind.CCL_INFRA_PLANE, "edge-1", "tenant")


def test_get_infraplane_raises_for_a_missing_plane(fake):
    c, dyn = make_client(fake)
    dyn.missing.add((Kind.CCL_INFRA_PLANE, "edge-1"))

    with pytest.raises(NotFoundError):
        c.get_infraplane(name="edge-1")


def plane(name, namespace):
    return {
        "metadata": {"name": name, "namespace": namespace},
        "spec": {"clusterCredentialsRef": {"name": f"{name}-credentials"}},
        "status": {"domain": f"{name}.{namespace}.example.com"},
    }


def component_set(name, namespace):
    return {
        "metadata": {"name": name, "namespace": namespace},
        "spec": {
            "components": [
                {
                    "name": "keda",
                    "infraPlaneRef": {"name": "edge-1"},
                    "chart": "catalogs/cluster_base/keda",
                    "namespace": "keda",
                }
            ]
        },
    }


def test_list_infraplanes_returns_the_planes_in_the_namespace(fake):
    c, dyn = make_client(fake)
    dyn.items[Kind.CCL_INFRA_PLANE] = [plane("edge-1", NS), plane("edge-2", "other")]

    planes = c.list_infraplanes()

    assert [p.metadata.name for p in planes] == ["edge-1"]
    assert isinstance(planes[0], CCLInfraPlane)
    assert dyn.reads[-1] == (Kind.CCL_INFRA_PLANE, None, NS)


def test_list_component_sets_returns_the_sets_in_the_namespace(fake):
    c, dyn = make_client(fake)
    dyn.items[Kind.CCL_COMPONENT_SET] = [component_set("base", NS), component_set("apps", "other")]

    sets = c.list_component_sets()

    assert [s.metadata.name for s in sets] == ["base"]
    assert isinstance(sets[0], CCLComponentSet)
    assert dyn.reads[-1] == (Kind.CCL_COMPONENT_SET, None, NS)


def test_lists_of_nothing_are_empty(fake):
    c, _ = make_client(fake)

    assert c.list_infraplanes() == []
    assert c.list_component_sets() == []


def test_apply_component_set_applies_the_set(fake):
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

    component_set = c.apply_component_set(name="local-base", components=components)

    assert [kind for kind, *_ in dyn.applied] == [Kind.CCL_COMPONENT_SET]
    _, ns_arg, body, kwargs = dyn.applied[0]
    assert ns_arg == "tenant"
    assert kwargs == {"field_manager": "ccluster", "force_conflicts": True}
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


def test_apply_component_set_rejects_empty_components(fake):
    c, dyn = make_client(fake)
    # minItems: 1 comes from the CRD, so Pydantic rejects it before any request is made.
    with pytest.raises(ValidationError):
        c.apply_component_set(name="local-base", components=[])
    assert dyn.applied == []


def test_delete_component_set_deletes_the_set(fake):
    c, dyn = make_client(fake)
    c.delete_component_set(name="local-base")
    assert dyn.deleted == [(Kind.CCL_COMPONENT_SET, "local-base", "tenant")]


def test_delete_component_set_ignores_a_missing_set(fake):
    c, dyn = make_client(fake)
    dyn.missing = {(Kind.CCL_COMPONENT_SET, "local-base")}
    c.delete_component_set(name="local-base")
    assert dyn.deleted == []


@pytest.mark.parametrize("method", ["delete_infraplane", "delete_component_set"])
def test_delete_takes_the_name_by_keyword(fake, method):
    c, dyn = make_client(fake)

    with pytest.raises(TypeError):
        getattr(c, method)("edge-1")
    assert dyn.deleted == []


def test_namespace_is_required(fake):
    # The namespace is the client's isolation boundary; it has no default to fall back to.
    with pytest.raises(TypeError):
        CCLClient(api_client=k8s.ApiClient())


def test_api_client_is_required(fake):
    # No in-cluster default: the caller decides which connection clients share.
    with pytest.raises(TypeError):
        CCLClient(NS)
    assert fake["built"] == 0


def test_failed_discovery_is_retried_by_the_next_client(fake, monkeypatch):
    api_client = k8s.ApiClient()
    real_factory = client_module.DynamicClient

    def unreachable(api_client):
        raise ConnectionError("API server unreachable")

    monkeypatch.setattr(client_module, "DynamicClient", unreachable)
    with pytest.raises(ConnectionError):
        CCLClient(NS, api_client=api_client)

    monkeypatch.setattr(client_module, "DynamicClient", real_factory)
    CCLClient(NS, api_client=api_client)
    assert fake["built"] == 1


def component_set_body(*conditions, generation=1, observed_generation=1):
    """A CCLComponentSet as the server returns it. conditions are (type, status, reason)."""
    return {
        "apiVersion": "ccluster.nvidia.com/v1alpha1",
        "kind": "CCLComponentSet",
        "metadata": {"name": "base", "namespace": "tenant", "generation": generation},
        "spec": {
            "components": [
                {
                    "name": "argo-rollouts",
                    "infraPlaneRef": {"name": "plane"},
                    "chart": argo_rollouts.CHART,
                    "namespace": "argo-rollouts",
                }
            ]
        },
        "status": {
            "observedGeneration": observed_generation,
            "conditions": [
                {
                    "type": condition_type,
                    "status": status,
                    "reason": reason,
                    "message": f"message for {reason}",
                    "lastTransitionTime": "2026-01-01T00:00:00Z",
                    "observedGeneration": observed_generation,
                }
                for condition_type, status, reason in conditions
            ],
        },
    }


VALIDATED = ("Validated", "True", "ComponentsAccepted")
APPLIED = ("ComponentsApplied", "True", "ApplicationsCreated")
SYNCING = ("Progressing", "True", "WaveSyncing")
COMPLETE = ("Progressing", "False", "RolloutComplete")


def test_current_conditions_key_by_type_and_keep_reason_and_message():
    component_set = CCLComponentSet.model_validate(
        component_set_body(VALIDATED, APPLIED, SYNCING)
    )

    conditions = component_set_current_conditions(component_set)

    assert set(conditions) == {"Validated", "ComponentsApplied", "Progressing"}
    assert conditions["Progressing"].reason == "WaveSyncing"
    assert conditions["Progressing"].message == "message for WaveSyncing"


def test_current_conditions_drop_an_older_generation():
    body = component_set_body(
        VALIDATED, APPLIED, COMPLETE, generation=2, observed_generation=2
    )
    for condition in body["status"]["conditions"]:
        if condition["type"] == "Progressing":
            condition["observedGeneration"] = 1
    component_set = CCLComponentSet.model_validate(body)

    conditions = component_set_current_conditions(component_set)

    assert set(conditions) == {"Validated", "ComponentsApplied"}


def test_current_conditions_are_empty_without_a_status():
    component_set = CCLComponentSet.model_validate(
        {**component_set_body(), "status": None}
    )

    assert component_set_current_conditions(component_set) == {}


def test_is_component_set_finished_is_true_once_progressing_is_false(fake):
    c, dyn = make_client(fake)
    dyn.component_set = component_set_body(VALIDATED, APPLIED, COMPLETE)

    assert c.is_component_set_finished(name="base") is True
    assert dyn.reads[-1] == (Kind.CCL_COMPONENT_SET.value, "base", "tenant")


@pytest.mark.parametrize(
    "body",
    [
        pytest.param(component_set_body(VALIDATED, APPLIED, SYNCING), id="syncing"),
        pytest.param(
            component_set_body(VALIDATED, APPLIED, COMPLETE, generation=2, observed_generation=1),
            id="stale-status",
        ),
        pytest.param(
            component_set_body(("Validated", "False", "InfraPlaneNotReady")),
            id="unavailable-plane",
        ),
        pytest.param({**component_set_body(), "status": None}, id="missing-status"),
        pytest.param(component_set_body(VALIDATED), id="missing-progress"),
    ],
)
def test_is_component_set_finished_rejects_incomplete_status(fake, body):
    c, dyn = make_client(fake)
    dyn.component_set = body

    assert c.is_component_set_finished(name="base") is False


@pytest.mark.parametrize("condition_type", ["Validated", "ComponentsApplied", "Progressing"])
def test_is_component_set_finished_requires_current_conditions(fake, condition_type):
    c, dyn = make_client(fake)
    body = component_set_body(VALIDATED, APPLIED, COMPLETE, generation=2, observed_generation=2)
    for condition in body["status"]["conditions"]:
        if condition["type"] == condition_type:
            condition["observedGeneration"] = 1
    dyn.component_set = body

    assert c.is_component_set_finished(name="base") is False


@pytest.mark.parametrize("applied", [None, ("ComponentsApplied", "False", "ApplicationConflict")])
def test_is_component_set_finished_requires_successful_application_writes(fake, applied):
    c, dyn = make_client(fake)
    conditions = [VALIDATED, COMPLETE]
    if applied is not None:
        conditions.append(applied)
    dyn.component_set = component_set_body(*conditions)

    assert c.is_component_set_finished(name="base") is False


def test_is_component_set_finished_is_false_during_teardown(fake):
    c, dyn = make_client(fake)
    body = component_set_body(VALIDATED, APPLIED, COMPLETE)
    body["metadata"]["deletionTimestamp"] = "2026-09-17T10:00:00Z"
    body["metadata"]["finalizers"] = ["ccluster.nvidia.com/argocd-applications"]
    dyn.component_set = body

    assert c.is_component_set_finished(name="base") is False


def test_is_component_set_finished_raises_for_a_missing_set(fake):
    c, dyn = make_client(fake)
    dyn.missing.add((Kind.CCL_COMPONENT_SET.value, "base"))

    with pytest.raises(NotFoundError):
        c.is_component_set_finished(name="base")
