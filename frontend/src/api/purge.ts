// Clean-up workflow data access. The module, the routes and every identifier
// here are still `purge` — only the operator-facing NAME is "Clean-up".
//
// Clean-up is the staged scan→propose→apply DELETE queue: Scan evaluates each
// mode's enabled pruning rules against every tracked item and enqueues one
// delete proposal per match, the operator reviews the queue, and each
// single-item action — Apply (Delete) / Dismiss — acts on EXACTLY ONE item.
//
// Claude 2026-08-11: the allowlist accessors (fetchAllowlist/addAllowlistTag/
// removeAllowlistTag) are GONE with the mechanism and its routes.
// Reason: tags are now a fourth AND'd condition on a pruning rule, so matching
// is configured entirely through src/api/pruningRules.ts.
// Review if: Clean-up ever gains a matching mechanism of its own again.
//
// One bounded bulk affordance exists on the PROPOSALS queue only: applyBatch
// backs the opt-in "Apply Selected" multi-select of already-reviewed Pending
// delete proposals, applied sequentially server-side with skip-and-continue
// (behind the same window.confirm guard the single delete has). It is NOT a
// queue-wide delete-all and does not change how any single row deletes.
//
// Unlike Rename, Clean-up has NO re-pick / give-back / draft: a proposal is
// only ever Applied (delete the file + drop the record) or Dismissed. Its
// proposal wire shape is the shared @dto Proposal, of which Clean-up reads
// only Title/Status/RootFolderPath/Reason.
//
// Every call goes through api() (src/api/client.ts) so it inherits the session
// cookie and the global 401 → re-boot session-expiry fallback.

import { api } from "./client";
import type {
  ApplyBatchItem,
  ApplyBatchResponse,
  Proposal,
  ProposalPage,
} from "@dto";
import type { Mode, ProposalStatus } from "./discover";
import { fetchProposalPage, adultAspectQuery, type ProposalListView } from "./organize";

export type { Proposal };
// ProposalStatus is the single shared narrowing (see discover.ts); re-exported
// so screens keep importing it from their workflow's api module. Clean-up only
// ever produces pending, then applied/dismissed.
export type { ProposalStatus };

// scanPurge runs Clean-up's propose-phase for one mode: the backend evaluates
// the mode's enabled pruning rules against every tracked item and replaces the
// mode's pending queue with what it finds. One POST, no body; the caller
// re-fetches.
export function scanPurge(mode: Mode, aspect?: string): Promise<void> {
  return api<void>(
    `/api/modes/${mode}/purge/scan${adultAspectQuery(aspect)}`,
    { method: "POST" },
  );
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
// id. Clean-up items carry only an id (no Dedup keepIndex/keepAll). Applies only
// to the proposals queue — the Rules card has no batch path.
export function applyBatch(
  items: ApplyBatchItem[],
): Promise<ApplyBatchResponse> {
  return api<ApplyBatchResponse>(`/api/proposals/apply-batch`, {
    method: "POST",
    body: JSON.stringify({ items }),
  });
}

