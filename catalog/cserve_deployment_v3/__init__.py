# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

from pathlib import Path
from typing import Optional, Self

import yaml
from pydantic import model_validator

from catalog.components import BaseAppConfig
from catalog.cserve_deployment_v3.interface import (
    DeploymentInputV3,
    PlanOutputV3,
)
from catalog.inference_deployment_v3 import InferenceDeploymentValues


class CServeV3InferenceDeploymentValues(InferenceDeploymentValues, DeploymentInputV3):
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
        cls, inference_config: InferenceDeploymentValues, cserve_version: Optional[str], plan: PlanOutputV3
    ) -> "CServeV3InferenceDeploymentValues":
        plan_yaml = yaml.dump(plan.model_dump())

        # gpus should match
        assert plan.deployment_input.ngpus == inference_config.resources.requests.accelerator_count

        return cls(
            **plan.deployment_input.model_dump(),
            cserve_version=cserve_version,
            plan_yaml=plan_yaml,
            **inference_config.model_dump(),
        )


class CServeV3InferenceDeploymentConfig(BaseAppConfig):
    values: CServeV3InferenceDeploymentValues
    app_path: str = "catalog/cserve_deployment_v3"
