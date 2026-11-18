// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import type { Summary } from "../api";

/** One summary box: how many of a kind are active, out of how many. */
function StatBox({ title, active, total }: { title: string; active: number; total: number }) {
  return (
    <div className="stat-strip" role="group" aria-label={title}>
      <span className="stat-strip-title">{title}</span>
      <span className="stat-value">
        {active}
        <span className="stat-total"> / {total}</span>
      </span>
    </div>
  );
}

/** Infra planes counted as active when Available, component sets when Finished. */
export function SummaryCards({ summary }: { summary: Summary }) {
  const planes = summary.infraPlanes;
  const sets = summary.componentSets;
  return (
    <section className="stat-strips" aria-label="Summary">
      <StatBox title="Infra planes" active={planes.available} total={planes.total} />
      <StatBox title="Component sets" active={sets.finished} total={sets.total} />
    </section>
  );
}
