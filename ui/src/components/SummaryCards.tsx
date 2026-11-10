// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import type { Summary } from "../api";
import { healthLook } from "../format";
import { Badge } from "./Badge";

function Stat({ value, label, tone }: { value: number; label: string; tone?: string }) {
  return (
    <div className={`stat${tone && value > 0 ? ` stat-${tone}` : ""}`}>
      <span className="stat-value">{value}</span>
      <span className="stat-label">{label}</span>
    </div>
  );
}

export function SummaryCards({ summary }: { summary: Summary }) {
  const planes = summary.infraPlanes;
  const sets = summary.componentSets;
  return (
    <section className="cards" aria-label="Summary">
      <article className="card">
        <h2>Infra planes</h2>
        <div className="stats">
          <Stat value={planes.total} label="total" />
          <Stat value={planes.available} label="available" tone="ok" />
          <Stat value={planes.verified} label="verified" tone="ok" />
        </div>
      </article>
      <article className="card">
        <h2>Component sets</h2>
        <div className="stats">
          <Stat value={sets.finished} label="finished" tone="ok" />
          <Stat value={sets.rollingOut} label="rolling out" tone="progress" />
          <Stat value={sets.failed} label="failed" tone="bad" />
          <Stat value={sets.terminating} label="terminating" tone="warn" />
        </div>
      </article>
      <article className="card">
        <h2>Components</h2>
        {summary.components.length === 0 ? (
          <p className="muted">None yet.</p>
        ) : (
          <ul className="health-counts">
            {summary.components.map(({ health, count }) => (
              <li key={health}>
                <span className="health-count">{count}</span>
                <Badge look={healthLook(health)} />
              </li>
            ))}
          </ul>
        )}
      </article>
    </section>
  );
}
