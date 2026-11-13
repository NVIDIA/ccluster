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
//
// A stylesheet's leading comment is its license notice: base-external.css's says the NVIDIA
// GUI Icons embedded in it are CC-BY-4.0. The minifier drops comments, so the notices are
// also written to src/generated/kui-notices.md, which vite.config.ts appends to the
// build's THIRD_PARTY_LICENSES.md.

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
    const text = await response.text();
    const css = text.replace(/@font-face\s*\{[^}]*url\(\s*["']?https?:[^}]*\}/g, "");
    return { file, css: `/* --- ${file}, Kaizen UI Foundations ${version} --- */\n${css}`, notice: leadingNotice(text) };
  }),
);

// The text of the comment the stylesheet starts with, without its comment markers, or "".
function leadingNotice(css) {
  const match = /^\s*\/\*([\s\S]*?)\*\//.exec(css);
  if (!match) {
    return "";
  }
  return match[1]
    .split("\n")
    .map((line) => line.replace(/^\s*\*+ ?/, "").trimEnd())
    .join("\n")
    .trim();
}

const out = new URL("../src/generated/", import.meta.url);
await mkdir(out, { recursive: true });
await writeFile(new URL("kui.css", out), parts.map((part) => part.css).join("\n\n"));
await writeFile(
  new URL("kui-notices.md", out),
  parts
    .filter((part) => part.notice)
    .map((part) => `### ${part.file}\n\n${part.notice}\n`)
    .join("\n"),
);
console.log(`Wrote src/generated/kui.css from Kaizen UI Foundations ${version}`);
