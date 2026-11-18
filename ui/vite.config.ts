// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

/// <reference types="vitest/config" />
import { appendFile, readFile } from "node:fs/promises";
import { join } from "node:path";
import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";

const licensesFile = "THIRD_PARTY_LICENSES.md";

// Vite's license file lists the JavaScript packages in the bundle. What arrives through CSS
// is appended here: Roboto's license, which must travel with the font files, and the
// notices of the Kaizen UI stylesheets, such as the CC-BY-4.0 attribution of the NVIDIA
// GUI Icons that base-external.css embeds. scripts/fetch-kui-styles.js keeps those
// notices, because the minifier drops the comments that carry them.
function cssLicenses(): Plugin {
  let outDir = "";
  return {
    name: "ccluster-css-licenses",
    apply: "build",
    configResolved(config) {
      outDir = config.build.outDir;
    },
    async writeBundle() {
      const pkg = JSON.parse(await readFile("node_modules/@fontsource/roboto/package.json", "utf8")) as { version: string };
      const license = await readFile("node_modules/@fontsource/roboto/LICENSE", "utf8");
      await appendFile(join(outDir, licensesFile), `\n## @fontsource/roboto - ${pkg.version} (OFL-1.1)\n\n${license.trim()}\n`);

      const kui = JSON.parse(
        await readFile("node_modules/@nvidia/foundations-react-core/package.json", "utf8"),
      ) as { version: string };
      const notices = (await readFile("src/generated/kui-notices.md", "utf8")).trim();
      if (!notices.includes("CC-BY-4.0")) {
        throw new Error("src/generated/kui-notices.md lacks the NVIDIA GUI Icons' CC-BY-4.0 notice");
      }
      await appendFile(
        join(outDir, licensesFile),
        `\n## Kaizen UI Foundations stylesheets - ${kui.version}\n\n${notices}\n`,
      );
    },
  };
}

// The build goes straight into the Go package that embeds it, and is committed: see
// internal/server/ui/embed.go. `npm run dev` serves the UI with hot reload and passes /api
// to a ccluster server started with --no-browser on its default address.
export default defineConfig({
  plugins: [react(), cssLicenses()],
  base: "/",
  build: {
    outDir: "../internal/server/ui/dist",
    emptyOutDir: true,
    // The server's Content-Security-Policy allows only its own origin, so nothing may be
    // inlined as a data: URI, and no inline script is emitted for module preloading.
    assetsInlineLimit: 0,
    modulePreload: { polyfill: false },
    // The licenses of the bundled packages, served beside the UI.
    license: { fileName: licensesFile },
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
