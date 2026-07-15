# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Override schema for the istio cluster_base component."""

from typing import Optional

from pydantic import BaseModel, Field


class IstioCniOverrides(BaseModel):
    cni_bin_dir: Optional[str] = Field(default=None, alias="cniBinDir")
    cni_conf_dir: Optional[str] = Field(default=None, alias="cniConfDir")

    model_config = {"populate_by_name": True}


class IstioCniSubchartOverrides(BaseModel):
    cni: Optional[IstioCniOverrides] = None

    model_config = {"populate_by_name": True}


class IstioOverrides(BaseModel):
    # Writable CNI paths vary by distro. Defaults in values.yaml target
    # k3s/k3d; override here for GKE (cniBinDir=/home/kubernetes/bin),
    # EKS / kind / kubeadm (cniBinDir=/opt/cni/bin), etc. cniConfDir is
    # /etc/cni/net.d on most distros.
    istio_cni: Optional[IstioCniSubchartOverrides] = Field(default=None, alias="istio-cni")

    model_config = {"populate_by_name": True}
