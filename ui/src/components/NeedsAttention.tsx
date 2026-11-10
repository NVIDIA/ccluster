// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import type { Attention } from "../api";

const kindLabels: Record<Attention["kind"], string> = {
  CCLInfraPlane: "Infra plane",
  CCLComponentSet: "Component set",
};

export function NeedsAttention({ entries, showNamespace }: { entries: Attention[]; showNamespace: boolean }) {
  return (
    <section className="panel" aria-labelledby="attention-heading">
      <h2 id="attention-heading">Needs attention</h2>
      {entries.length === 0 ? (
        <p className="all-clear">
          <span aria-hidden="true">✓ </span>Nothing needs attention.
        </p>
      ) : (
        <ul className="attention">
          {entries.map((entry, index) => (
            <li key={`${entry.kind}/${entry.namespace}/${entry.name}/${entry.component ?? ""}/${index}`}>
              <div className="attention-what">
                <span className="attention-kind">{kindLabels[entry.kind] ?? entry.kind}</span>{" "}
                <strong>
                  {showNamespace && `${entry.namespace}/`}
                  {entry.name}
                </strong>
                {entry.component && (
                  <>
                    {" "}
                    · component <strong>{entry.component}</strong>
                  </>
                )}
                {entry.reason && <span className="attention-reason">{entry.reason}</span>}
              </div>
              {entry.message && <p className="attention-message">{entry.message}</p>}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
