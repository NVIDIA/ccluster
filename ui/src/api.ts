// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// The JSON API of ccluster server. These types mirror the Go structs in
// internal/server/server.go and internal/server/overview.go; change both together.

export type ConditionStatus = "True" | "False" | "Unknown";

export interface Condition {
  status: ConditionStatus;
  reason?: string;
  message?: string;
}

export interface Config {
  context: string;
  version: string;
  /** The only namespace the server reads, or "" for all of them. */
  namespace: string;
  refreshSeconds: number;
}

export interface InfraPlaneCounts {
  total: number;
  available: number;
  verified: number;
}

export interface ComponentSetCounts {
  total: number;
  finished: number;
  rollingOut: number;
  failed: number;
  terminating: number;
}

export interface HealthCount {
  health: string;
  count: number;
}

export interface Summary {
  infraPlanes: InfraPlaneCounts;
  componentSets: ComponentSetCounts;
  components: HealthCount[];
}

export interface InfraPlaneRow {
  namespace: string;
  name: string;
  domain?: string;
  available: Condition | null;
  clusterConfigApplied: Condition | null;
  verified: Condition | null;
  dnsReady: Condition | null;
  created: string;
}

export type SetStatus = "Terminating" | "Finished" | "Failed" | "RollingOut";

export interface ComponentRow {
  name: string;
  chart: string;
  infraPlane: string;
  /** An Argo CD health, Succeeded, Failed, or NotReleased. */
  health: string;
  reason?: string;
  message?: string;
  sync?: string;
}

export interface ComponentSetRow {
  namespace: string;
  name: string;
  status: SetStatus;
  applied: Condition | null;
  progressing: Condition | null;
  message?: string;
  components: ComponentRow[];
  created: string;
}

export interface Overview {
  namespaces: string[];
  /** The namespace shown, or "" for all of them. */
  selected: string;
  summary: Summary;
  infraPlanes: InfraPlaneRow[];
  componentSets: ComponentSetRow[];
}

/** An API request that failed, with the server's status, reason and message. */
export class ApiError extends Error {
  readonly status: number;
  readonly reason: string;

  constructor(status: number, reason: string, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.reason = reason;
  }
}

async function getJSON<T>(url: string, signal?: AbortSignal): Promise<T> {
  let response: Response;
  try {
    response = await fetch(url, { headers: { Accept: "application/json" }, signal });
  } catch (error) {
    if (error instanceof DOMException && error.name === "AbortError") {
      throw error;
    }
    throw new ApiError(0, "ServerUnreachable", "ccluster server could not be reached. Is it still running?");
  }
  if (!response.ok) {
    let body: { reason?: string; message?: string } = {};
    try {
      body = (await response.json()) as typeof body;
    } catch {
      // Not one of the API's JSON errors, such as a proxy's page.
    }
    throw new ApiError(response.status, body.reason ?? "", body.message ?? response.statusText);
  }
  return (await response.json()) as T;
}

export function fetchConfig(signal?: AbortSignal): Promise<Config> {
  return getJSON<Config>("/api/v1/config", signal);
}

export function fetchOverview(namespace: string, signal?: AbortSignal): Promise<Overview> {
  const query = namespace ? `?namespace=${encodeURIComponent(namespace)}` : "";
  return getJSON<Overview>(`/api/v1/overview${query}`, signal);
}
