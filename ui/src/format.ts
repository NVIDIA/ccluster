// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import type { Condition, SetStatus } from "./api";

/** How a badge looks. Every tone also carries a text label and an icon. */
export type Tone = "ok" | "bad" | "warn" | "progress" | "neutral";

export interface BadgeLook {
  tone: Tone;
  label: string;
}

/** The badge of a condition the operator reports, or of one it has not reported yet. */
export function conditionLook(condition: Condition | null): BadgeLook {
  switch (condition?.status) {
    case "True":
      return { tone: "ok", label: "True" };
    case "False":
      return { tone: "bad", label: "False" };
    case "Unknown":
      return { tone: "warn", label: "Unknown" };
    default:
      return { tone: "neutral", label: "Pending" };
  }
}

const setStatusLooks: Record<SetStatus, BadgeLook> = {
  Finished: { tone: "ok", label: "Finished" },
  RollingOut: { tone: "progress", label: "Rolling out" },
  Failed: { tone: "bad", label: "Failed" },
  Terminating: { tone: "warn", label: "Terminating" },
};

export function setStatusLook(status: SetStatus): BadgeLook {
  return setStatusLooks[status] ?? { tone: "neutral", label: status };
}

const healthTones: Record<string, Tone> = {
  Healthy: "ok",
  Succeeded: "ok",
  Progressing: "progress",
  Degraded: "bad",
  Failed: "bad",
  Missing: "bad",
  Suspended: "warn",
  Unknown: "warn",
  NotReleased: "neutral",
};

export function healthLook(health: string): BadgeLook {
  return { tone: healthTones[health] ?? "neutral", label: healthLabel(health) };
}

export function healthLabel(health: string): string {
  return health === "NotReleased" ? "Not released" : health;
}

/** The text behind a condition's badge, for its tooltip. */
export function conditionDetail(condition: Condition | null): string {
  if (!condition) {
    return "Not reported yet for the current generation.";
  }
  return [condition.reason, condition.message].filter(Boolean).join(": ");
}

/** A short age, like kubectl's: 45s, 12m, 5h, 3d. */
export function age(created: string, now: number = Date.now()): string {
  const seconds = Math.max(0, Math.floor((now - Date.parse(created)) / 1000));
  if (Number.isNaN(seconds)) {
    return "";
  }
  if (seconds < 120) {
    return `${seconds}s`;
  }
  const minutes = Math.floor(seconds / 60);
  if (minutes < 120) {
    return `${minutes}m`;
  }
  const hours = Math.floor(minutes / 60);
  if (hours < 48) {
    return `${hours}h`;
  }
  return `${Math.floor(hours / 24)}d`;
}

export function clock(time: number): string {
  return new Date(time).toLocaleTimeString();
}
