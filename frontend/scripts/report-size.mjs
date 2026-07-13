// Build-time gzipped-JS size report.
//
// Establishes the bundle-size *measurement* only. Guardrail #9 of the plan
// sets a soft ceiling of 200 KB gzipped JS; full enforcement is Stage 4's
// job. This script never fails the build — it prints the total and flags
// (does not block) an over-ceiling bundle, matching the plan's "soft
// guardrail, flag if exceeded" wording.

import { readdir, readFile } from "node:fs/promises";
import { gzipSync } from "node:zlib";
import { fileURLToPath } from "node:url";
import { join, relative } from "node:path";

const CEILING_BYTES = 200 * 1024; // 200 KB gzipped JS, soft ceiling

// outDir from vite.config.ts, resolved relative to this script.
const outDir = fileURLToPath(new URL("../../internal/web/static/app", import.meta.url));

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

const files = await jsFiles(outDir);
if (files.length === 0) {
  console.warn("[bundle-size] no built .js files found under", relative(process.cwd(), outDir));
  process.exit(0);
}

let totalGzip = 0;
console.log("[bundle-size] gzipped JS:");
for (const file of files.sort()) {
  const gz = gzipSync(await readFile(file)).length;
  totalGzip += gz;
  console.log(`  ${kb(gz).padStart(8)} KB  ${relative(outDir, file)}`);
}

const status = totalGzip > CEILING_BYTES ? "OVER CEILING" : "ok";
console.log(
  `[bundle-size] total gzipped JS: ${kb(totalGzip)} KB / ${kb(CEILING_BYTES)} KB ceiling (${status})`,
);
if (totalGzip > CEILING_BYTES) {
  console.warn(
    "[bundle-size] WARNING: gzipped JS exceeds the 200 KB soft ceiling (Guardrail #9). Not blocking the build.",
  );
}
