// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import type { ComponentRow, ComponentSetRow } from "../api";
import { age, healthLabel, healthLook, setStatusLook } from "../format";
import { Badge } from "./Badge";

/** One segment per component, in rollout order, coloured and titled by its health. */
function ComponentBar({ components }: { components: ComponentRow[] }) {
  const done = components.filter((c) => c.health === "Healthy" || c.health === "Succeeded").length;
  return (
    <div className="component-cell">
      <div className="component-bar" aria-hidden="true">
        {components.map((component) => (
          <span
            key={component.name}
            className={`segment segment-${healthLook(component.health).tone}`}
            title={`${component.name}: ${healthLabel(component.health)}`}
          />
        ))}
      </div>
      <span className="component-count">
        {done} of {components.length} healthy
      </span>
    </div>
  );
}

export function ComponentSetTable({ rows, showNamespace }: { rows: ComponentSetRow[]; showNamespace: boolean }) {
  return (
    <section className="panel" aria-labelledby="sets-heading">
      <h2 id="sets-heading">
        Component sets <span className="count">{rows.length}</span>
      </h2>
      {rows.length === 0 ? (
        <p className="empty">No component sets here.</p>
      ) : (
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                {showNamespace && <th scope="col">Namespace</th>}
                <th scope="col">Name</th>
                <th scope="col">Status</th>
                <th scope="col">Components</th>
                <th scope="col">Message</th>
                <th scope="col">Age</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr key={`${row.namespace}/${row.name}`}>
                  {showNamespace && <td>{row.namespace}</td>}
                  <th scope="row">{row.name}</th>
                  <td>
                    <Badge look={setStatusLook(row.status)} title={row.progressing?.reason} />
                  </td>
                  <td>
                    <ComponentBar components={row.components} />
                  </td>
                  <td className="message">{row.message || "—"}</td>
                  <td className="muted">{age(row.created)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}
