// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import type { ComponentRow, ComponentSetRow } from "../api";
import { age, healthLabel, healthLook, removalLabel, setStatusLook } from "../format";
import { DataTable, EmptyTable, Section, type Column } from "./DataTable";
import { StatusBadge } from "./StatusBadge";

/**
 * One segment per component, in rollout order, colored and titled by its health, then a
 * hatched segment per removed component whose chart is still being uninstalled. Only the
 * components in the spec count towards healthy.
 */
function ComponentBar({ components }: { components: ComponentRow[] }) {
  const current = components.filter((c) => !c.removal);
  const removing = components.length - current.length;
  const done = current.filter((c) => c.health === "Healthy" || c.health === "Succeeded").length;
  return (
    <span className="component-cell">
      <span className="component-bar" aria-hidden="true">
        {components.map((component) => (
          <span
            key={component.name}
            className={
              component.removal ? "segment segment-removing" : `segment segment-${healthLook(component.health).color}`
            }
            title={`${component.name}: ${
              component.removal ? removalLabel(component.removal) : healthLabel(component.health)
            }`}
          />
        ))}
      </span>
      <span className="muted tabular">
        {done} of {current.length} healthy
        {removing > 0 && `, ${removing} removing`}
      </span>
    </span>
  );
}

export function ComponentSetTable({ rows, showNamespace }: { rows: ComponentSetRow[]; showNamespace: boolean }) {
  const columns: Column<ComponentSetRow>[] = [
    ...(showNamespace ? [{ header: "Namespace", cell: (row: ComponentSetRow) => row.namespace }] : []),
    { header: "Name", cell: (row) => row.name, rowHeader: true },
    {
      header: "Status",
      cell: (row) => <StatusBadge kind="solid" look={setStatusLook(row.status)} title={row.progressing?.reason} />,
    },
    { header: "Components", cell: (row) => <ComponentBar components={row.components} /> },
    { header: "Message", cell: (row) => <span className="muted message">{row.message || "—"}</span> },
    { header: "Age", cell: (row) => <span className="muted">{age(row.created)}</span> },
  ];
  return (
    <Section id="component-sets" title="Component sets" description="Ordered bundles of catalog charts and how far each has rolled out.">
      {rows.length === 0 ? (
        <EmptyTable>No component sets yet.</EmptyTable>
      ) : (
        <DataTable columns={columns} rows={rows} rowKey={(row) => `${row.namespace}/${row.name}`} />
      )}
    </Section>
  );
}
