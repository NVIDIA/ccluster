// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

/// <reference types="vitest/config" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The build goes straight into the Go package that embeds it, and is committed: see
// internal/server/ui/embed.go. `npm run dev` serves the UI with hot reload and passes /api
// to a ccluster server started with --no-browser on its default address.
export default defineConfig({
  plugins: [react()],
  base: "/",
  build: {
    outDir: "../internal/server/ui/dist",
    emptyOutDir: true,
    // The server's Content-Security-Policy allows only its own origin, so nothing may be
    // inlined as a data: URI, and no inline script is emitted for module preloading.
    assetsInlineLimit: 0,
    modulePreload: { polyfill: false },
    // The licenses of the bundled packages, served beside the UI.
    license: { fileName: "THIRD_PARTY_LICENSES.md" },
    sourcemap: false,
  },
  server: {
    proxy: {
      "/api": { target: "http://127.0.0.1:8484", changeOrigin: true },
    },
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test-setup.ts"],
    restoreMocks: true,
  },
});
