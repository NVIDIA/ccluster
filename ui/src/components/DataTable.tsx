// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import type { ReactNode } from "react";

export interface Column<T> {
  header: string;
  cell: (row: T) => ReactNode;
  /** Renders the cell as the row's header, for its identifier. */
  rowHeader?: boolean;
}

interface DataTableProps<T> {
  columns: Column<T>[];
  rows: T[];
  rowKey: (row: T) => string;
}

/** A bordered table with a gray header row, in the platform UI's table style. */
export function DataTable<T>({ columns, rows, rowKey }: DataTableProps<T>) {
  return (
    <div className="table-frame">
      <table className="data-table">
        <thead>
          <tr>
            {columns.map((column) => (
              <th key={column.header} scope="col">
                {column.header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr key={rowKey(row)}>
              {columns.map((column) =>
                column.rowHeader ? (
                  <th key={column.header} scope="row">
                    {column.cell(row)}
                  </th>
                ) : (
                  <td key={column.header}>{column.cell(row)}</td>
                ),
              )}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** A section with the platform UI's page-section heading and description. */
export function Section({ id, title, description, children }: { id: string; title: string; description: string; children: ReactNode }) {
  return (
    <section id={id} className="section" aria-labelledby={`${id}-heading`}>
      <div>
        <h2 id={`${id}-heading`} className="section-title">
          {title}
        </h2>
        <p className="section-description">{description}</p>
      </div>
      {children}
    </section>
  );
}

/** The dashed box shown in place of an empty table. */
export function EmptyTable({ children }: { children: ReactNode }) {
  return <p className="empty-table">{children}</p>;
}
