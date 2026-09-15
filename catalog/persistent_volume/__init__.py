from typing import Annotated, Literal, Optional

from pydantic import Field

from catalog.components import BaseAppConfig, HelmValues


# Plain HelmValues (not DeploymentBaseHelmValues): the chart renders no pod, so the
# deployment base's mandatory resources/dev_shm/scheduler fields do not apply.
class PersistentVolumeValuesBase(HelmValues):
    user_id: int
    volume_id: int
    name: str


class FilesystemPersistentVolumeValues(PersistentVolumeValuesBase):
    backend: Literal["filesystem"] = "filesystem"
    size_gb: int
    storage_class_name: str


class ObjectPersistentVolumeValues(PersistentVolumeValuesBase):
    backend: Literal["object"] = "object"
    bucket: str
    region: str
    prefix: Optional[str] = None
    read_only: bool = False


PersistentVolumeValues = Annotated[
    FilesystemPersistentVolumeValues | ObjectPersistentVolumeValues,
    Field(discriminator="backend"),
]


class PersistentVolumeConfig(BaseAppConfig):
    values: PersistentVolumeValues
    app_path: str = "catalog/src/catalog/persistent_volume"
