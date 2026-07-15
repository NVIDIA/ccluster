# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Override schema for the metrics-server cluster_base component."""

from pydantic import BaseModel


class MetricsServerOverrides(BaseModel):
    pass
