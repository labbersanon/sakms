// Usenet auto-grab toggle data access — the one global setting behind the
// Settings -> Download -> Usenet page's "Enable auto-grab" switch.
//
// Kept out of api/settings.ts deliberately: that module is the singleton
// `connections` + per-mode settings surface, and this key belongs to the Usenet
// page alongside api/serviceConnections.ts's subscription CRUD.
//
// Response shapes are plain local structs on the Go side (internal/api, next to
// adultModeEnabledResponse / recheckIntervalResponse), NOT apidto types, so
// there is nothing generated to import for these routes — the two inline shapes
// below match how /api/settings/adult-mode-enabled and
// /api/settings/recheck-interval are already consumed in api/settings.ts.
//
// THE INTERVAL COUPLING IS SERVER-SIDE, NOT HERE. Turning the toggle on also
// writes usenet_retry_interval_seconds = 86400 (off writes 0) inside the
// autograb-enabled PUT itself, so an operator can never end up with auto-grab
// on and the retry scheduler off. Do NOT "helpfully" send a second PUT to the
// interval route when toggling — one PUT is the whole operation.
//
// A GET/PUT /api/settings/usenet-retry-interval pair also exists, for
// parity/diagnostics only. It is deliberately NOT wrapped here: there is no
// user-facing interval control (the toggle owns the cadence), and the 24-hour
// figure the Settings -> Download -> Usenet page states is fixed copy, not a
// fetched value. Adding a
// client for it would be an unused export, and a writable one would break the
// single-source-of-truth coupling above.

import { api } from "./client";

// fetchUsenetAutoGrabEnabled reads the global unattended-auto-grab toggle —
// GET /api/settings/usenet-autograb-enabled. Default false.
export function fetchUsenetAutoGrabEnabled(): Promise<boolean> {
  return api<{ enabled: boolean }>(
    "/api/settings/usenet-autograb-enabled",
  ).then((r) => r.enabled);
}

// putUsenetAutoGrabEnabled flips the toggle — PUT
// /api/settings/usenet-autograb-enabled, 204 No Content. This single request
// also sets the retry interval server-side (see the module note above).
export function putUsenetAutoGrabEnabled(enabled: boolean): Promise<void> {
  return api<void>("/api/settings/usenet-autograb-enabled", {
    method: "PUT",
    body: JSON.stringify({ enabled }),
  });
}


// fetchUsenetMaxConcurrentDownloads reads how many NZBs may fetch segments at
// once — GET /api/settings/usenet-max-concurrent-downloads. Default 1. Does not
// count PAR2/repair.
export function fetchUsenetMaxConcurrentDownloads(): Promise<number> {
  return api<{ maxConcurrentDownloads: number }>(
    "/api/settings/usenet-max-concurrent-downloads",
  ).then((r) => r.maxConcurrentDownloads);
}

// putUsenetMaxConcurrentDownloads sets the NZB job concurrency cap — PUT
// /api/settings/usenet-max-concurrent-downloads, 204 No Content. Applies live.
export function putUsenetMaxConcurrentDownloads(
  maxConcurrentDownloads: number,
): Promise<void> {
  return api<void>("/api/settings/usenet-max-concurrent-downloads", {
    method: "PUT",
    body: JSON.stringify({ maxConcurrentDownloads }),
  });
}
