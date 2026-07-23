// Dedup workflow data access (Stage 3). Ported from the vanilla-JS frontend
// (internal/web/static/index.html's renderDedup). Dedup is the staged
// scan→propose→apply DEDUPLICATION queue: Scan finds content identified twice
// (an already-tracked copy plus one or more orphan files that resolve to the
// SAME identity) and enqueues one proposal PER DUPLICATE GROUP, each carrying
// the group's candidate files with the quality winner pre-flagged. The operator
// reviews each group and resolves it with that group's OWN Apply/Keep All — one
// group per click. On top of that there is now one bounded bulk affordance —
// applyBatch, backing the opt-in "Apply Selected" multi-select of already-
// reviewed Pending groups — applied sequentially server-side with skip-and-
// continue. It is NOT a queue-wide resolve-all and does not change how any
// single group resolves. Each batched group keeps the auto-winner unless the
// operator changed that group's Keep radio first, in which case that chosen
// index rides along as the item's keepIndex.
//
// Structurally DIFFERENT from Rename and Purge (verified against the old
// frontend, do NOT "align" them): a Rename/Purge proposal is a single flat row
// acted on with an empty body; a Dedup proposal is a GROUP of candidate files
// (proposal.candidates), and Apply carries a body identifying which candidate to
// keep. What a "duplicate" is differs by MODE at Scan time only — Movies group
// by TMDB id, Series by (show, season, episode), Adult by (box, scene_id) — but
// the wire shape and this client are mode-agnostic: every mode returns the same
// group-of-candidates Proposal and resolves through the same /api/proposals
// route (proposal.workflow, set at Scan time, dispatches to the right backend
// Apply — dedup.ApplyLibrary / ApplyLibrarySeries / ApplyLibraryAdult).
//
// Every call goes through api() (src/api/client.ts) so it inherits the session
// cookie and the global 401 → re-boot session-expiry fallback. Response/request
// shapes are the generated DTOs (@dto), never hand-duplicated (plan Guardrail #4).

import { api } from "./client";
import type {
  ApplyBatchItem,
  ApplyBatchResponse,
  Candidate,
  DedupApplyRequest,
  Proposal,
} from "@dto";
import type { Mode, ProposalStatus } from "./discover";

export type { Candidate, Proposal };
// ProposalStatus is the single shared narrowing (see discover.ts); re-exported
// so screens keep importing it from their workflow's api module. Dedup only
// ever produces pending, then applied/dismissed.
export type { ProposalStatus };

// scanDedup KICKS OFF Dedup's propose-phase for one mode: the backend validates
// synchronously, then runs the scan (root walk → group duplicates → replace the
// mode's pending queue) in a background goroutine. One POST, no body.
//
// The POST now resolves with 202 Accepted — meaning the scan was ACCEPTED and
// started, NOT that it finished. The proposal list is fetched separately (this
// client already discards the POST body and refetches via fetchDedupProposals),
// so no call-signature change is forced. Live progress and the terminal
// done/error signal ride the SSE stream at GET /api/modes/{mode}/dedup/scan/stream
// (see useDedupScanStream); the refetch that repopulates the queue happens in
// that stream's `done` handler, never right after this POST. A 4xx (400 bad
// root, 409 already scanning, 401 auth) still rejects synchronously here.
export function scanDedup(mode: Mode): Promise<void> {
  return api<void>(`/api/modes/${mode}/dedup/scan`, { method: "POST" });
}

// DedupScanStatus is the shape of GET /api/modes/{mode}/dedup/scan/status. There
// is no generated DTO for it (the backend type lives in internal/dedupscan, not
// internal/apidto), so it is declared locally here.
export interface DedupScanStatus {
  inflight: boolean;
}

// fetchDedupScanStatus reports whether a scan for one mode is currently running.
// It backs useDedupScanStream's liveness backstop: if the SSE stream goes quiet
// while the UI still thinks it is scanning, this reconciles "still running" vs.
// "finished (terminal frame missed/dropped)" — a proposals-count poll cannot,
// since a legitimately empty result and a still-running scan both read as zero.
export function fetchDedupScanStatus(mode: Mode): Promise<DedupScanStatus> {
  return api<DedupScanStatus>(`/api/modes/${mode}/dedup/scan/status`);
}

// fetchDedupProposals lists the Dedup review queue for one mode (every status;
// only pending groups expose actions). Each proposal carries a `candidates`
// group with one `winner` flagged.
export function fetchDedupProposals(mode: Mode): Promise<Proposal[]> {
  return api<Proposal[]>(`/api/modes/${mode}/dedup/proposals`);
}

// applyKeep resolves one duplicate group by KEEPING candidate `keepIndex` (the
// primary/tracked keeper — an array index into that proposal's `candidates`, in
// received order) and deleting every other file in the group. keepIndex is
// threaded through as a real number even when it is 0 — the group's winner may
// sit at index 0, or the operator may pick candidate 0, and dropping a literal 0
// would make the backend silently fall back to its auto-winner and delete the
// wrong file (dedup.ApplyLibrary indexes p.Candidates[keepIndex] directly).
//
// additionalKeepIndices is the multi-keep set: extra candidates the operator
// checked as "also keep" — left on disk untouched (only the primary is tracked).
// It MUST be OMITTED (not sent as []) when empty, or the existing strict
// request-shape tests break and the single-keep wire contract changes (see
// DedupApplyRequest's doc comment / AC9). Resolves exactly one proposal id.
export function applyKeep(
  id: number,
  keepIndex: number,
  additionalKeepIndices?: number[],
): Promise<unknown> {
  const body: DedupApplyRequest = { keepIndex };
  if (additionalKeepIndices && additionalKeepIndices.length > 0) {
    body.additionalKeepIndices = additionalKeepIndices;
  }
  return api(`/api/proposals/${id}/apply`, {
    method: "POST",
    body: JSON.stringify(body),
  });
}

// dedupVideoUrl builds the src for a card tile's click-to-play <video> element:
// the provenance-only streaming endpoint that resolves proposalId+candidateIndex
// to a file SAK itself recorded during its own scan (never a client-supplied
// path — see internal/api/dedup_video.go). It is a plain same-origin URL the
// browser loads with the session cookie; it does NOT go through api() (that is
// for JSON, and a <video> streams bytes with Range requests).
export function dedupVideoUrl(
  mode: Mode,
  id: number,
  candidateIndex: number,
): string {
  return `/api/modes/${mode}/dedup/proposals/${id}/video?candidateIndex=${candidateIndex}`;
}

// DedupVmafScore is the shape of GET /api/modes/{mode}/dedup/proposals/{id}/vmaf.
// There is no generated DTO for it (the backend type vmafScoreResponse is
// unexported in internal/api, not internal/apidto), so it is declared locally
// here — the same local-declaration pattern as DedupScanStatus above. The
// endpoint is poll-shaped: a cache miss kicks off a background computation and
// returns "computing" (HTTP 202); the client re-polls until "ready" (score
// populated) or "error".
export interface DedupVmafScore {
  status: "ready" | "computing" | "error";
  score?: number;
  cached?: boolean;
  candidateIndex: number;
  referenceIndex: number;
  error?: string;
}

// fetchDedupVmaf scores one candidate tile (candidateIndex) against the group's
// current primary (referenceIndex). api() returns the parsed body for the 200
// ("ready"/"error") AND 202 ("computing") responses alike — only a 4xx (bad
// params, unknown proposal) throws, which the caller surfaces as an error state.
export function fetchDedupVmaf(
  mode: Mode,
  id: number,
  candidateIndex: number,
  referenceIndex: number,
): Promise<DedupVmafScore> {
  return api<DedupVmafScore>(
    `/api/modes/${mode}/dedup/proposals/${id}/vmaf?candidateIndex=${candidateIndex}&referenceIndex=${referenceIndex}`,
  );
}

// applyKeepAll resolves one duplicate group by keeping EVERY candidate and
// deleting nothing — the conservative "these aren't really duplicates" escape
// hatch ("Keep All"). keepIndex is omitted entirely so the backend reads it as
// nil, not 0.
export function applyKeepAll(id: number): Promise<unknown> {
  const body: DedupApplyRequest = { keepAll: true };
  return api(`/api/proposals/${id}/apply`, {
    method: "POST",
    body: JSON.stringify(body),
  });
}

// dismissProposal drops one duplicate group from the queue without deleting
// anything (leaves both copies on disk, unresolved).
export function dismissProposal(id: number): Promise<unknown> {
  return api(`/api/proposals/${id}/dismiss`, { method: "POST" });
}

// applyBatch resolves several already-reviewed Pending duplicate groups in one
// request (the "Apply Selected" affordance). The backend resolves them
// sequentially and skips-and-continues on a per-item failure, returning one
// result per requested id. Per group the caller sends keepIndex ONLY when the
// operator overrode that group's Keep radio before selecting it — an item with
// keepIndex omitted lets the backend fall back to its own auto-winner (the same
// nil-vs-0 semantics as applyKeep/applyKeepAll: a real chosen index, including
// 0, must be sent, never dropped). No keepAll is sent from the batch path.
export function applyBatch(
  items: ApplyBatchItem[],
): Promise<ApplyBatchResponse> {
  return api<ApplyBatchResponse>(`/api/proposals/apply-batch`, {
    method: "POST",
    body: JSON.stringify({ items }),
  });
}
