from pydantic import computed_field

from catalog.components import BaseAppConfig, HelmValues

CCLUSTER_ROOT_QUEUE = "ccluster-root"
UNLIMITED_GPU_RESOURCE = -1


def org_queue_name(organization_id: int) -> str:
    return f"org-{organization_id}"


class OrgGpuQueueValues(HelmValues):
    organization_id: int
    gpu_quota: int

    @computed_field  # type: ignore[prop-decorator]
    @property
    def queue_name(self) -> str:
        return org_queue_name(self.organization_id)


class KaiGpuQueuesValues(HelmValues):
    org_queues: list[OrgGpuQueueValues]
    ccluster_root_queue: str = CCLUSTER_ROOT_QUEUE
    unlimited_quota: int = UNLIMITED_GPU_RESOURCE


class KaiGpuQueuesConfig(BaseAppConfig):
    values: KaiGpuQueuesValues
    app_path: str = "catalog/src/catalog/kai_gpu_queues"
