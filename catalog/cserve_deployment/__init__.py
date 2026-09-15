from pathlib import Path
from typing import Optional, Self

import yaml
from pydantic import model_validator

from catalog.components import BaseAppConfig
from catalog.cserve_deployment.interface import (
    DeploymentInputV2,
    PlanOutput,
    PlanOutputV2,
)
from catalog.inference_deployment import InferenceDeploymentValues


# CServeInferenceDeploymentValues is a Pydantic model that represents the values
# for an CServer inference deployment. The HelmValues base allows the model to be converted
# to a dictionary that can be used as values in a Helm chart in ArgoCD.
class CServeInferenceDeploymentValues(InferenceDeploymentValues, PlanOutput):
    plan_yaml: str

    @classmethod
    def from_plan(
        cls, inference_config: InferenceDeploymentValues, plan: PlanOutput
    ) -> "CServeInferenceDeploymentValues":
        plan_yaml = yaml.dump({"deployment_input": plan.model_dump()})

        # gpus should match
        assert plan.ngpus == inference_config.resources.requests.accelerator_count

        return cls(**plan.model_dump(), plan_yaml=plan_yaml, **inference_config.model_dump())


class CServeInferenceDeploymentConfig(BaseAppConfig):
    values: CServeInferenceDeploymentValues
    app_path: str = "catalog/src/catalog/cserve_deployment"


class CServeV2InferenceDeploymentValues(InferenceDeploymentValues, DeploymentInputV2):
    plan_yaml: str
    cserve_version: Optional[str] = None
    plan_yaml_mount_path: Path = Path("/etc/cserve")
    plan_yaml_filename: str = "plan.yaml"

    @model_validator(mode="after")
    def set_default_command_and_args(self) -> Self:
        if not self.command and not self.command_args:
            self.command = ["/bin/tini"]
            self.command_args = [
                "--",
                "/bin/bash",
                "-c",
                f"./examples/launch_services.sh --plan {self.plan_yaml_mount_path / self.plan_yaml_filename}",
            ]
        return self

    @model_validator(mode="after")
    def set_default_env_vars(self) -> Self:
        self.env_vars.setdefault("CSERVE_METRICS_PORT", "18000")
        return self

    @classmethod
    def from_plan(
        cls, inference_config: InferenceDeploymentValues, cserve_version: Optional[str], plan: PlanOutputV2
    ) -> "CServeV2InferenceDeploymentValues":
        plan_yaml = yaml.dump(plan.model_dump())

        # gpus should match
        assert plan.deployment_input.ngpus == inference_config.resources.requests.accelerator_count

        return cls(
            **plan.deployment_input.model_dump(),
            cserve_version=cserve_version,
            plan_yaml=plan_yaml,
            **inference_config.model_dump(),
        )


class CServeV2InferenceDeploymentConfig(BaseAppConfig):
    values: CServeV2InferenceDeploymentValues
    app_path: str = "catalog/src/catalog/cserve_deployment"
