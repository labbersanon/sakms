// Unified downloader data access. The live queue (active + waiting + recent
// stopped) plus per-item pause/resume/cancel, backed by the aria2c engine SAK
// manages. Every call goes through api() (src/api/client.ts) so it inherits the
// session cookie and the global 401 → re-boot session-expiry fallback.
// Request/response shapes are the generated DTOs (@dto), never hand-duplicated.

import { api } from "./client";
import type { BulkCancelResponse, Download, DownloadPauseState } from "@dto";

export type { BulkCancelResponse, Download, DownloadPauseState };

// fetchDownloads lists the current merged queue (active + waiting + recent
// stopped). The Downloads screen uses the SSE stream for live updates; this is
// the one-shot fallback / initial paint helper.
export function fetchDownloads(): Promise<Download[]> {
  return api<Download[]>("/api/downloads");
}

// cancelDownload removes a download and clears its stopped-list result — a true
// "remove it entirely" for the queue UI.
export function cancelDownload(gid: string): Promise<void> {
  return api<void>(`/api/downloads/${encodeURIComponent(gid)}`, {
    method: "DELETE",
  });
}

// bulkCancelDownloads cancels several downloads in one call — each cancel also
// deletes the download's files server-side, exactly like the per-item DELETE.
// Skip-and-continue: one gid's failure never blocks the rest, so the response
// carries a per-gid ok/error result.
export function bulkCancelDownloads(gids: string[]): Promise<BulkCancelResponse> {
  return api<BulkCancelResponse>("/api/downloads/cancel-batch", {
    method: "POST",
    body: JSON.stringify({ gids }),
  });
}

// fetchPauseState reads the global download-pause toggle — a single system-wide
// flag distinct from each row's per-item pause/resume.
export function fetchPauseState(): Promise<DownloadPauseState> {
  return api<DownloadPauseState>("/api/downloads/pause-state");
}

// setPauseState writes the global download-pause toggle. When paused, every
// active download is paused AND new grabs are blocked at the shared dispatch
// choke point until it is set back to false. Returns the persisted state.
export function setPauseState(paused: boolean): Promise<DownloadPauseState> {
  return api<DownloadPauseState>("/api/downloads/pause-state", {
    method: "PUT",
    body: JSON.stringify({ paused }),
  });
}

// pauseDownload pauses an active download.
export function pauseDownload(gid: string): Promise<void> {
  return api<void>(`/api/downloads/${encodeURIComponent(gid)}/pause`, {
    method: "POST",
  });
}

// resumeDownload unpauses a paused download.
export function resumeDownload(gid: string): Promise<void> {
  return api<void>(`/api/downloads/${encodeURIComponent(gid)}/resume`, {
    method: "POST",
  });
}
