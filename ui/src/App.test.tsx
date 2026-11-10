// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Config, Overview } from "./api";
import { App } from "./App";

const config: Config = { context: "kind-cp", version: "v0.9.0", namespace: "", refreshSeconds: 5 };

function overview(selected = ""): Overview {
  return {
    namespaces: ["team-a", "team-b"],
    selected,
    summary: {
      infraPlanes: { total: 1, available: 1, verified: 0 },
      componentSets: { total: 1, finished: 0, rollingOut: 0, failed: 1, terminating: 0 },
      components: [
        { health: "Degraded", count: 1 },
        { health: "NotReleased", count: 1 },
      ],
    },
    needsAttention: [
      { kind: "CCLComponentSet", namespace: "team-a", name: "apps", component: "web", reason: "CrashLoopBackOff", message: "back-off restarting" },
    ],
    infraPlanes: [
      {
        namespace: "team-a",
        name: "gpu",
        domain: "gpu.team-a.example.com",
        available: { status: "True", reason: "ClusterSecretCreated" },
        clusterConfigApplied: { status: "True" },
        verified: null,
        dnsReady: { status: "False", reason: "LoadBalancerPending", message: "waiting" },
        created: "2026-11-09T10:00:00Z",
      },
    ],
    componentSets: [
      {
        namespace: "team-a",
        name: "apps",
        status: "Failed",
        applied: { status: "True" },
        progressing: { status: "True", reason: "WaveSyncFailed" },
        message: "web failed to sync",
        components: [
          { name: "web", chart: "catalogs/web", infraPlane: "gpu", health: "Degraded" },
          { name: "db", chart: "catalogs/db", infraPlane: "gpu", health: "NotReleased" },
        ],
        created: "2026-11-09T10:00:00Z",
      },
    ],
  };
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

type Handler = (url: URL) => Response | Promise<Response>;

function mockFetch(handler: Handler) {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://127.0.0.1:8484");
    if (url.pathname === "/api/v1/config") {
      return Promise.resolve(json(config));
    }
    return Promise.resolve(handler(url));
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function renderApp() {
  const client = new QueryClient({ defaultOptions: { queries: { refetchInterval: false } } });
  render(
    <QueryClientProvider client={client}>
      <App />
    </QueryClientProvider>,
  );
  return client;
}

beforeEach(() => {
  window.history.replaceState(null, "", "/");
});

describe("App", () => {
  it("shows the summary, what needs attention and both tables", async () => {
    mockFetch(() => json(overview()));
    renderApp();

    const planes = await screen.findByRole("region", { name: /Infra planes/ });
    const planeRow = within(planes).getByRole("row", { name: /gpu/ });
    expect(within(planeRow).getByText("gpu.team-a.example.com")).toBeInTheDocument();
    expect(within(planeRow).getByText("Pending")).toBeInTheDocument();
    expect(within(planeRow).getByText("False")).toHaveAttribute("title", "LoadBalancerPending: waiting");
    // All namespaces: a Namespace column.
    expect(within(planes).getByRole("columnheader", { name: "Namespace" })).toBeInTheDocument();

    const sets = screen.getByRole("region", { name: /Component sets/ });
    expect(within(sets).getByText("Failed")).toBeInTheDocument();
    expect(within(sets).getByText("0 of 2 healthy")).toBeInTheDocument();
    expect(within(sets).getByText("web failed to sync")).toBeInTheDocument();

    const attention = screen.getByRole("region", { name: "Needs attention" });
    expect(within(attention).getByText("CrashLoopBackOff")).toBeInTheDocument();
    expect(within(attention).getByText("back-off restarting")).toBeInTheDocument();

    expect(screen.getByText("Not released")).toBeInTheDocument();
    expect(screen.getByText("kind-cp")).toBeInTheDocument();
  });

  it("keeps the last data with a notice when a refresh fails", async () => {
    let fail = false;
    mockFetch(() => (fail ? json({ status: 502, reason: "KubernetesUnavailable", message: "connection refused" }, 502) : json(overview())));
    const client = renderApp();
    await screen.findByText("gpu.team-a.example.com");

    fail = true;
    await client.refetchQueries({ queryKey: ["overview"] });

    expect(await screen.findByRole("status")).toHaveTextContent(/Couldn.t refresh at .*connection refused/);
    expect(screen.getByText("gpu.team-a.example.com")).toBeInTheDocument();
  });

  it("explains a cluster-wide 403 and suggests -n", async () => {
    mockFetch(() =>
      json({ status: 403, reason: "ClusterReadForbidden", message: 'User "dev" cannot list resource "cclinfraplanes"' }, 403),
    );
    renderApp();

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Cannot read all namespaces");
    expect(alert).toHaveTextContent("ccluster server -n <namespace>");
    expect(alert).toHaveTextContent('User "dev" cannot list resource "cclinfraplanes"');
  });

  it("opens the namespace picked in the dropdown", async () => {
    const fetchMock = mockFetch((url) => json(overview(url.searchParams.get("namespace") ?? "")));
    renderApp();
    await screen.findByText("gpu.team-a.example.com");

    await userEvent.selectOptions(screen.getByRole("combobox", { name: "Namespace" }), "team-b");

    expect(window.location.search).toBe("?namespace=team-b");
    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith("/api/v1/overview?namespace=team-b", expect.anything()),
    );
    await waitFor(() => expect(screen.queryByRole("columnheader", { name: "Namespace" })).not.toBeInTheDocument());
  });
});
