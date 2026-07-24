// Auto-grab data access (Stage 2). Discover's one-click unattended grab and
// its manual-fallback pick list, plus the Grabs view's list. Every call goes
// through api() (src/api/client.ts) so it inherits the session cookie and the
// global 401 → re-boot session-expiry fallback. Request/response shapes are the
// generated DTOs (@dto), never hand-duplicated (plan Guardrail #4).
//
// AMENDED 2026-07-24 — bounded bulk-grab exception (Discover-depth Pass 2, a
// deliberate, user-approved narrowing of the "never a bulk action" convention;
// see discover/index.tsx's amended top comment and the plan's Guardrail #3
// override). The single autoGrab/manualGrab calls below are UNCHANGED — still
// exactly one title/scene per call. The new autoGrabBatch adds one capped
// (≤20 flattened items), sequential, server-side multi-item path for Discover's
// opt-in select-mode: operator-chosen items only, three-state per-item results,
// no scheduler, no queue-wide "grab everything". It reuses the SAME per-mode
// AutoGrabRequest as the single endpoint (one AutoGrabBatchItem = {mode,request}).

import { api } from "./client";
import type {
  AutoGrabBatchItem,
  AutoGrabBatchRequest,
  AutoGrabBatchResponse,
  AutoGrabBatchResult,
  AutoGrabCandidate,
  AutoGrabRequest,
  AutoGrabResponse,
  Grab,
} from "@dto";

export type {
  AutoGrabBatchItem,
  AutoGrabBatchRequest,
  AutoGrabBatchResponse,
  AutoGrabBatchResult,
  AutoGrabCandidate,
  AutoGrabRequest,
  AutoGrabResponse,
  Grab,
};

// autoGrab triggers Discover's one-click unattended grab for exactly one
// title/scene. The backend searches, scores, and either grabs the top
// qualifier (response.grabbed) or returns the ranked manual pick list
// (response.fallback). Still exactly one grab per call — the bounded
// multi-item path is the separate autoGrabBatch below, never this function.
export function autoGrab(
  mode: string,
  req: AutoGrabRequest,
): Promise<AutoGrabResponse> {
  return api<AutoGrabResponse>(`/api/modes/${mode}/autograb`, {
    method: "POST",
    body: JSON.stringify(req),
  });
}

// autoGrabBatch runs Discover select-mode's bounded bulk grab: one POST to the
// global /api/autograb-batch (not mode-scoped in the path — each item carries
// its own mode). The backend caps the flattened item count (20), rejects an
// over-cap batch BEFORE any Prowlarr search, then runs a SEQUENTIAL server-side
// loop (max one indexer query in flight at a time — the affirmative safety
// argument that keeps this consistent with "Discover never queries Prowlarr
// automatically"). Each item comes back three-state: grabbed / fallback (with
// candidates, no grab) / error (message, skip-and-continue). Always resolves —
// per-item failures live in the results, never reject the whole call.
export function autoGrabBatch(
  items: AutoGrabBatchItem[],
): Promise<AutoGrabBatchResponse> {
  const body: AutoGrabBatchRequest = { items };
  return api<AutoGrabBatchResponse>(`/api/autograb-batch`, {
    method: "POST",
    body: JSON.stringify(body),
  });
}

// libraryRootFolder reads a mode's configured import root. Auto-grab resolves
// it server-side; the manual fallback grab needs it explicitly because it
// reuses the existing /search/grab endpoint (which takes rootFolderPath).
export function libraryRootFolder(mode: string): Promise<string> {
  return api<{ path: string }>(
    `/api/modes/${mode}/library/root-folder`,
  ).then((r) => r.path);
}

// ManualGrabBody is the identity + chosen release the fallback pick list
// sends to /api/modes/{mode}/search/grab — exactly one release per submit.
interface ManualGrabBody {
  title: string;
  tmdbId?: number;
  seasonNumber?: number;
  episodeNumber?: number;
  seasonSpecified?: boolean;
  indexer: string;
  protocol: string;
  downloadUrl: string;
  rootFolderPath: string;
}

// manualGrab sends one operator-picked fallback release to the download client
// via the existing search/grab endpoint — the same no-bulk, one-item-per-click
// invariant as auto-grab.
export function manualGrab(mode: string, body: ManualGrabBody): Promise<Grab> {
  return api<Grab>(`/api/modes/${mode}/search/grab`, {
    method: "POST",
    body: JSON.stringify(body),
  });
}

// fetchGrabs lists the grabs recorded for one mode (the Grabs view).
export function fetchGrabs(mode: string): Promise<Grab[]> {
  return api<Grab[]>(`/api/modes/${mode}/grabs`);
}
