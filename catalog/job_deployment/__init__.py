# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

from typing import Dict, List, Optional

from catalog.components import BaseAppConfig, DeploymentBaseHelmValues
from catalog.inference_deployment_v3 import ConfigFileMountValues, ImagePullSecret


class JobDeploymentValues(DeploymentBaseHelmValues):
    user_id: int
    deployment_id: int
    name: str
    image_url: str
    image_pull_secret: Optional[ImagePullSecret] = None
    env_vars: Dict[str, str] = {}
    command: Optional[List[str]] = None
    args: Optional[List[str]] = None
    original_command: Optional[str] = None
    node_affinity_labels: Dict[str, str]
    completions: int = 1
    parallelism: int = 1
    backoff_limit: int = 3
    active_deadline_seconds: Optional[int] = None
    enable_logging: bool = True
    config_file: Optional[ConfigFileMountValues] = None


class JobDeploymentConfig(BaseAppConfig):
    values: JobDeploymentValues
    app_path: str = "catalog/job_deployment"
