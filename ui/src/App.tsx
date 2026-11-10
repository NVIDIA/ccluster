// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import { useQuery } from "@tanstack/react-query";
import { fetchConfig, fetchOverview } from "./api";
import { ComponentSetTable } from "./components/ComponentSetTable";
import { ErrorPanel } from "./components/ErrorPanel";
import { Header } from "./components/Header";
import { InfraPlaneTable } from "./components/InfraPlaneTable";
import { SummaryCards } from "./components/SummaryCards";
import { clock } from "./format";
import { useNamespace } from "./useNamespace";

const defaultRefreshSeconds = 5;

export function App() {
  const [namespace, setNamespace] = useNamespace();

  const config = useQuery({
    queryKey: ["config"],
    queryFn: ({ signal }) => fetchConfig(signal),
    staleTime: Infinity,
    retry: false,
    // Until the server answers once, keep asking: it may still be starting.
    refetchInterval: (query) => (query.state.data ? false : defaultRefreshSeconds * 1000),
  });
  const refreshSeconds = config.data?.refreshSeconds ?? defaultRefreshSeconds;

  // On a failed refresh TanStack Query keeps the last data, which the page goes on
  // showing under a notice; polling continues, so the page recovers by itself.
  const overview = useQuery({
    queryKey: ["overview", namespace],
    queryFn: ({ signal }) => fetchOverview(namespace, signal),
    enabled: config.isSuccess,
    retry: false,
    refetchInterval: refreshSeconds * 1000,
    refetchIntervalInBackground: false,
  });

  const data = overview.data;
  const showNamespace = data ? data.selected === "" : false;
  const error = config.error ?? overview.error;

  let body;
  if (data) {
    body = (
      <>
        {overview.isError && (
          <p className="refresh-failed" role="status">
            Couldn&apos;t refresh at {clock(overview.errorUpdatedAt)}: {overview.error.message} Showing data from{" "}
            {clock(overview.dataUpdatedAt)}.
          </p>
        )}
        <SummaryCards summary={data.summary} />
        <InfraPlaneTable rows={data.infraPlanes} showNamespace={showNamespace} />
        <ComponentSetTable rows={data.componentSets} showNamespace={showNamespace} />
      </>
    );
  } else if (error) {
    body = (
      <ErrorPanel
        error={error}
        namespace={namespace}
        servedNamespace={config.data?.namespace}
        onNamespace={setNamespace}
      />
    );
  } else {
    body = <p className="loading">Loading…</p>;
  }

  return (
    <div className="page">
      <Header
        config={config.data}
        namespaces={data?.namespaces ?? (namespace ? [namespace] : [])}
        namespace={namespace}
        onNamespace={setNamespace}
        updatedAt={data ? overview.dataUpdatedAt : undefined}
      />
      <main className="content">{body}</main>
      <footer className="footer">
        ccluster {config.data?.version} · read-only · refreshes every {refreshSeconds}s ·{" "}
        <a href="/THIRD_PARTY_LICENSES.md">licenses</a>
      </footer>
    </div>
  );
}
