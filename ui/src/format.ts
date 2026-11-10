// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import type { Condition, SetStatus } from "./api";

/** A Kaizen Badge color. Every status keeps its meaning in the label too. */
export type BadgeColor = "blue" | "green" | "red" | "yellow" | "gray";

export interface StatusLook {
  color: BadgeColor;
  label: string;
}

/** The look of a condition the operator reports, or of one it has not reported yet. */
export function conditionLook(condition: Condition | null): StatusLook {
  switch (condition?.status) {
    case "True":
      return { color: "green", label: "True" };
    case "False":
      return { color: "red", label: "False" };
    case "Unknown":
      return { color: "yellow", label: "Unknown" };
    default:
      return { color: "gray", label: "Pending" };
  }
}

const setStatusLooks: Record<SetStatus, StatusLook> = {
  Finished: { color: "green", label: "Finished" },
  RollingOut: { color: "blue", label: "Rolling out" },
  Failed: { color: "red", label: "Failed" },
  Terminating: { color: "yellow", label: "Terminating" },
};

export function setStatusLook(status: SetStatus): StatusLook {
  return setStatusLooks[status] ?? { color: "gray", label: status };
}

const healthColors: Record<string, BadgeColor> = {
  Healthy: "green",
  Succeeded: "green",
  Progressing: "blue",
  Degraded: "red",
  Failed: "red",
  Missing: "red",
  Suspended: "yellow",
  Unknown: "yellow",
  NotReleased: "gray",
};

export function healthLook(health: string): StatusLook {
  return { color: healthColors[health] ?? "gray", label: healthLabel(health) };
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
