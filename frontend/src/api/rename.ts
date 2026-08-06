// Rename workflow data access (Stage 3). The staged scan→propose→apply review
// queue: Scan enqueues proposals server-side, the operator reviews each row, and
// each single-item mutating action (Apply / Give back / Re-pick / Dismiss) still
// acts on EXACTLY ONE already-listed proposal via its own button. On top of that
// there is now one bounded bulk affordance — applyBatch, backing the opt-in
// "Apply Selected" multi-select of already-reviewed Pending rows — which the
// backend applies sequentially with skip-and-continue (not a queue-wide
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
  ApplyBatchItem,
  ApplyBatchResponse,
  DiscoverItem,
  Proposal,
  ProposalPage,
  RepickRequest,
} from "@dto";
import type { Mode, ProposalStatus } from "./discover";
import {
  applyBatchStreaming,
  fetchProposalPage,
  type ProposalListView,
} from "./organize";

export type { Proposal, RepickRequest };
export type { ProposalStatus };

export function scanRename(mode: Mode): Promise<void> {
  return api<void>(`/api/modes/${mode}/rename/scan`, { method: "POST" });
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

export function applyBatch(
  items: ApplyBatchItem[],
): Promise<ApplyBatchResponse> {
  return api<ApplyBatchResponse>(`/api/proposals/apply-batch`, {
    method: "POST",
    body: JSON.stringify({ items }),
  });
}

export { applyBatchStreaming };

export function submitDraft(id: number): Promise<unknown> {
  return api(`/api/proposals/${id}/submit-draft`, { method: "POST" });
}

export function tmdbSearch(mode: Mode, query: string): Promise<DiscoverItem[]> {
  return api<DiscoverItem[]>(
    `/api/modes/${mode}/tmdb-search?q=${encodeURIComponent(query)}`,
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
