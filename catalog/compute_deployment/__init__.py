# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

from typing import Dict, Optional

from pydantic import model_validator

from catalog.components import BaseAppConfig, DeploymentBaseHelmValues
from catalog.inference_deployment_v3 import ImagePullSecret


# ComputeDeploymentValues is a Pydantic model that represents the values
# for a compute deployment. The HelmValues base allows the model to be converted
# to a dictionary that can be used as values in a Helm chart in ArgoCD.
class ComputeDeploymentValues(DeploymentBaseHelmValues):
    user_id: int
    deployment_id: int
    name: str
    image_url: str
    # Default keeps older persisted compute app configs parseable; new creates pass this explicitly.
    ssh_injector_image: str = "host.minikube.internal:5001/compute_ssh_launcher_image:latest"
    image_pull_secret: Optional[ImagePullSecret] = None
    deployment_url: Optional[str] = None
    domain: str
    container_port: int
    exposed_port: int
    node_affinity_labels: Dict[str, str]
    env_vars: Dict[str, str] = {}
    storage_class_name: str
    ssh_username: Optional[str] = ""
    ssh_password: Optional[str] = ""
    ssh_github_username: Optional[str] = ""
    ssh_public_key: Optional[str] = ""
    # Default True so old deployments (without this field in app_config) retain logging on resume
    enable_logging: bool = True

    @model_validator(mode="before")
    def check_ssh_credentials(cls, values):
        ssh_password = values.get("ssh_password")
        ssh_github_username = values.get("ssh_github_username")
        ssh_public_key = values.get("ssh_public_key")

        if not ssh_password and not ssh_github_username and not ssh_public_key:
            raise ValueError("At least one of ssh_password, ssh_github_username, or ssh_public_key must be set")

        return values


class ComputeDeploymentConfig(BaseAppConfig):
    values: ComputeDeploymentValues
    app_path: str = "catalog/compute_deployment"
