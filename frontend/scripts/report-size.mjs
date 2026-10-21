// Build-time gzipped-JS size report.
//
// Establishes the bundle-size *measurement* only. Guardrail #9 of the plan
// sets a soft ceiling of 200 KB gzipped JS; full enforcement is Stage 4's
// job. This script never fails the build — it prints the total and flags
// (does not block) an over-ceiling bundle, matching the plan's "soft
// guardrail, flag if exceeded" wording.
//
// Claude 2026-10-21: ceiling applies to the app shell, not hls.js.
// Reason: hls.js is ~184 KB gzipped by itself and is only needed when a
//   browser without native HLS starts playback. Counting it in the shell
//   made Settings pay for a video engine. Playback chunks are printed
//   separately and do not trip the warning.
// Troubleshooting: OVER CEILING on "shell" means app code grew; a missing
//   hls-*.js line means the dynamic import / manualChunks split broke.
// Review if: Guardrail #9 is redefined to include optional playback engines.

import { readdir, readFile } from "node:fs/promises";
import { gzipSync } from "node:zlib";
import { fileURLToPath } from "node:url";
import { join, relative } from "node:path";

const CEILING_BYTES = 200 * 1024; // 200 KB gzipped JS, soft ceiling (app shell)

// outDir from vite.config.ts, resolved relative to this script.
const outDir = fileURLToPath(new URL("../../internal/web/static", import.meta.url));

async function jsFiles(dir) {
  const out = [];
  let entries;
  try {
    entries = await readdir(dir, { withFileTypes: true });
  } catch {
    return out; // build dir missing — nothing to measure
  }
  for (const entry of entries) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) {
      out.push(...(await jsFiles(full)));
    } else if (entry.name.endsWith(".js")) {
      out.push(full);
    }
  }
  return out;
}

function kb(bytes) {
  return (bytes / 1024).toFixed(1);
}

function isPlaybackEngine(file) {
  const rel = relative(outDir, file).replaceAll("\\", "/");
  return /(^|\/)hls[-.]/i.test(rel);
}

const files = await jsFiles(outDir);
if (files.length === 0) {
  console.warn("[bundle-size] no built .js files found under", relative(process.cwd(), outDir));
  process.exit(0);
}

let shellGzip = 0;
let playbackGzip = 0;
console.log("[bundle-size] gzipped JS:");
for (const file of files.sort()) {
  const gz = gzipSync(await readFile(file)).length;
  const rel = relative(outDir, file);
  if (isPlaybackEngine(file)) {
    playbackGzip += gz;
    console.log(`  ${kb(gz).padStart(8)} KB  ${rel}  (playback)`);
  } else {
    shellGzip += gz;
    console.log(`  ${kb(gz).padStart(8)} KB  ${rel}`);
  }
}

const status = shellGzip > CEILING_BYTES ? "OVER CEILING" : "ok";
console.log(
  `[bundle-size] app shell gzipped JS: ${kb(shellGzip)} KB / ${kb(CEILING_BYTES)} KB ceiling (${status})`,
);
if (playbackGzip > 0) {
  console.log(`[bundle-size] playback engine gzipped JS: ${kb(playbackGzip)} KB (not in ceiling)`);
}
if (shellGzip > CEILING_BYTES) {
  console.warn(
    "[bundle-size] WARNING: gzipped JS exceeds the 200 KB soft ceiling (Guardrail #9). Not blocking the build.",
  );
}
