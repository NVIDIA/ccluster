// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import { Fragment } from "react";
import type { Summary } from "../api";

interface Stat {
  label: string;
  value: number;
  /** Flags the value as a problem when it is not zero. */
  alert?: boolean;
}

/** A strip of counts with dividers, as on the platform UI's dashboard. */
function StatStrip({ title, stats }: { title: string; stats: Stat[] }) {
  return (
    <div className="stat-strip" role="group" aria-label={title}>
      <span className="stat-strip-title">{title}</span>
      <dl className="stat-list">
        {stats.map((stat, index) => (
          <Fragment key={stat.label}>
            {index > 0 && <div className="stat-divider" aria-hidden="true" />}
            <div className="stat">
              <dt className="stat-label">{stat.label}</dt>
              <dd className={`stat-value${stat.alert && stat.value > 0 ? " stat-alert" : ""}`}>{stat.value}</dd>
            </div>
          </Fragment>
        ))}
      </dl>
    </div>
  );
}

export function SummaryCards({ summary }: { summary: Summary }) {
  const planes = summary.infraPlanes;
  const sets = summary.componentSets;
  return (
    <section className="stat-strips" aria-label="Summary">
      <StatStrip
        title="Infra planes"
        stats={[
          { label: "Total", value: planes.total },
          { label: "Available", value: planes.available },
          { label: "Verified", value: planes.verified },
        ]}
      />
      <StatStrip
        title="Component sets"
        stats={[
          { label: "Total", value: sets.total },
          { label: "Finished", value: sets.finished },
          { label: "Rolling out", value: sets.rollingOut },
          { label: "Failed", value: sets.failed, alert: true },
          { label: "Terminating", value: sets.terminating },
        ]}
      />
    </section>
  );
}
