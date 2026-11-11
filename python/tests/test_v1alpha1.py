# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""The generated spec and status models, and the hand-written objects around them, accept
the repository's examples unchanged."""

from pathlib import Path

import pytest
import yaml
from pydantic import ValidationError

from ccluster import v1alpha1
from ccluster.v1alpha1 import CCLComponentSet, CCLInfraPlane, Kind, ObjectMeta
from ccluster.v1alpha1.base import CustomResource
from ccluster.v1alpha1.cclcomponentset import (
    CCLComponentSetSpec,
    CCLComponentSetStatus,
    ComponentSpec,
    LocalObjectReference,
)
from ccluster.v1alpha1.cclinfraplane import CCLInfraPlaneSpec, CCLInfraPlaneStatus
from ccluster.v1alpha1.cclinfraplane import LocalObjectReference as InfraPlaneRef

EXAMPLES = Path(__file__).resolve().parents[2] / "examples" / "quickstart" / "manifests"


def dump(model):
    return model.model_dump(by_alias=True, exclude_none=True)


@pytest.mark.parametrize(
    ("file", "model"),
    [
        ("v1alpha1_cclcomponentset.yaml", CCLComponentSet),
        ("v1alpha1_cclinfraplane.yaml", CCLInfraPlane),
    ],
)
def test_quickstart_example_round_trips(file, model):
    # The infra plane example also carries the Namespace and Secret it depends on.
    kind = model.model_fields["kind"].default
    docs = yaml.safe_load_all((EXAMPLES / file).read_text())
    doc = next(d for d in docs if d["kind"] == kind)
    assert dump(model.model_validate(doc)) == doc


def test_api_version_and_kind_are_fixed():
    obj = CCLInfraPlane(
        metadata=ObjectMeta(name="edge-1", namespace="tenant"),
        # Each generated module carries its own LocalObjectReference class.
        spec=CCLInfraPlaneSpec(cluster_credentials_ref=InfraPlaneRef(name="creds")),
    )
    out = dump(obj)
    assert out["apiVersion"] == "ccluster.nvidia.com/v1alpha1"
    assert out["kind"] == Kind.CCL_INFRA_PLANE == "CCLInfraPlane"
    assert "status" not in out
    with pytest.raises(ValidationError):
        CCLInfraPlane.model_validate({**out, "kind": Kind.CCL_COMPONENT_SET})


def test_every_generated_kind_is_declared():
    # __init__.py is generated from the same CRD kinds as the <kind>.py modules: one Kind
    # member and one concrete CustomResource per kind.
    modules = {
        p.stem for p in Path(v1alpha1.__file__).parent.glob("*.py") if p.stem not in ("__init__", "base")
    }
    assert {k.value.lower() for k in Kind} == modules
    for kind in Kind:
        cls = getattr(v1alpha1, kind.value)
        assert issubclass(cls, CustomResource)
        assert cls.model_fields["kind"].default is kind


def test_objects_read_back_from_the_server_validate():
    # The server adds metadata it manages and the operator writes status; both survive.
    obj = CCLInfraPlane.model_validate(
        {
            "apiVersion": "ccluster.nvidia.com/v1alpha1",
            "kind": Kind.CCL_INFRA_PLANE,
            "metadata": {"name": "edge-1", "namespace": "tenant", "uid": "u-1", "resourceVersion": "7"},
            "spec": {"clusterCredentialsRef": {"name": "creds"}},
            "status": {"conditions": [{"type": "Available", "status": "True", "reason": "R", "message": "", "lastTransitionTime": "2026-09-16T00:00:00Z"}]},
        }
    )
    assert obj.metadata.model_extra == {"uid": "u-1", "resourceVersion": "7"}
    assert obj.status.conditions[0].type == "Available"


def test_status_models_read_what_the_operator_writes():
    status = CCLInfraPlaneStatus.model_validate(
        {
            "observedGeneration": 2,
            "conditions": [
                {
                    "type": "Available",
                    "status": "True",
                    "reason": "Registered",
                    "message": "Cluster Secret mirrored",
                    "lastTransitionTime": "2026-09-16T00:00:00Z",
                }
            ],
        }
    )
    assert status.conditions[0].status.value == "True"
    assert CCLComponentSetStatus.model_validate({}).conditions is None


def test_infra_plane_status_carries_the_cluster_config_status():
    # The control-plane operator copies the infra plane's CCLClusterConfig status verbatim.
    condition = {
        "type": "Verified",
        "status": "False",
        "reason": "ComponentsNotVerified",
        "message": "keda",
        "lastTransitionTime": "2026-09-16T00:00:00Z",
    }
    status = CCLInfraPlaneStatus.model_validate(
        {
            "conditions": [{**condition, "type": "Available", "status": "True", "reason": "R"}],
            "clusterConfig": {
                "observedGeneration": 3,
                "domain": "edge-1.tenant.example.com",
                "conditions": [condition],
            },
        }
    )
    assert status.cluster_config.observed_generation == 3
    assert status.cluster_config.domain == "edge-1.tenant.example.com"
    assert status.cluster_config.conditions[0].type == "Verified"
    assert status.conditions[0].type == "Available"


def test_wire_names_are_aliases():
    spec = CCLComponentSetSpec.model_validate(
        {
            "components": [
                {
                    "name": "keda",
                    "infraPlaneRef": {"name": "plane"},
                    "chart": "catalogs/cluster_base/keda",
                    "namespace": "keda",
                }
            ]
        }
    )
    assert spec.components[0].infra_plane_ref.name == "plane"
    assert "values" not in dump(spec)["components"][0]


def test_crd_constraints_are_enforced():
    # The models come from the CRD schema, so controller-gen's validation markers
    # (DNS-1123 names, at least one component) are checked before any request is made.
    with pytest.raises(ValidationError):
        ComponentSpec(
            name="Not-DNS-1123",
            infra_plane_ref=LocalObjectReference(name="plane"),
            chart="catalogs/cluster_base/keda",
            namespace="keda",
        )
    with pytest.raises(ValidationError):
        CCLComponentSetSpec(components=[])
