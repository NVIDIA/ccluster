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

"""A client for the control-plane operator's API.

CCLClient registers infra-plane clusters with the control plane and removes them again,
and creates and deletes the component sets deployed onto them. It writes into one
namespace, fixed at construction, and by default connects with the in-cluster
configuration of the pod it runs in.
"""

from __future__ import annotations

import base64

from kubernetes import client as k8s
from kubernetes import config
from kubernetes.dynamic import DynamicClient
from kubernetes.dynamic.exceptions import NotFoundError
from pydantic import BaseModel, ConfigDict, Field

from ccluster.v1alpha1 import API_VERSION, CCLComponentSet, CCLInfraPlane, Kind, ObjectMeta
from ccluster.v1alpha1.cclcomponentset import CCLComponentSetSpec, ComponentSpec
from ccluster.v1alpha1.cclinfraplane import CCLInfraPlaneSpec, LocalObjectReference


__all__ = ["CCLClient", "ClusterConfig", "TLSClientConfig"]


# Server-side apply records, per field, which manager last set it. Applying under one
# fixed name is what lets a later register_infraplane update the Secret this client wrote
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


class CCLClient:
    """Register and unregister infra-plane clusters, and create and delete component
    sets, in one namespace.

    namespace is where the CCLInfraPlanes, their credentials Secrets and the
    CCLComponentSets are written; it is created if it does not exist. api_client
    defaults to the in-cluster configuration; from a workstation pass
    kubernetes.config.new_client_from_config().
    """

    def __init__(self, namespace: str, api_client: k8s.ApiClient | None = None) -> None:
        if api_client is None:
            config.load_incluster_config()
            api_client = k8s.ApiClient()
        self.namespace = namespace
        self._api = api_client
        self._dynamic = DynamicClient(api_client)
        resources = self._dynamic.resources
        self._namespaces = resources.get(api_version="v1", kind="Namespace")
        self._secrets = resources.get(api_version="v1", kind="Secret")
        self._infra_planes = resources.get(
            api_version=API_VERSION, kind=Kind.CCL_INFRA_PLANE.value
        )
        self._component_sets = resources.get(
            api_version=API_VERSION, kind=Kind.CCL_COMPONENT_SET.value
        )
        self._ensure_namespace()

    def _ensure_namespace(self) -> None:
        try:
            self._dynamic.get(self._namespaces, name=self.namespace)
        except NotFoundError:
            namespace = k8s.V1Namespace(
                api_version="v1",
                kind="Namespace",
                metadata=k8s.V1ObjectMeta(name=self.namespace),
            )
            self._dynamic.create(
                self._namespaces, body=self._api.sanitize_for_serialization(namespace)
            )

    def register_infraplane(
        self,
        name: str,
        server: str | None = None,
        bearer_token: str | None = None,
        ca_cert: str | bytes | None = None,
    ) -> CCLInfraPlane:
        """Register the cluster at server as the CCLInfraPlane called name.

        Creates (or updates) a Secret named "<name>-credentials" in Argo CD's
        cluster-secret shape, then a CCLInfraPlane pointing at it. ca_cert is the API
        server's CA certificate in PEM. The call returns as soon as both objects are
        applied; it does not wait for the operator to report Available.

        server, bearer_token and ca_cert are all optional. With none given, this
        registers the cluster the operator itself runs in: server defaults to Argo CD's
        special-cased in-cluster address, and bearer_token/ca_cert stay unset, which Argo
        CD accepts for that address by connecting with its own ServiceAccount instead.
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
        self._apply(self._secrets, secret)

        plane = CCLInfraPlane(
            metadata=ObjectMeta(name=name, namespace=self.namespace),
            spec=CCLInfraPlaneSpec(cluster_credentials_ref=LocalObjectReference(name=secret_name)),
        )
        return CCLInfraPlane.model_validate(self._apply_object(self._infra_planes, plane))

    def unregister_infraplane(self, name: str) -> None:
        """Remove the CCLInfraPlane called name and its credentials Secret.

        The CCLInfraPlane goes first so its finalizer can remove the mirrored Argo CD
        cluster Secret. Either object already being gone is not an error. The namespace
        is left in place.
        """
        self._delete(self._infra_planes, name)
        self._delete(self._secrets, _credentials_secret_name(name))

    def create_component_set(self, name: str, components: list[ComponentSpec]) -> CCLComponentSet:
        """Create (or update) the CCLComponentSet called name.

        components deploy in list order, each onto the CCLInfraPlane its infraPlaneRef
        names; those planes must live in this client's namespace and already be
        Available. One set may target several planes. Build components with
        ccluster.catalogs.component. Applying under the client's field manager means
        calling again with a different list updates the set in place.

        The call returns as soon as the object is applied; it does not wait for the
        operator to report ComponentsApplied.
        """
        component_set = CCLComponentSet(
            metadata=ObjectMeta(name=name, namespace=self.namespace),
            spec=CCLComponentSetSpec(components=components),
        )
        return CCLComponentSet.model_validate(
            self._apply_object(self._component_sets, component_set)
        )

    def _apply_object(self, resource, obj: BaseModel) -> dict:
        """Server-side apply one of this API's objects and return it as the server holds it.

        obj is a whole object from ccluster.v1alpha1.base; the returned dict carries the
        metadata the server filled in and the status the operator has written, and
        validates back into the same class.
        """
        body = obj.model_dump(mode="json", by_alias=True, exclude_none=True)
        return self._apply(resource, body).to_dict()

    def delete_component_set(self, name: str) -> None:
        """Remove the CCLComponentSet called name.

        Returns as soon as the delete is accepted. The operator's finalizer then removes
        the set's Argo CD Applications in reverse order, one at a time, so the object
        stays in Terminating until every chart is uninstalled. Already being gone is not
        an error. The referenced CCLInfraPlanes and the namespace are left in place.
        """
        self._delete(self._component_sets, name)

    def _apply(self, resource, body):
        return self._dynamic.server_side_apply(
            resource,
            body=self._api.sanitize_for_serialization(body),
            namespace=self.namespace,
            field_manager=_FIELD_MANAGER,
            force_conflicts=True,
        )

    def _delete(self, resource, name: str) -> None:
        try:
            self._dynamic.delete(resource, name=name, namespace=self.namespace)
        except NotFoundError:
            pass
