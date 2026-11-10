// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import type { ReactNode } from "react";
import { Anchor, AppBar, Button, Text, ThemeProvider } from "@nvidia/foundations-react-core";
import { ChevronRight, Moon, Sun } from "lucide-react";
import type { Config } from "../api";
import { useThemePreference } from "../useThemePreference";

interface AppShellProps {
  config?: Config;
  /** Page-wide controls shown in the app bar, after the control plane's context. */
  controls?: ReactNode;
  children: ReactNode;
}

/**
 * The page frame, after the NVIDIA platform UI: a Kaizen AppBar with the product and
 * page on the left, and the control plane's context, the page's controls and a theme
 * toggle on the right; then the content.
 */
export function AppShell({ config, controls, children }: AppShellProps) {
  const { theme, isDark, toggle } = useThemePreference();

  return (
    <ThemeProvider key={theme} theme={theme} global target="html">
      <div className="shell">
        <AppBar
          className="shell-bar"
          slotStart={
            <>
              <Text kind="label/bold/md">CCluster</Text>
              <ChevronRight className="icon-sm muted" aria-hidden="true" />
              <Text kind="label/regular/md" className="muted">
                Overview
              </Text>
            </>
          }
          slotEnd={
            <>
              {config && (
                <Text kind="mono/sm" className="muted context" title="Kubeconfig context of the control plane">
                  {config.context}
                </Text>
              )}
              {controls}
              <Button kind="tertiary" size="small" onClick={toggle} aria-label={isDark ? "Switch to light mode" : "Switch to dark mode"}>
                {isDark ? <Sun className="icon" /> : <Moon className="icon" />}
              </Button>
            </>
          }
        />
        <main className="shell-main">
          <div className="shell-content">
            {children}
            <footer className="shell-footer">
              <Text kind="body/regular/sm" className="muted">
                ccluster {config?.version} · read-only · refreshes every {config?.refreshSeconds ?? 5}s ·{" "}
                <Anchor href="/THIRD_PARTY_LICENSES.md">licenses</Anchor>
              </Text>
            </footer>
          </div>
        </main>
      </div>
    </ThemeProvider>
  );
}
