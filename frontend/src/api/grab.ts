// Auto-grab data access (Stage 2). Discover's one-click unattended grab and
// its manual-fallback pick list, plus the Grabs view's list. Every call goes
// through api() (src/api/client.ts) so it inherits the session cookie and the
// global 401 → re-boot session-expiry fallback. Request/response shapes are the
// generated DTOs (@dto), never hand-duplicated (plan Guardrail #4).

import { api } from "./client";
import type {
  AutoGrabCandidate,
  AutoGrabRequest,
  AutoGrabResponse,
  Grab,
} from "@dto";

export type { AutoGrabCandidate, AutoGrabRequest, AutoGrabResponse, Grab };

// autoGrab triggers Discover's one-click unattended grab for exactly one
// title/scene. The backend searches, scores, and either grabs the top
// qualifier (response.grabbed) or returns the ranked manual pick list
// (response.fallback). Never a bulk action — one call, at most one grab.
export function autoGrab(
  mode: string,
  req: AutoGrabRequest,
): Promise<AutoGrabResponse> {
  return api<AutoGrabResponse>(`/api/modes/${mode}/autograb`, {
    method: "POST",
    body: JSON.stringify(req),
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
