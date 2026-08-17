// Claude 2026-08-10: new file — data access for the Rename/Purge/Dedup scan
// scheduler's Settings surface (plan
// .omc/plans/autopilot-impl-scanschedule-settings-ui.md §4).
// Reason: one shared file for all three workflows because the UI is now
// consolidated into a single Settings -> Organize tab; splitting six enabled
// functions and four interval functions across rename.ts, a dedup file and
// pruningRules.ts would scatter one panel's data access across three modules.
// The spec's Technical Context explicitly permits this shared-file option.
// None of these endpoints has a generated DTO (the Go handlers use local
// request/response structs, internal/api/scanschedule_settings.go), so the
// wire shapes are inlined here — same pattern as fetchAdultNewestScanInterval
// in src/api/settings.ts.
// Related files: src/api/pruningRules.ts still owns fetchPurgeScanInterval/
// putPurgeScanInterval — deliberately NOT moved, so PruningRules.tsx's own
// module graph is untouched by the relocation; the Organize panel imports them
// from there.
// Review if: these endpoints ever gain generated DTOs, at which point the
// inlined wire shapes below should be replaced by @dto imports.

import { api } from "./client";

// Enabled and interval are SEPARATE endpoints per workflow, never one combined
// body — matching this codebase's own convention (dedup-vmaf-scan-enabled is a
// fully separate endpoint pair from dedup-scan-interval, and Global.tsx's
// adult-mode toggle fires two sequential PUTs rather than one). That separation
// is also what makes "turning a workflow off never rewrites its interval" true
// on the wire, not just by backend convention.
function fetchScanEnabled(path: string): Promise<boolean> {
  return api<{ enabled: boolean }>(`/api/settings/${path}`).then((r) => r.enabled);
}

function putScanEnabled(path: string, enabled: boolean): Promise<void> {
  return api<void>(`/api/settings/${path}`, {
    method: "PUT",
    body: JSON.stringify({ enabled }),
  });
}

function fetchScanInterval(path: string): Promise<number> {
  return api<{ intervalSeconds: number }>(`/api/settings/${path}`).then(
    (r) => r.intervalSeconds,
  );
}

function putScanInterval(path: string, intervalSeconds: number): Promise<void> {
  return api<void>(`/api/settings/${path}`, {
    method: "PUT",
    body: JSON.stringify({ intervalSeconds }),
  });
}

export function fetchRenameScanEnabled(): Promise<boolean> {
  return fetchScanEnabled("rename-scan-enabled");
}
export function putRenameScanEnabled(enabled: boolean): Promise<void> {
  return putScanEnabled("rename-scan-enabled", enabled);
}
export function fetchDedupScanEnabled(): Promise<boolean> {
  return fetchScanEnabled("dedup-scan-enabled");
}
export function putDedupScanEnabled(enabled: boolean): Promise<void> {
  return putScanEnabled("dedup-scan-enabled", enabled);
}
export function fetchPurgeScanEnabled(): Promise<boolean> {
  return fetchScanEnabled("purge-scan-enabled");
}
export function putPurgeScanEnabled(enabled: boolean): Promise<void> {
  return putScanEnabled("purge-scan-enabled", enabled);
}

// Rename/Dedup had no frontend interval controls before this tab existed —
// only Purge did (src/api/pruningRules.ts), which is why those two need new
// functions and Purge's are reused as-is.
export function fetchRenameScanInterval(): Promise<number> {
  return fetchScanInterval("rename-scan-interval");
}
export function putRenameScanInterval(intervalSeconds: number): Promise<void> {
  return putScanInterval("rename-scan-interval", intervalSeconds);
}
export function fetchDedupScanInterval(): Promise<number> {
  return fetchScanInterval("dedup-scan-interval");
}
export function putDedupScanInterval(intervalSeconds: number): Promise<void> {
  return putScanInterval("dedup-scan-interval", intervalSeconds);
}

export function fetchDedupVMAFScanEnabled(): Promise<boolean> {
  return fetchScanEnabled("dedup-vmaf-scan-enabled");
}
export function putDedupVMAFScanEnabled(enabled: boolean): Promise<void> {
  return putScanEnabled("dedup-vmaf-scan-enabled", enabled);
}
