// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import type { Condition, InfraPlaneRow } from "../api";
import { age, conditionDetail, conditionLook } from "../format";
import { DataTable, EmptyTable, Section, type Column } from "./DataTable";
import { StatusBadge } from "./StatusBadge";

function condition(value: Condition | null) {
  return <StatusBadge look={conditionLook(value)} title={conditionDetail(value)} />;
}

export function InfraPlaneTable({ rows, showNamespace }: { rows: InfraPlaneRow[]; showNamespace: boolean }) {
  const columns: Column<InfraPlaneRow>[] = [
    ...(showNamespace ? [{ header: "Namespace", cell: (row: InfraPlaneRow) => row.namespace }] : []),
    { header: "Name", cell: (row) => row.name, rowHeader: true },
    { header: "Domain", cell: (row) => <span className="mono">{row.domain || "—"}</span> },
    { header: "Available", cell: (row) => condition(row.available) },
    { header: "Cluster config", cell: (row) => condition(row.clusterConfigApplied) },
    { header: "Verified", cell: (row) => condition(row.verified) },
    { header: "DNS", cell: (row) => condition(row.dnsReady) },
    { header: "Age", cell: (row) => <span className="muted">{age(row.created)}</span> },
  ];
  return (
    <Section id="infra-planes" title="Infra planes" description="Clusters registered with the control plane, and whether they are ready for component sets.">
      {rows.length === 0 ? (
        <EmptyTable>
          No infra planes yet. Register a cluster with <code>ccluster register</code>.
        </EmptyTable>
      ) : (
        <DataTable columns={columns} rows={rows} rowKey={(row) => `${row.namespace}/${row.name}`} />
      )}
    </Section>
  );
}
