# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Defines the interfaces for deployment planner with pydantic"""

from __future__ import annotations

from catalog.cserve_deployment.interface import CServeV2Recipe, DeploymentInputV2, PlanOutputV2

# Nothing has changed between V2 and V3, to keep things simple
# outside of this file we just reference the V2 classes once
# here.
CServeV3Recipe = CServeV2Recipe
DeploymentInputV3 = DeploymentInputV2
PlanOutputV3 = PlanOutputV2
