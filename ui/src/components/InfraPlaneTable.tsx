// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import type { Condition, InfraPlaneRow } from "../api";
import { age, conditionDetail, conditionLook } from "../format";
import { Badge } from "./Badge";

function ConditionCell({ condition }: { condition: Condition | null }) {
  return (
    <td>
      <Badge look={conditionLook(condition)} title={conditionDetail(condition)} />
    </td>
  );
}

export function InfraPlaneTable({ rows, showNamespace }: { rows: InfraPlaneRow[]; showNamespace: boolean }) {
  return (
    <section className="panel" aria-labelledby="planes-heading">
      <h2 id="planes-heading">
        Infra planes <span className="count">{rows.length}</span>
      </h2>
      {rows.length === 0 ? (
        <p className="empty">
          No infra planes here. Register a cluster with <code>ccluster register</code>.
        </p>
      ) : (
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                {showNamespace && <th scope="col">Namespace</th>}
                <th scope="col">Name</th>
                <th scope="col">Domain</th>
                <th scope="col">Available</th>
                <th scope="col">Cluster config</th>
                <th scope="col">Verified</th>
                <th scope="col">DNS</th>
                <th scope="col">Age</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr key={`${row.namespace}/${row.name}`}>
                  {showNamespace && <td>{row.namespace}</td>}
                  <th scope="row">{row.name}</th>
                  <td className="mono">{row.domain || "—"}</td>
                  <ConditionCell condition={row.available} />
                  <ConditionCell condition={row.clusterConfigApplied} />
                  <ConditionCell condition={row.verified} />
                  <ConditionCell condition={row.dnsReady} />
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
