// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import type { BadgeLook, Tone } from "../format";

const icons: Record<Tone, string> = {
  ok: "✓",
  bad: "✕",
  warn: "!",
  progress: "↻",
  neutral: "–",
};

/** A status with an icon and a text label, so colour is never the only signal. */
export function Badge({ look, title }: { look: BadgeLook; title?: string }) {
  return (
    <span className={`badge badge-${look.tone}`} title={title}>
      <span className="badge-icon" aria-hidden="true">
        {icons[look.tone]}
      </span>
      {look.label}
    </span>
  );
}
