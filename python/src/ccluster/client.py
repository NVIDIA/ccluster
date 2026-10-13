# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""A client for the control-plane operator's API.

CCLClient registers infra-plane clusters with the control plane and removes them again,
and creates, polls and deletes the component sets deployed onto them. It works in one
namespace, fixed at construction, over a Kubernetes ApiClient the caller supplies.
"""

from __future__ import annotations

import base64

from kubernetes import client as k8s
from kubernetes.dynamic import DynamicClient
from kubernetes.dynamic.exceptions import ConflictError, NotFoundError
from kubernetes.dynamic.resource import Resource
from pydantic import BaseModel, ConfigDict, Field

from ccluster.v1alpha1 import API_VERSION, CCLComponentSet, CCLInfraPlane, Kind, ObjectMeta
from ccluster.v1alpha1.cclcomponentset import (
    CCLComponentSetSpec,
    ComponentSpec,
    Condition,
    Status,
)
from ccluster.v1alpha1.cclinfraplane import CCLInfraPlaneSpec, LocalObjectReference


__all__ = [
    "CCLClient",
    "ClusterConfig",
    "TLSClientConfig",
    "component_set_current_conditions",
]


# Server-side apply records, per field, which manager last set it. Applying under one
# fixed name is what lets a later apply_infraplane update the Secret this client wrote
# without conflicting with itself, and what marks the objects as ours in managedFields.
_FIELD_MANAGER = "ccluster-py"

# Argo CD special-cases this exact server URL: it connects with its own ServiceAccount
# rather than anything in the Secret's "config", so no token or CA is needed to register
# the cluster the operator itself runs in. See examples/quickstart/v1alpha1_cclinfraplane.yaml.
_IN_CLUSTER_SERVER = "https://kubernetes.default.svc"


class TLSClientConfig(BaseModel):
    """The tlsClientConfig block of an Argo CD cluster Secret's config."""

    model_config = ConfigDict(populate_by_name=True)

    insecure: bool = False
    ca_data: str | None = Field(default=None, alias="caData")
    """The API server's CA certificate, PEM, base64-encoded."""


class ClusterConfig(BaseModel):
    """The config key of an Argo CD cluster Secret: how Argo CD reaches the cluster.

    Only the fields this client sets are modelled; see
    https://argo-cd.readthedocs.io/en/stable/operator-manual/declarative-setup/#clusters
    """

    model_config = ConfigDict(populate_by_name=True)

    bearer_token: str | None = Field(default=None, alias="bearerToken")
    tls_client_config: TLSClientConfig = Field(
        default_factory=TLSClientConfig, alias="tlsClientConfig"
    )


def _credentials_secret_name(cluster: str) -> str:
    return f"{cluster}-credentials"


class _Discovery:
    """What API discovery resolves for one ApiClient. None of it depends on a namespace."""

    def __init__(self, api_client: k8s.ApiClient) -> None:
        self.dynamic = DynamicClient(api_client)
        resources = self.dynamic.resources
        self.namespace_gvr: Resource = resources.get(api_version="v1", kind="Namespace")
        self.secret_gvr: Resource = resources.get(api_version="v1", kind="Secret")
        self.infra_plane_gvr: Resource = resources.get(
            api_version=API_VERSION, kind=Kind.CCL_INFRA_PLANE.value
        )
        self.component_set_gvr: Resource = resources.get(
            api_version=API_VERSION, kind=Kind.CCL_COMPONENT_SET.value
        )


# The ApiClient attribute that holds its _Discovery. Stored on the ApiClient itself, so the
# discovery results are released together with it.
_DISCOVERY_ATTR = "_ccluster_discovery"


def _discover(api_client: k8s.ApiClient) -> _Discovery:
    """Return api_client's discovery results, running discovery if it has none yet.

    Unlocked: constructions that race on a new api_client may each run discovery, and the
    last one's results are kept. Every run resolves the same resources, so any of them is
    correct.
    """
    discovery = getattr(api_client, _DISCOVERY_ATTR, None)
    if discovery is None:
        discovery = _Discovery(api_client)
        setattr(api_client, _DISCOVERY_ATTR, discovery)
    return discovery


def component_set_current_conditions(component_set: CCLComponentSet) -> dict[str, Condition]:
    """Return the conditions computed from the set's current generation, keyed by type.

    The operator stamps the generation only on the conditions it writes, so after a
    failed validation or apply the other types keep an older observedGeneration. Those
    are dropped; what remains is what the operator concluded about the spec as it now
    stands, and the reason and message behind it.
    """
    generation = getattr(component_set.metadata, "generation", None)
    status = component_set.status
    if generation is None or status is None or not status.conditions:
        return {}
    return {
        condition.type: condition
        for condition in status.conditions
        if condition.observed_generation == generation
    }


class CCLClient:
    """Create and delete infra planes (cluster registrations) and component sets, in one
    namespace.

    namespace is where the CCLInfraPlanes, their credentials Secrets and the
    CCLComponentSets are read and written, and the client reaches no other. It is the
    isolation boundary: whoever constructs the client decides which namespace it serves.
    Construct one client per namespace. Clients built on the same api_client share its
    connection pool and reuse its API discovery results.
    Constructing a client reads and writes no object.

    api_client is required. In a pod, call kubernetes.config.load_incluster_config() and
    pass kubernetes.client.ApiClient(); from a workstation pass
    kubernetes.config.new_client_from_config().
    """

    def __init__(self, namespace: str, *, api_client: k8s.ApiClient) -> None:
        self.namespace = namespace
        self._api = api_client
        self._discovery = _discover(api_client)

    def ensure_namespace(self) -> None:
        """Create the client's namespace if it does not exist.

        Another caller creating it between this read and this create is not an error.
        No other method creates one, so call this before registering the first
        CCLInfraPlane of a namespace. A CCLComponentSet needs an Available CCLInfraPlane
        already in its namespace, so it can never be the first object written there.
        Creation is separate from the writes so that a mistyped namespace fails against
        the API server rather than quietly gaining a working infra plane.
        """
        try:
            self._discovery.dynamic.get(self._discovery.namespace_gvr, name=self.namespace)
        except NotFoundError:
            body = k8s.V1Namespace(
                api_version="v1",
                kind="Namespace",
                metadata=k8s.V1ObjectMeta(name=self.namespace),
            )
            try:
                self._discovery.dynamic.create(
                    self._discovery.namespace_gvr, body=self._api.sanitize_for_serialization(body)
                )
            except ConflictError:
                pass

    def apply_infraplane(
        self,
        *,
        name: str,
        server: str | None = None,
        bearer_token: str | None = None,
        ca_cert: str | bytes | None = None,
    ) -> CCLInfraPlane:
        """Create or update the CCLInfraPlane called name, for the cluster at server.

        Server-side applies a Secret named "<name>-credentials" in Argo CD's
        cluster-secret shape, then a CCLInfraPlane pointing at it. ca_cert is the API
        server's CA certificate in PEM. The call returns as soon as both objects are
        applied; it does not wait for the operator to report Available.

        server, bearer_token and ca_cert are all optional. With none given, this
        registers the cluster the operator itself runs in: server defaults to Argo CD's
        special-cased in-cluster address, and bearer_token/ca_cert stay unset, which Argo
        CD accepts for that address by connecting with its own ServiceAccount instead.

        The namespace must already exist; call ensure_namespace first for the first plane
        of a namespace.
        """
        server = server or _IN_CLUSTER_SERVER
        if isinstance(ca_cert, str):
            ca_cert = ca_cert.encode()
        secret_name = _credentials_secret_name(name)

        cluster_config = ClusterConfig(
            bearer_token=bearer_token,
            tls_client_config=TLSClientConfig(
                ca_data=base64.b64encode(ca_cert).decode() if ca_cert else None,
            ),
        )
        secret = k8s.V1Secret(
            api_version="v1",
            kind="Secret",
            metadata=k8s.V1ObjectMeta(name=secret_name, namespace=self.namespace),
            type="Opaque",
            string_data={
                "server": server,
                "config": cluster_config.model_dump_json(
                    by_alias=True, exclude_none=True
                ),
            },
        )
        self._apply(self._discovery.secret_gvr, secret)

        plane = CCLInfraPlane(
            metadata=ObjectMeta(name=name, namespace=self.namespace),
            spec=CCLInfraPlaneSpec(cluster_credentials_ref=LocalObjectReference(name=secret_name)),
        )
        return CCLInfraPlane.model_validate(
            self._apply_object(self._discovery.infra_plane_gvr, plane)
        )

    def delete_infraplane(self, *, name: str) -> None:
        """Remove the CCLInfraPlane called name and its credentials Secret.

        The CCLInfraPlane goes first so its finalizer can remove the mirrored Argo CD
        cluster Secret. Either object already being gone is not an error, and neither is
        a namespace that does not exist: the API server answers both with the same 404.
        The namespace is left in place.
        """
        self._delete(self._discovery.infra_plane_gvr, name)
        self._delete(self._discovery.secret_gvr, _credentials_secret_name(name))

    def get_infraplane(self, *, name: str) -> CCLInfraPlane:
        """Return the CCLInfraPlane called name, or raise NotFoundError."""
        return CCLInfraPlane.model_validate(
            self._get(self._discovery.infra_plane_gvr, name)
        )

    def apply_component_set(
        self, *, name: str, components: list[ComponentSpec]
    ) -> CCLComponentSet:
        """Create or update the CCLComponentSet called name.

        components deploy in list order onto the CCLInfraPlane their infraPlaneRefs
        name. Every component must name the same plane, which must exist in the
        client's namespace and be Available; a set spanning several planes is not
        supported yet. The operator also checks every component's values against its
        chart, deploys nothing for a set that fails any check, and reports the outcome
        as the set's Validated condition.
        Build components with
        ccluster.catalogs.component. Applying under the client's field manager means
        calling again with a different list updates the set in place.

        The call returns as soon as the object is applied; it does not wait for the
        rollout to finish. Poll is_component_set_finished to check convergence.

        The client's namespace is the set's own, on the control plane. The namespace a
        component installs into is on its infra plane, created there by Argo CD from the
        CreateNamespace=true sync option the operator puts on every Application.
        """
        component_set = CCLComponentSet(
            metadata=ObjectMeta(name=name, namespace=self.namespace),
            spec=CCLComponentSetSpec(components=components),
        )
        return CCLComponentSet.model_validate(
            self._apply_object(self._discovery.component_set_gvr, component_set)
        )

    def is_component_set_finished(self, *, name: str) -> bool:
        """Return whether the current generation is applied and reported converged.

        Reads once. Returns False during deletion or unless validation, Application
        writes and rollout completion are confirmed for this generation.
        The caller controls polling and timeouts.
        Raises kubernetes.dynamic.exceptions.NotFoundError if the set does not exist.

        True means every Application matches current desired input and, for tracked
        requests, its sync succeeded before a later Healthy/Synced comparison.
        The timestamp check is a freshness barrier, not an atomic configuration-version
        acknowledgement. Existing Applications enter request tracking on a spec change;
        updates across already released components are not yet serialized by wave.
        """
        component_set = self.get_component_set(name=name)

        # deletionTimestamp is server-managed metadata, retained by ObjectMeta's
        # extra="allow". The finalizer never writes conditions, so a set being torn down
        # still carries the ones its last generation finished with.
        if getattr(component_set.metadata, "deletionTimestamp", None) is not None:
            return False

        conditions = component_set_current_conditions(component_set)
        expected = {
            "Validated": Status.true,
            "ComponentsApplied": Status.true,
            "Progressing": Status.false,
        }
        for condition_type, expected_status in expected.items():
            condition = conditions.get(condition_type)
            if condition is None or condition.status != expected_status:
                return False
        return True

    def _apply_object(self, gvr, obj: BaseModel) -> dict:
        """Server-side apply one of this API's objects and return it as the server holds it.

        obj is a whole object from ccluster.v1alpha1.base; the returned dict carries the
        metadata the server filled in and the status the operator has written, and
        validates back into the same class.
        """
        body = obj.model_dump(mode="json", by_alias=True, exclude_none=True)
        return self._apply(gvr, body).to_dict()

    def delete_component_set(self, *, name: str) -> None:
        """Remove the CCLComponentSet called name.

        Returns as soon as the delete is accepted. The operator's finalizer then removes
        the set's Argo CD Applications in reverse order, one at a time, so the object
        stays in Terminating until every chart is uninstalled. Already being gone is not
        an error, and neither is a namespace that does not exist: the API server answers
        both with the same 404. The referenced CCLInfraPlanes and the namespace are left
        in place.
        """
        self._delete(self._discovery.component_set_gvr, name)

    def get_component_set(self, *, name: str) -> CCLComponentSet:
        """Return the CCLComponentSet called name, or raise NotFoundError."""
        return CCLComponentSet.model_validate(
            self._get(self._discovery.component_set_gvr, name)
        )

    def _apply(self, gvr, body):
        return self._discovery.dynamic.server_side_apply(
            gvr,
            body=self._api.sanitize_for_serialization(body),
            namespace=self.namespace,
            field_manager=_FIELD_MANAGER,
            force_conflicts=True,
        )

    def _delete(self, gvr, name: str) -> None:
        try:
            self._discovery.dynamic.delete(gvr, name=name, namespace=self.namespace)
        except NotFoundError:
            pass

    def _get(self, gvr, name: str) -> dict:
        return self._discovery.dynamic.get(gvr, name=name, namespace=self.namespace).to_dict()
