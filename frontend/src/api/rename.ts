// Rename workflow data access (Stage 3). The staged scan→propose→apply review
// queue: Scan enqueues proposals server-side, the operator reviews each row, and
// each single-item mutating action (Apply / Give back / Re-pick / Dismiss) still
// acts on EXACTLY ONE already-listed proposal via its own button. On top of that
// there is now one bounded bulk affordance — applyBatchStreaming, backing the
// opt-in "Apply Selected" multi-select of already-reviewed Pending rows — which
// the backend applies sequentially with skip-and-continue (not a queue-wide
// apply-all, and it does not change how any single row applies). Every call goes
// through api() (src/api/client.ts) so it inherits the session cookie and the
// global 401 → re-boot session-expiry fallback. Response shapes are the
// generated DTOs (@dto), never hand-duplicated (plan Guardrail #4).
//
// Claude 2026-08-05: fetchProposals returns ProposalPage (paginated).
// Reason: deep-interview-organize-pagination-log
// Troubleshooting: tests that expect Proposal[] must use .items
// Review if: infinite scroll replaces offset pages.

import { api } from "./client";
import type {
  AdultReviewConfirmRequest,
  AdultReviewPreview,
  AdultSceneSearchResponse,
  AdultSceneResolveResponse,
  DeleteBatchRequest,
  DeleteBatchResponse,
  DiscoverItem,
  MoveModeRequest,
  Proposal,
  ProposalPage,
  RecentlyAppliedEntry,
  RepickRequest,
  SeriesSearchItem,
} from "@dto";
import type { Mode, ProposalStatus } from "./discover";
import {
  adultAspectQuery,
  applyBatchStreaming,
  fetchProposalPage,
  type ProposalListView,
} from "./organize";

export type {
  AdultReviewConfirmRequest,
  AdultReviewPreview,
  Proposal,
  RecentlyAppliedEntry,
  RepickRequest,
};
export type { ProposalStatus };

export function scanRename(mode: Mode, aspect?: string): Promise<void> {
  return api<void>(
    `/api/modes/${mode}/rename/scan${adultAspectQuery(aspect)}`,
    { method: "POST" },
  );
}

export function fetchProposals(
  mode: Mode,
  limit = 50,
  offset = 0,
  view: ProposalListView = "live",
): Promise<ProposalPage> {
  return fetchProposalPage(
    `/api/modes/${mode}/rename/proposals`,
    limit,
    offset,
    view,
  );
}

export function applyProposal(id: number): Promise<unknown> {
  return api(`/api/proposals/${id}/apply`, {
    method: "POST",
    body: JSON.stringify({}),
  });
}

export function dismissProposal(id: number): Promise<unknown> {
  return api(`/api/proposals/${id}/dismiss`, { method: "POST" });
}

/** Rename's Delete action — see .omc/plans/autopilot-impl.md §1. */
export function deleteBatch(
  items: DeleteBatchRequest["items"],
): Promise<DeleteBatchResponse> {
  return api("/api/proposals/delete-batch", {
    method: "POST",
    body: JSON.stringify({ items } satisfies DeleteBatchRequest),
  });
}

export { applyBatchStreaming };

export type CatalogSearchOpts = {
  year?: number;
  id?: number;
  series?: string;
};

function catalogSearchParams(
  query: string,
  extra: Record<string, string | number | undefined>,
): string {
  const parts: string[] = [];
  if (query.trim()) {
    parts.push(`q=${encodeURIComponent(query.trim())}`);
  }
  for (const [key, value] of Object.entries(extra)) {
    if (value === undefined || value === "") {
      continue;
    }
    parts.push(`${encodeURIComponent(key)}=${encodeURIComponent(String(value))}`);
  }
  return parts.join("&");
}

export function tmdbSearch(
  mode: Mode,
  query: string,
  opts?: CatalogSearchOpts,
): Promise<DiscoverItem[]> {
  return api<DiscoverItem[]>(
    `/api/modes/${mode}/tmdb-search?${catalogSearchParams(query, {
      year: opts?.year,
      id: opts?.id,
    })}`,
  );
}

export function tvdbSearch(
  query: string,
  kind: "series" | "episode",
  opts?: CatalogSearchOpts,
): Promise<SeriesSearchItem[]> {
  return api<SeriesSearchItem[]>(
    `/api/modes/series/tvdb-search?${catalogSearchParams(query, {
      kind,
      year: opts?.year,
      id: opts?.id,
      series: opts?.series,
    })}`,
  );
}

export function repickProposal(
  id: number,
  req: RepickRequest,
): Promise<unknown> {
  return api(`/api/proposals/${id}/repick`, {
    method: "POST",
    body: JSON.stringify(req),
  });
}

// Promise<void>, NOT Promise<Proposal>: the endpoint returns 204 and client.ts
// yields `null as T` on a 204 — typing this as Promise<Proposal> is a lie
// TypeScript cannot catch, and the first `.title` deref on the result would be
// a runtime null crash. Callers refetch through the gated list endpoint instead.
export function moveProposalMode(
  id: number,
  req: MoveModeRequest,
): Promise<void> {
  return api(`/api/proposals/${id}/move-mode`, {
    method: "POST",
    body: JSON.stringify(req),
  });
}

export type AdultSearchOpts = {
  performer?: string;
  studio?: string;
  year?: number;
};

export function adultSceneSearch(
  query: string,
  opts?: AdultSearchOpts,
): Promise<AdultSceneSearchResponse> {
  return api(
    `/api/modes/adult/scene-search?${catalogSearchParams(query, {
      performer: opts?.performer,
      studio: opts?.studio,
      year: opts?.year,
    })}`,
  );
}

export function adultSceneResolve(
  url: string,
): Promise<AdultSceneResolveResponse> {
  return api(
    `/api/modes/adult/scene-resolve?url=${encodeURIComponent(url)}`,
  );
}

// ---- Rename Undo ----------------------------------------------------------

// UndoResult is hand-declared rather than imported from @dto because the
// endpoint emits internal/rename's own UndoResult, not an internal/apidto type
// — the same "no generated DTO for this endpoint, so the wire shape is inlined
// here" precedent settings.ts's fetchDiscoverRefreshInterval /
// fetchAdultNewestScanInterval already set. (RecentlyAppliedEntry below IS
// generated: its Go struct lives in internal/apidto precisely because it was
// new, so there was nothing to mirror.)
// Review if: a generated DTO is ever added for POST /api/proposals/{id}/undo.
//
// A 200 does NOT mean "fully restored" — undo is best-effort by design, so the
// caller MUST branch on fileRestored/driftDetected in the body. The failure
// cases (400/404/409/500/503) come back as a thrown ApiError instead.
export interface UndoResult {
  proposalId: number;
  mode: Mode;
  /** False whenever no file was moved back, for ANY reason. fileMessage says which. */
  fileRestored: boolean;
  fileMessage: string;
  /** Where the file actually landed — not necessarily the original source path. */
  restoredPath?: string;
  driftDetected: boolean;
  driftWarnings?: string[];
  rowsReverted: number;
  /** Adult-only: the Apply's external stash-box submission cannot be retracted. */
  giveBackNotRetractable: boolean;
}

export function undoProposal(id: number): Promise<UndoResult> {
  return api<UndoResult>(`/api/proposals/${id}/undo`, { method: "POST" });
}

// The bounded, undo-eligible-only list — NOT the general `view=history`
// proposals list. Answers 200 [] when undo is unavailable or nothing is
// undoable, so this never needs its own error branch for those two states.
export function fetchRecentlyApplied(
  mode: Mode,
): Promise<RecentlyAppliedEntry[]> {
  return api<RecentlyAppliedEntry[]>(
    `/api/modes/${mode}/rename/recently-applied`,
  );
}

// ---- Adult Review ----------------------------------------------------------
// DTOs live in @dto (AdultReviewPreview / AdultReviewConfirmRequest).

export function fetchAdultReview(
  mode: Mode,
  id: number,
): Promise<AdultReviewPreview> {
  return api<AdultReviewPreview>(
    `/api/modes/${mode}/rename/proposals/${id}/review`,
    { signal: AbortSignal.timeout(35000) },
  );
}

export function confirmAdultReview(
  mode: Mode,
  id: number,
  body: AdultReviewConfirmRequest,
): Promise<unknown> {
  return api(
    `/api/modes/${mode}/rename/proposals/${id}/review-confirm`,
    { method: "POST", body: JSON.stringify(body) },
  );
}
