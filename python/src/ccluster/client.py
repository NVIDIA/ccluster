# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""A client for the control-plane operator's API.

CCLClient registers infra-plane clusters with the control plane and removes them again,
and creates, polls and deletes the component sets deployed onto them. It works in any
namespace and by default connects with the in-cluster configuration of the pod it runs in.
"""

from __future__ import annotations

import base64

from kubernetes import client as k8s
from kubernetes import config
from kubernetes.dynamic import DynamicClient
from kubernetes.dynamic.exceptions import NotFoundError
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
    """Create and delete infra planes (cluster registrations) and component sets.

    Every method but the constructor takes as its first argument the namespace it works
    in: the CCLInfraPlanes, their credentials Secrets and the CCLComponentSets live there.
    Nothing the client holds is namespace-specific, so one client serves every namespace
    and constructing it neither reads nor writes. api_client defaults to the in-cluster
    configuration; from a workstation pass kubernetes.config.new_client_from_config().
    """

    def __init__(self, api_client: k8s.ApiClient | None = None) -> None:
        if api_client is None:
            config.load_incluster_config()
            api_client = k8s.ApiClient()
        self._api = api_client
        self._dynamic = DynamicClient(api_client)
        resources = self._dynamic.resources
        self._namespace_gvr = resources.get(api_version="v1", kind="Namespace")
        self._secret_gvr = resources.get(api_version="v1", kind="Secret")
        self._infra_plane_gvr = resources.get(
            api_version=API_VERSION, kind=Kind.CCL_INFRA_PLANE.value
        )
        self._component_set_gvr = resources.get(
            api_version=API_VERSION, kind=Kind.CCL_COMPONENT_SET.value
        )

    def ensure_namespace(self, namespace: str) -> None:
        """Create namespace if it does not exist.

        No other method creates one, so call this before registering the first
        CCLInfraPlane of a namespace. A CCLComponentSet needs an Available CCLInfraPlane
        already in its namespace, so it can never be the first object written there.
        Creation is separate from the writes so that a mistyped namespace fails against
        the API server rather than quietly gaining a working infra plane.
        """
        try:
            self._dynamic.get(self._namespace_gvr, name=namespace)
        except NotFoundError:
            body = k8s.V1Namespace(
                api_version="v1",
                kind="Namespace",
                metadata=k8s.V1ObjectMeta(name=namespace),
            )
            self._dynamic.create(
                self._namespace_gvr, body=self._api.sanitize_for_serialization(body)
            )

    def apply_infraplane(
        self,
        namespace: str,
        name: str,
        server: str | None = None,
        bearer_token: str | None = None,
        ca_cert: str | bytes | None = None,
    ) -> CCLInfraPlane:
        """Create or update the CCLInfraPlane called name in namespace, for the cluster at server.

        Server-side applies a Secret named "<name>-credentials" in Argo CD's
        cluster-secret shape, then a CCLInfraPlane pointing at it. ca_cert is the API
        server's CA certificate in PEM. The call returns as soon as both objects are
        applied; it does not wait for the operator to report Available.

        server, bearer_token and ca_cert are all optional. With none given, this
        registers the cluster the operator itself runs in: server defaults to Argo CD's
        special-cased in-cluster address, and bearer_token/ca_cert stay unset, which Argo
        CD accepts for that address by connecting with its own ServiceAccount instead.

        namespace must already exist; call ensure_namespace first for the first plane of
        a namespace.
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
            metadata=k8s.V1ObjectMeta(name=secret_name, namespace=namespace),
            type="Opaque",
            string_data={
                "server": server,
                "config": cluster_config.model_dump_json(
                    by_alias=True, exclude_none=True
                ),
            },
        )
        self._apply(self._secret_gvr, namespace, secret)

        plane = CCLInfraPlane(
            metadata=ObjectMeta(name=name, namespace=namespace),
            spec=CCLInfraPlaneSpec(cluster_credentials_ref=LocalObjectReference(name=secret_name)),
        )
        return CCLInfraPlane.model_validate(
            self._apply_object(self._infra_plane_gvr, namespace, plane)
        )

    def delete_infraplane(self, namespace: str, name: str) -> None:
        """Remove the CCLInfraPlane called name in namespace and its credentials Secret.

        The CCLInfraPlane goes first so its finalizer can remove the mirrored Argo CD
        cluster Secret. Either object already being gone is not an error. The namespace
        is left in place.
        """
        self._delete(self._infra_plane_gvr, namespace, name)
        self._delete(self._secret_gvr, namespace, _credentials_secret_name(name))

    def get_infraplane(self, namespace: str, name: str) -> CCLInfraPlane:
        """Return the CCLInfraPlane called name in namespace, or raise NotFoundError."""
        plane = self._dynamic.get(self._infra_plane_gvr, name=name, namespace=namespace)
        return CCLInfraPlane.model_validate(plane.to_dict())

    def apply_component_set(
        self, namespace: str, name: str, components: list[ComponentSpec]
    ) -> CCLComponentSet:
        """Create or update the CCLComponentSet called name in namespace.

        components deploy in list order, each onto the CCLInfraPlane its infraPlaneRef
        names; those planes must live in the same namespace and already be Available.
        One set may target several planes. Build components with
        ccluster.catalogs.component. Applying under the client's field manager means
        calling again with a different list updates the set in place.

        The call returns as soon as the object is applied; it does not wait for the
        rollout to finish. Poll is_component_set_finished to check convergence.

        namespace is the set's own, on the control plane. The namespace a component
        installs into is on its infra plane, created there by Argo CD from the
        CreateNamespace=true sync option the operator puts on every Application.
        """
        component_set = CCLComponentSet(
            metadata=ObjectMeta(name=name, namespace=namespace),
            spec=CCLComponentSetSpec(components=components),
        )
        return CCLComponentSet.model_validate(
            self._apply_object(self._component_set_gvr, namespace, component_set)
        )

    def is_component_set_finished(self, namespace: str, name: str) -> bool:
        """Return whether the current generation is applied and reported converged.

        Reads once. Returns False during deletion or unless validation, Application
        writes and rollout completion are confirmed for this generation.
        The caller controls polling and timeouts.
        Raises kubernetes.dynamic.exceptions.NotFoundError if the set does not exist.

        True means the operator has written every Application for this generation and
        Argo CD reports them Healthy and Synced. That verdict is about each Application
        as Argo CD last compared it, so shortly after an update it can still describe the
        previous spec. Treat True as convergence, not as proof the newest values are live.
        """
        component_set = self.get_component_set(namespace, name)

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

    def _apply_object(self, gvr, namespace: str, obj: BaseModel) -> dict:
        """Server-side apply one of this API's objects and return it as the server holds it.

        obj is a whole object from ccluster.v1alpha1.base; the returned dict carries the
        metadata the server filled in and the status the operator has written, and
        validates back into the same class.
        """
        body = obj.model_dump(mode="json", by_alias=True, exclude_none=True)
        return self._apply(gvr, namespace, body).to_dict()

    def delete_component_set(self, namespace: str, name: str) -> None:
        """Remove the CCLComponentSet called name in namespace.

        Returns as soon as the delete is accepted. The operator's finalizer then removes
        the set's Argo CD Applications in reverse order, one at a time, so the object
        stays in Terminating until every chart is uninstalled. Already being gone is not
        an error. The referenced CCLInfraPlanes and the namespace are left in place.
        """
        self._delete(self._component_set_gvr, namespace, name)

    def get_component_set(self, namespace: str, name: str) -> CCLComponentSet:
        """Return the CCLComponentSet called name in namespace, or raise NotFoundError."""
        got = self._dynamic.get(self._component_set_gvr, name=name, namespace=namespace)
        return CCLComponentSet.model_validate(got.to_dict())

    def _apply(self, gvr, namespace: str, body):
        return self._dynamic.server_side_apply(
            gvr,
            body=self._api.sanitize_for_serialization(body),
            namespace=namespace,
            field_manager=_FIELD_MANAGER,
            force_conflicts=True,
        )

    def _delete(self, gvr, namespace: str, name: str) -> None:
        try:
            self._dynamic.delete(gvr, name=name, namespace=namespace)
        except NotFoundError:
            pass

