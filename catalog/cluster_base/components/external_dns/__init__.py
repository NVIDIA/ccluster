# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Override schema for the external-dns cluster_base component."""

from pydantic import BaseModel


class ExternalDnsOverrides(BaseModel):
    pass
