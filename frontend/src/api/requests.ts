// Request-status data access (F4). One read-only, cross-mode aggregation
// endpoint (GET /api/requests) that rolls up, per title: In Library (from
// tracked items), Pending (queued grabs), Pending Retry / Scheduled, and
// Missing (Series episodes TMDB knows about with no file on disk). Pure
// derive-on-read — no new persisted table. Goes through api() so it inherits
// the session cookie and the global 401 → re-boot fallback.

import { api } from "./client";
import type {
  ExcludeTitleRequest,
  ExcludeTitlesBatchResponse,
  RequestStatusResponse,
} from "@dto";

export type { ExcludeTitleRequest, RequestStatusResponse };

// fetchRequests returns the cross-mode request-status rollup for the Requests
// worklist screen.
export function fetchRequests(): Promise<RequestStatusResponse> {
  return api<RequestStatusResponse>(`/api/requests`);
}

// excludeTitle permanently removes one title from the Requests worklist so it is
// never auto-grabbed/matched again (204 on success). GET /api/requests then
// suppresses the excluded title server-side.
export function excludeTitle(body: ExcludeTitleRequest): Promise<void> {
  return api<void>(`/api/requests/exclude`, {
    method: "POST",
    body: JSON.stringify(body),
  });
}

// excludeTitlesBatch removes several titles in one call (the bulk multi-select
// "Remove Selected" form). Skip-and-continue: one item's failure never blocks
// the rest, so the response carries a per-item ok/error result.
export function excludeTitlesBatch(
  items: ExcludeTitleRequest[],
): Promise<ExcludeTitlesBatchResponse> {
  return api<ExcludeTitlesBatchResponse>(`/api/requests/exclude-batch`, {
    method: "POST",
    body: JSON.stringify({ items }),
  });
}
