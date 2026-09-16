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

"""The generated spec and status models accept the repository's examples unchanged."""

from pathlib import Path

import pytest
import yaml
from pydantic import ValidationError

from ccluster.v1alpha1.cclcomponentset import (
    CCLComponentSetSpec,
    CCLComponentSetStatus,
    ComponentSpec,
    LocalObjectReference,
)
from ccluster.v1alpha1.cclinfraplane import CCLInfraPlaneSpec, CCLInfraPlaneStatus

EXAMPLES = Path(__file__).resolve().parents[2] / "examples" / "quickstart"


def dump(model):
    return model.model_dump(by_alias=True, exclude_none=True)


@pytest.mark.parametrize(
    ("file", "kind", "spec_model"),
    [
        ("v1alpha1_cclcomponentset.yaml", "CCLComponentSet", CCLComponentSetSpec),
        ("v1alpha1_cclinfraplane.yaml", "CCLInfraPlane", CCLInfraPlaneSpec),
    ],
)
def test_quickstart_example_spec_round_trips(file, kind, spec_model):
    # The infra plane example also carries the Namespace and Secret it depends on.
    docs = yaml.safe_load_all((EXAMPLES / file).read_text())
    doc = next(d for d in docs if d["kind"] == kind)
    assert dump(spec_model.model_validate(doc["spec"])) == doc["spec"]


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
