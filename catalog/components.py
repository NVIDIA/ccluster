# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import re
from enum import StrEnum
from pathlib import Path
from typing import Any, Optional

import yaml
from pydantic import (
    BaseModel,
    ByteSize,
    NonNegativeInt,
    field_serializer,
    field_validator,
    model_validator,
)


# Helper function to convert CPU string to integer (in millicpu)
def cpu_resource_int(cpu_str: str) -> int:
    if cpu_str.endswith("m"):
        # Millicpu (e.g., "500m" -> 500)
        return int(cpu_str[:-1])  # Remove the "m" and convert to int
    else:
        # Full CPUs (e.g., "2" -> 2000 millicpu)
        return int(cpu_str) * 1000


# Helper function to convert memory string to integer (in MiB)
def memory_resource_int(memory_str: str) -> int:
    if memory_str.endswith("Mi"):
        return int(memory_str[:-2])  # Remove the "Mi" and convert to int
    else:
        raise ValueError(f"Invalid memory format: {memory_str}")


# Helper function to convert integer to the strings that Kubernetes expects for cpu.
# CPU is in millicpu.
def cpu_resource_str(cpu: int) -> str:
    # If we use millicpu ('m') when cpu is multiple of 1000, Kubernetes will just
    # convert it to number of full cpus and remove the 'm'. ArgoCD will think that
    # the value has changed and thus indicate that the application is out of sync.
    # To prevent this, we pre-convert the value to cpu when it is multiple of 1000
    # so that ArgoCD will not mistakenly think that the value has changed.
    if cpu % 1000 == 0:
        return f"{int(cpu / 1000)}"
    return f"{cpu}m"


# Helper function to convert integer to the strings that Kubernetes expects for memory.
# Memory is in MiB.
def memory_resource_str(memory: int) -> str:
    return f"{memory}Mi"


# Resources model is used to define the resources for pods. In order to prevent
# synchronization issues between Argo and Kubernetes, we need to convert the
# integer values to the strings as Kubernetes does automatically.
class Resources(BaseModel):
    accelerator_count: Optional[int] = None  # Number of accelerators
    accelerator_key: str = "nvidia.com/gpu"  # Type of accelerator (e.g., "nvidia.com/gpu")
    gpus: int = 1  # DEPRECATED: Use accelerator_count instead. Number of GPUs
    cpu: int = 0  # CPU is in millicpu.
    memory: int = 0  # Memory is in MiB.

    infiniband_count: NonNegativeInt = 0
    """The number of IB cards to request."""

    infiniband_key: str = "nvidia.com/ib"
    """The CDIDevice name of the IB cards to request."""

    # deserializes from string
    @field_validator("cpu", mode="before")
    @classmethod
    def cpu_deserializer(cls, value):
        if isinstance(value, str):
            return cpu_resource_int(value)
        return value

    # deserializes from string
    @field_validator("memory", mode="before")
    @classmethod
    def memory_deserializer(cls, value):
        if isinstance(value, str):
            return memory_resource_int(value)
        return value

    @model_validator(mode="after")
    def set_accelerator_count(self):
        # Set accelerator_count to match gpus if not explicitly provided
        if self.accelerator_count is None:
            self.accelerator_count = self.gpus
        return self

    @field_serializer("cpu")
    def cpu_serializer(self, cpu: int) -> str:
        return cpu_resource_str(cpu)

    @field_serializer("memory")
    def memory_serializer(self, memory: int) -> str:
        return memory_resource_str(memory)


# ResourceLimitsRequests model is used to define the resource limits and requests for pods.
# It contains two Resources objects, one for limits and one for requests.
class ResourceLimitsRequests(BaseModel):
    limits: Resources
    requests: Resources


class AccessMode(StrEnum):
    """An enum that represents the access modes of a PVC.

    For the definition of each access mode of a PVC, please refer to:

        https://kubernetes.io/docs/concepts/storage/persistent-volumes/#access-modes

    """

    READ_WRITE_ONCE = "ReadWriteOnce"
    READ_ONLY_MANY = "ReadOnlyMany"
    READ_WRITE_MANY = "ReadWriteMany"
    READ_WRITE_ONCE_POD = "ReadWriteOncePod"


class PersistentVolumeClaimToMount(BaseModel):
    """Specifies the spec of a PVC that already exists and is to be mounted."""

    name: str
    """The name of this PVC."""

    mount_path: Path
    """Where inside the container to mount the PVC."""

    read_only: bool
    """This does not make the volume itself read-only, but that specific container will
    not be able to write to it. Other containers in the Pod may mount the same volume as
    read-write.
    """


class PersistentVolumeClaimToCreateAndMount(PersistentVolumeClaimToMount):
    """Specifies the spec of a PVC that will be created and mounted."""

    access_modes: list[AccessMode]
    """The list of access modes granted to this PVC."""

    size: ByteSize
    """The size of the PVC in MiB."""

    storage_class_name: str
    """The name of the storage class used to provision this PVC."""


class SecurityContext(BaseModel):
    run_as_user: int
    """For any containers in the pods, all processes run with this UID."""

    run_as_group: int
    """The primary GID for all processes within any containers of the pods. If this
    field is omitted, the primary group ID of the containers will be root(0). Any files
    created will also be owned by the UID `run_as_user` and this GID."""

    fs_group: int
    """All processes of the container are also part of this GID `fs_group`. The owner
    for volumes and any files created in that volume will be this GID `fs_group`."""


# HelmValues is a base class that provides a method to convert the Pydantic model to a list
# of dictionaries that can be used as Helm parameters in Argo CD. It also provides a method
# to convert the Pydantic model to YAML for the values.yaml file.
class HelmValues(BaseModel):
    def _flatten_yaml(self, d: Any, parent_key: str = "", sep: str = ".") -> dict[str, Any]:
        if isinstance(d, dict):
            return self._flatten_yaml_dict(d=d, parent_key=parent_key, sep=sep)
        elif isinstance(d, (list, tuple)):
            return self._flatten_yaml_list(d=d, parent_key=parent_key, sep=sep)
        else:
            return {parent_key: d}

    def _flatten_yaml_dict(self, d: dict[str, Any], parent_key="", sep=".") -> dict[str, Any]:
        flat_dict = {}
        for k, v in d.items():
            new_key = f"{parent_key}{sep}{k}" if parent_key else k
            flat_dict.update(self._flatten_yaml(d=v, parent_key=new_key, sep=sep))
        return flat_dict

    def _flatten_yaml_list(self, d: list | tuple, parent_key="", sep=".") -> dict[str, Any]:
        flat_dict = {}
        for i, v in enumerate(d):
            new_key = f"{parent_key}[{i}]"
            flat_dict.update(self._flatten_yaml(d=v, parent_key=new_key, sep=sep))
        return flat_dict

    def helm_params(self) -> list[dict[str, str | bool | None]]:
        # JSON is a subset of YAML
        yaml_content = yaml.safe_load(self.model_dump_json(by_alias=True))
        flat_dict = self._flatten_yaml(yaml_content)
        params = []
        for k, v in flat_dict.items():
            # https://argo-cd.readthedocs.io/en/stable/user-guide/application-specification/
            # We force Argo CD to parse everything that's a string as a string.
            # We need forceString to make sure argo do not parse password as number etc.
            params.append(
                {
                    "name": k,
                    "value": str(v) if v is not None else None,
                    "forceString": isinstance(v, str),
                }
            )
        return params

    def yaml(self):
        return yaml.dump(self.model_dump(by_alias=True))


# DevShm provides the fields for dev_shm_size, and a method to set the default value for
# dev_shm_size if it is not provided.
class DevShm(BaseModel):
    dev_shm_size: int | None = None

    # /dev/shm size is formatted the same as in k8s, this formats it
    @field_serializer("dev_shm_size")
    def memory_serializer(self, memory: int) -> str:
        return memory_resource_str(memory)

    # deserializes from string, dev_shm is formatted the same as memory in k8s
    @field_validator("dev_shm_size", mode="before")
    @classmethod
    def memory_deserializer(cls, value):
        if isinstance(value, str):
            return memory_resource_int(value)
        return value

    # We default dev_shm_size to 30% of memory if it is not provided.
    @model_validator(mode="after")
    def set_default_dev_shm_size(self):
        """
        Sets 'dev_shm_size' to 30% of resources.request.memory if 'dev_shm_size' is not provided.
        """
        if self.dev_shm_size is None:
            memory = self.resources.requests.memory
            self.dev_shm_size = int(memory * 0.3)  # 30% of memory
        return self


# ResourceRequirements provides the common fields of resources.
class ResourceRequirements(BaseModel):
    resources: ResourceLimitsRequests
    supplemental_group_ids: list[int] | None = None


class CommonJobSpec(BaseModel):
    """Specifies the common fields that are relevant to a job in a K8s cluster."""

    user_id: int
    """The user ID."""

    deployment_id: int
    """The deployment/job ID (i.e., key into Platform DB) of this job."""

    name: str
    """The name of this job."""

    image_url: str
    """The container image URL of this job."""

    working_dir: Optional[Path] = None
    """The container's working directory. If not specified, the container runtime's
    default will be used, which might be configured in the container image.
    """

    command: list[str]
    """The CLI command to run."""

    command_args: list[str]
    """The CLI arguments to be appended to the above command."""

    env_vars: dict[str, str] = {}
    """The environment variables to set in the container."""

    pvcs_to_create_and_mount: dict[str, PersistentVolumeClaimToCreateAndMount] = {}
    """The PVCs to create and mount into the job container. The helm chart owns these
    PVCs; hence, once the chart is deleted, these PVCs are deleted as well.

    The key is the name of the PVC.
    """

    pvcs_to_mount: dict[str, PersistentVolumeClaimToMount] = {}
    """In addition to `pvcs_to_create_and_mount` above, the PVCs that were already
    created to mount into the job container. The helm chart does NOT own these PVCs.

    The key is the name of the PVC.
    """

    security_context: SecurityContext = SecurityContext(
        run_as_user=1024,
        run_as_group=1024,
        fs_group=1024,
    )
    """Specifies the security context."""


# DeploymentBaseHelmValues is a base class that provides the common fields for deployment values.
# Specifically, it provides the fields for dev_shm_size and resources. It also provides a method
# to set the default value for dev_shm_size if it is not provided.
class DeploymentBaseHelmValues(DevShm, ResourceRequirements, HelmValues):
    user_annotations: dict[str, str] | None = None
    # Scheduler name injected into pod spec, defaulting to "kai-scheduler" for
    # GPU bin-packing. Set to "" to fall back to the cluster default scheduler.
    scheduler_name: Optional[str] = "kai-scheduler"

    def helm_params(self) -> list[dict[str, str | bool | None]]:
        # Dots in user_annotations keys are the K8s annotation separator but ALSO
        # Helm --set's nesting separator. Escape them at emit time so Helm parses
        # them as literal characters. We do this here (not at model construction)
        # so the DB preserves the user's raw keys — round-trips via GET/PUT and
        # deployment clone return what the user originally typed.
        if not self.user_annotations:
            return super().helm_params()
        escaped = self.model_copy(
            update={"user_annotations": {re.escape(k): v for k, v in self.user_annotations.items()}}
        )
        return HelmValues.helm_params(escaped)


class BaseAppConfig(BaseModel):
    values: HelmValues
    app_path: str
