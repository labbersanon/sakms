// Purge workflow data access (Stage 3). Ported from the vanilla-JS frontend
// (internal/web/static/index.html's renderPurge). Purge is the staged
// scan→propose→apply DELETE queue keyed off an editable tag allowlist: Scan
// matches each mode's allowlist against every tracked item's tags and enqueues
// one delete proposal per match, the operator reviews the queue, and each
// single-item action — Apply (Delete) / Dismiss on a proposal, add/remove on
// the allowlist — still acts on EXACTLY ONE item via its own control.
//
// One bounded bulk affordance now exists on the PROPOSALS queue only: applyBatch
// backs the opt-in "Apply Selected" multi-select of already-reviewed Pending
// delete proposals, applied sequentially server-side with skip-and-continue
// (behind the same window.confirm guard the single delete has). It is NOT a
// queue-wide delete-all and does not change how any single row deletes. The
// ALLOWLIST stays deliberately bulk-free — one × per chip, one Add per input,
// no clear-all/remove-all path.
//
// Unlike Rename, Purge has NO re-pick / give-back / draft: a proposal is only
// ever Applied (delete the file + drop the record) or Dismissed. Its proposal
// wire shape is the shared @dto Proposal, of which Purge reads only
// Title/Status/RootFolderPath/Reason. The allowlist crosses the wire as a bare
// string[] (no named response DTO); the add body is the generated
// AllowlistAddRequest.
//
// Every call goes through api() (src/api/client.ts) so it inherits the session
// cookie and the global 401 → re-boot session-expiry fallback.

import { api } from "./client";
import type {
  ApplyBatchItem,
  ApplyBatchResponse,
  Proposal,
  ProposalPage,
  AllowlistAddRequest,
} from "@dto";
import type { Mode, ProposalStatus } from "./discover";
import { fetchProposalPage, type ProposalListView } from "./organize";

export type { Proposal };
// ProposalStatus is the single shared narrowing (see discover.ts); re-exported
// so screens keep importing it from their workflow's api module. Purge only
// ever produces pending, then applied/dismissed.
export type { ProposalStatus };

// scanPurge runs Purge's propose-phase for one mode: the backend matches the
// mode's allowlist against every tracked item's tags and replaces the mode's
// pending queue with what it finds. One POST, no body; the caller re-fetches.
export function scanPurge(mode: Mode): Promise<void> {
  return api<void>(`/api/modes/${mode}/purge/scan`, { method: "POST" });
}

// fetchPurgeProposals lists one Purge queue page (view=live|history).
export function fetchPurgeProposals(
  mode: Mode,
  limit = 50,
  offset = 0,
  view: ProposalListView = "live",
): Promise<ProposalPage> {
  return fetchProposalPage(
    `/api/modes/${mode}/purge/proposals`,
    limit,
    offset,
    view,
  );
}

// applyProposal commits exactly one pending proposal — for Purge this deletes
// the tracked item's file and drops its library record. The single destructive
// "do it" action; the empty body mirrors the vanilla frontend's applyProposal.
// Defined locally (not imported from rename.ts) so Purge stays fully
// self-contained on the shared /api/proposals route.
export function applyProposal(id: number): Promise<unknown> {
  return api(`/api/proposals/${id}/apply`, {
    method: "POST",
    body: JSON.stringify({}),
  });
}

// dismissProposal drops one proposal from the queue without deleting anything.
export function dismissProposal(id: number): Promise<unknown> {
  return api(`/api/proposals/${id}/dismiss`, { method: "POST" });
}

// applyBatch deletes several already-reviewed Pending purge proposals in one
// request (the "Apply Selected" affordance, gated behind a count-worded
// window.confirm at the call site). The backend applies them sequentially and
// skips-and-continues on a per-item failure, returning one result per requested
// id. Purge items carry only an id (no Dedup keepIndex/keepAll). Applies only to
// the proposals queue — the allowlist has no batch path.
export function applyBatch(
  items: ApplyBatchItem[],
): Promise<ApplyBatchResponse> {
  return api<ApplyBatchResponse>(`/api/proposals/apply-batch`, {
    method: "POST",
    body: JSON.stringify({ items }),
  });
}

// fetchAllowlist returns one mode's current Purge tag allowlist — a bare array
// of tag names (no wrapping object).
export function fetchAllowlist(mode: Mode): Promise<string[]> {
  return api<string[]>(`/api/modes/${mode}/purge/allowlist`);
}

// addAllowlistTag adds exactly one tag rule. Adding a tag already present is
// not an error server-side.
export function addAllowlistTag(mode: Mode, tag: string): Promise<unknown> {
  const body: AllowlistAddRequest = { tag };
  return api(`/api/modes/${mode}/purge/allowlist`, {
    method: "POST",
    body: JSON.stringify(body),
  });
}

// removeAllowlistTag removes exactly one tag rule (path-only, no body).
export function removeAllowlistTag(mode: Mode, tag: string): Promise<unknown> {
  return api(`/api/modes/${mode}/purge/allowlist/${encodeURIComponent(tag)}`, {
    method: "DELETE",
  });
}
