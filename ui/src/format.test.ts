// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { age, conditionDetail, conditionLook, healthLook, setStatusLook } from "./format";

describe("badge looks", () => {
  it("maps condition statuses to a tone and a text label", () => {
    expect(conditionLook({ status: "True" })).toEqual({ tone: "ok", label: "True" });
    expect(conditionLook({ status: "False" })).toEqual({ tone: "bad", label: "False" });
    expect(conditionLook({ status: "Unknown" })).toEqual({ tone: "warn", label: "Unknown" });
    expect(conditionLook(null)).toEqual({ tone: "neutral", label: "Pending" });
  });

  it("maps component set statuses", () => {
    expect(setStatusLook("Finished").tone).toBe("ok");
    expect(setStatusLook("RollingOut")).toEqual({ tone: "progress", label: "Rolling out" });
    expect(setStatusLook("Failed").tone).toBe("bad");
    expect(setStatusLook("Terminating").tone).toBe("warn");
  });

  it("maps component healths", () => {
    expect(healthLook("Healthy").tone).toBe("ok");
    expect(healthLook("Succeeded").tone).toBe("ok");
    expect(healthLook("Degraded").tone).toBe("bad");
    expect(healthLook("Missing").tone).toBe("bad");
    expect(healthLook("Progressing").tone).toBe("progress");
    expect(healthLook("NotReleased")).toEqual({ tone: "neutral", label: "Not released" });
    expect(healthLook("SomethingNew").tone).toBe("neutral");
  });
});

describe("conditionDetail", () => {
  it("joins reason and message", () => {
    expect(conditionDetail({ status: "False", reason: "SecretNotFound", message: "no secret" })).toBe(
      "SecretNotFound: no secret",
    );
    expect(conditionDetail(null)).toMatch(/Not reported/);
  });
});

describe("age", () => {
  const now = Date.parse("2026-11-09T12:00:00Z");
  it.each([
    ["2026-11-09T11:59:15Z", "45s"],
    ["2026-11-09T11:48:00Z", "12m"],
    ["2026-11-09T07:00:00Z", "5h"],
    ["2026-11-06T12:00:00Z", "3d"],
  ])("of %s is %s", (created, want) => {
    expect(age(created, now)).toBe(want);
  });
});
