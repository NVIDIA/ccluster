# Introduction

This module contains:

- Helm Charts for various applications meant to be deployed by ArgoCD programmatically
- Models representing the values for the Helm Charts
- Various utility classes/functions for working with the above


# Adding A New Helm Chart (catalog item)

1. Create your helm chart as you would normally. 
2. Instead of using `values.yaml`, create a __init.py__ in the chart directory
3. The contents of the __init__.py should be a class that inherits from `HelmValues` and represents the values for the Helm Chart. For example:

```python
from typing import Dict
from catalog.components import HelmValues, ResourceLimitsRequests

# InferenceDeploymentValues is a Pydantic model that represents the values 
# for an inference deployment. The HelmValues base allows the model to be converted
# to a dictionary that can be used as values in a Helm chart in ArgoCD.
class InferenceDeploymentValues(HelmValues):
    user_id: int 
    deployment_id: int
    name: str
    image_url: str
    domain: str
    container_port: int
    endpoint_certificate_authority: Optional[str] = None
    min_scale: int
    max_scale: int
    concurrency: Optional[int] = None
    healthcheck: Optional[str] = "/"
    command: Optional[list[str]] = None
    command_args: Optional[list[str]] = None
    node_affinity_labels: Dict[str, str]
    env_vars: Dict[str, str] = {}
    resources: ResourceLimitsRequests
```

# Code Style

## Prevent YAML Misinterpretation

YAML can misinterpret certain values (e.g., `true`, `false`, `yes`, `no`, numbers) as non-strings. To avoid issues, always quote dynamic string values in Helm templates.

**Always Quote Strings**: Use double quotes around values that meant to be string.
```yaml
name: "{{ .Values.name }}"
namespace: "user-{{ .Values.user_id }}"
```
