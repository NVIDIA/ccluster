// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import { Badge } from "@nvidia/foundations-react-core";
import type { StatusLook } from "../format";

/** A Kaizen Badge for a status; the label carries the meaning, the color reinforces it. */
export function StatusBadge({ look, title, kind = "outline" }: { look: StatusLook; title?: string; kind?: "outline" | "solid" }) {
  return (
    <Badge color={look.color} kind={kind} title={title}>
      {look.label}
    </Badge>
  );
}
