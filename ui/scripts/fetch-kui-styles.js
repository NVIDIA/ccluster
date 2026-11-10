// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Fetches the Kaizen UI stylesheets for the installed @nvidia/foundations-react-core
// version into src/generated/kui.css, which src/styles.css imports. Kaizen publishes its
// CSS only to NVIDIA's CDN; bundling it keeps ccluster server self-contained, as its
// Content-Security-Policy allows nothing from another origin.
//
// base-external.css is Kaizen's base for open-source projects: base.css carries NVIDIA
// Sans and proprietary icons that only NVIDIA-licensed projects may ship. Its @font-face
// rules that load fonts from the CDN are dropped, so text falls back to local fonts.

import { mkdir, readFile, writeFile } from "node:fs/promises";

const { version } = JSON.parse(
  await readFile(new URL("../node_modules/@nvidia/foundations-react-core/package.json", import.meta.url), "utf8"),
);
const cdn = `https://webassets.nvidia.com/kaizen-ui-foundations/${version}`;
const files = ["base-external.css", "components.css"];

const parts = await Promise.all(
  files.map(async (file) => {
    const response = await fetch(`${cdn}/${file}`);
    if (!response.ok) {
      throw new Error(`fetching ${cdn}/${file}: ${response.status} ${response.statusText}`);
    }
    const css = (await response.text()).replace(/@font-face\s*\{[^}]*url\(\s*["']?https?:[^}]*\}/g, "");
    return `/* --- ${file}, Kaizen UI Foundations ${version} --- */\n${css}`;
  }),
);

const out = new URL("../src/generated/", import.meta.url);
await mkdir(out, { recursive: true });
await writeFile(new URL("kui.css", out), parts.join("\n\n"));
console.log(`Wrote src/generated/kui.css from Kaizen UI Foundations ${version}`);
