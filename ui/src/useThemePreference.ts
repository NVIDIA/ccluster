// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import { useCallback, useEffect, useState } from "react";

export type ThemePreference = "system" | "light" | "dark";

const storageKey = "ccluster-theme";
const darkQuery = "(prefers-color-scheme: dark)";

function stored(): ThemePreference {
  try {
    const value = window.localStorage.getItem(storageKey);
    return value === "light" || value === "dark" ? value : "system";
  } catch {
    return "system";
  }
}

function systemIsDark(): boolean {
  return typeof window.matchMedia === "function" && window.matchMedia(darkQuery).matches;
}

/**
 * The page's theme: the operating system's light or dark setting until the user picks
 * one with the toggle, which this browser then remembers.
 */
export function useThemePreference(): { theme: ThemePreference; isDark: boolean; toggle: () => void } {
  const [theme, setTheme] = useState<ThemePreference>(stored);
  const [systemDark, setSystemDark] = useState(systemIsDark);

  useEffect(() => {
    if (typeof window.matchMedia !== "function") {
      return;
    }
    const query = window.matchMedia(darkQuery);
    const onChange = (event: MediaQueryListEvent) => setSystemDark(event.matches);
    query.addEventListener("change", onChange);
    return () => query.removeEventListener("change", onChange);
  }, []);

  const isDark = theme === "system" ? systemDark : theme === "dark";

  // Kaizen's stylesheet themes the page from a class on <html>: nv-dark or nv-light, or
  // neither to follow prefers-color-scheme. Its ThemeProvider does not update the class
  // when the theme changes, so it is kept here, as the NVIDIA platform UI does.
  useEffect(() => {
    const root = document.documentElement.classList;
    root.toggle("nv-dark", theme === "dark");
    root.toggle("nv-light", theme === "light");
  }, [theme]);

  const toggle = useCallback(() => {
    const next: ThemePreference = isDark ? "light" : "dark";
    try {
      window.localStorage.setItem(storageKey, next);
    } catch {
      // Private windows may refuse storage; the choice then lasts for this page only.
    }
    setTheme(next);
  }, [isDark]);

  return { theme, isDark, toggle };
}
