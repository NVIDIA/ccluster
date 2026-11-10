// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { age, conditionDetail, conditionLook, healthLook, setStatusLook } from "./format";

describe("badge looks", () => {
  it("maps condition statuses to a Kaizen badge color and a text label", () => {
    expect(conditionLook({ status: "True" })).toEqual({ color: "green", label: "True" });
    expect(conditionLook({ status: "False" })).toEqual({ color: "red", label: "False" });
    expect(conditionLook({ status: "Unknown" })).toEqual({ color: "yellow", label: "Unknown" });
    expect(conditionLook(null)).toEqual({ color: "gray", label: "Pending" });
  });

  it("maps component set statuses", () => {
    expect(setStatusLook("Finished").color).toBe("green");
    expect(setStatusLook("RollingOut")).toEqual({ color: "blue", label: "Rolling out" });
    expect(setStatusLook("Failed").color).toBe("red");
    expect(setStatusLook("Terminating").color).toBe("yellow");
  });

  it("maps component healths", () => {
    expect(healthLook("Healthy").color).toBe("green");
    expect(healthLook("Succeeded").color).toBe("green");
    expect(healthLook("Degraded").color).toBe("red");
    expect(healthLook("Missing").color).toBe("red");
    expect(healthLook("Progressing").color).toBe("blue");
    expect(healthLook("NotReleased")).toEqual({ color: "gray", label: "Not released" });
    expect(healthLook("SomethingNew").color).toBe("gray");
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
