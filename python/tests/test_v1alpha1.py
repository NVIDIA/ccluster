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

"""The generated CRD models accept the repository's examples and hand them back unchanged."""

from pathlib import Path

import pytest
import yaml
from pydantic import ValidationError

from ccluster.v1alpha1 import (
    CCLComponentSet,
    CCLComponentSetSpec,
    CCLInfraPlane,
    ComponentSpec,
    LocalObjectReference,
    ObjectMeta,
)

EXAMPLES = Path(__file__).resolve().parents[2] / "examples" / "quickstart"


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


def test_api_version_and_kind_default():
    obj = CCLComponentSet(
        metadata=ObjectMeta(name="x", namespace="y"),
        spec=CCLComponentSetSpec(
            components=[
                ComponentSpec(
                    name="keda",
                    infra_plane_ref=LocalObjectReference(name="plane"),
                    chart="catalogs/cluster_base/keda",
                    namespace="keda",
                )
            ]
        ),
    )
    out = dump(obj)
    assert out["apiVersion"] == "ccluster.nvidia.com/v1alpha1"
    assert out["kind"] == "CCLComponentSet"
    assert out["spec"]["components"][0]["infraPlaneRef"] == {"name": "plane"}
    assert "values" not in out["spec"]["components"][0]


def test_required_fields_and_unknown_fields_are_enforced():
    # The models are reflected from the Go API types, not lifted from the CRD, so the
    # kubebuilder validation markers (name patterns, item counts) are the API server's
    # to enforce. What Pydantic does check is the shape: required fields and no extras.
    with pytest.raises(ValidationError):
        ComponentSpec(name="keda", chart="catalogs/cluster_base/keda", namespace="keda")
    with pytest.raises(ValidationError):
        CCLComponentSetSpec.model_validate({"components": [], "unknown": 1})
