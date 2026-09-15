# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

from enum import StrEnum
from pathlib import Path
from typing import Optional

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


class HelmValues(BaseModel):
    def helm_values_yaml(self) -> str:
        """Serialize to a JSON string for the ArgoCD helm.values field.

        JSON is a subset of YAML, so Helm/ArgoCD consume it directly, and its
        unconditional string quoting keeps number-like values (zero-padded ids,
        "1e-5", octal-looking file modes) from being coerced to numbers by Helm's
        Go YAML parser. by_alias runs the Pydantic field_serializers (CPU -> "2",
        memory -> "4000Mi").
        """
        return self.model_dump_json(by_alias=True)


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


# DeploymentSharedHelmValues contains deployment-wide fields that are not owned by
# any particular pod or worker pool. Most charts have one homogeneous workload and
# continue to use DeploymentBaseHelmValues below; multi-pool charts can inherit this
# narrower base and place resources/dev_shm_size on their component values instead.
class DeploymentSharedHelmValues(HelmValues):
    user_annotations: dict[str, str] | None = None
    # Scheduler name injected into pod spec, defaulting to "kai-scheduler" for
    # GPU bin-packing. Set to "" to fall back to the cluster default scheduler.
    scheduler_name: Optional[str] = "kai-scheduler"
    # Leaf queue name for label kai.scheduler/queue. Unset keeps pods on the
    # chart default-queue (unlimited). Set to org-{organization_id} when org
    # Queue CRs exist.
    kai_queue: Optional[str] = None
    # Kubernetes PriorityClass name on the pod spec. Omit to leave unset.
    priority_class_name: Optional[str] = None


# DeploymentBaseHelmValues is the legacy homogeneous-workload base. It provides
# dev_shm_size and resources in addition to the deployment-wide shared fields, and
# defaults dev_shm_size from the single workload's requested memory.
class DeploymentBaseHelmValues(DevShm, ResourceRequirements, DeploymentSharedHelmValues):
    pass


class BaseAppConfig(BaseModel):
    values: HelmValues
    app_path: str
