from typing import Dict, Optional

from catalog.components import BaseAppConfig, DeploymentBaseHelmValues


# InferenceDeploymentValues is a Pydantic model that represents the values
# for an inference deployment. The HelmValues base allows the model to be converted
# to a dictionary that can be used as values in a Helm chart in ArgoCD.
class InferenceDeploymentValues(DeploymentBaseHelmValues):
    user_id: int
    deployment_id: int
    name: str
    image_url: str
    domain: str
    deployment_url: Optional[str] = None
    container_port: int
    endpoint_certificate_authority: Optional[str] = None
    endpoint_bearer_token: Optional[str] = None
    min_scale: int
    max_scale: int
    concurrency: Optional[int] = None
    healthcheck: Optional[str] = "/"
    command: Optional[list[str]] = None
    command_args: Optional[list[str]] = None
    node_affinity_labels: Dict[str, str]
    env_vars: Dict[str, str] = {}
    # cluster_cache: Dict[str, Any] = {"enabled": False}
    cluster_cache_mount_path: Optional[str] = None


class InferenceDeploymentConfig(BaseAppConfig):
    values: InferenceDeploymentValues
    app_path: str = "catalog/src/catalog/inference_deployment"
