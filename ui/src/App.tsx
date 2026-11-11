// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import { useQuery } from "@tanstack/react-query";
import { Banner, Flex, Spinner } from "@nvidia/foundations-react-core";
import { fetchConfig, fetchOverview } from "./api";
import { AppShell } from "./components/AppShell";
import { ComponentSetTable } from "./components/ComponentSetTable";
import { ErrorPanel } from "./components/ErrorPanel";
import { InfraPlaneTable } from "./components/InfraPlaneTable";
import { SummaryCards } from "./components/SummaryCards";
import { NamespaceSelect } from "./components/NamespaceSelect";
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
          <div role="status">
            <Banner kind="inline" status="warning">
              Couldn&apos;t refresh at {clock(overview.errorUpdatedAt)}: {overview.error.message} Showing data from{" "}
              {clock(overview.dataUpdatedAt)}.
            </Banner>
          </div>
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
    body = (
      <Flex justify="center" paddingY="12">
        <Spinner aria-label="Loading" />
      </Flex>
    );
  }

  return (
    <AppShell
      config={config.data}
      updatedAt={data ? overview.dataUpdatedAt : undefined}
      controls={
        <NamespaceSelect
          lockedNamespace={config.data?.namespace || undefined}
          namespaces={data?.namespaces ?? (namespace ? [namespace] : [])}
          namespace={namespace}
          onNamespace={setNamespace}
        />
      }
    >
      <div className="page">
        <div>
          <h1 className="page-title">Overview</h1>
          <p className="section-description">
            Infra planes and component sets on the control plane
            {config.data?.context ? ` ${config.data.context}` : ""}, read with your kubeconfig credentials.
          </p>
        </div>
        {body}
      </div>
    </AppShell>
  );
}
