// Request-status data access (F4). One read-only, cross-mode aggregation
// endpoint (GET /api/requests) that rolls up, per title: In Library (from
// tracked items), Downloading (from active grabs), and Missing (Series episodes
// TMDB knows about with no file on disk). Pure derive-on-read — no new persisted
// table, no write path. Goes through api() so it inherits the session cookie and
// the global 401 → re-boot fallback, same as every other data module.

import { api } from "./client";
import type { RequestStatusResponse } from "@dto";

export type { RequestStatusResponse };

// fetchRequests returns the cross-mode request-status rollup for the Requests
// worklist screen.
export function fetchRequests(): Promise<RequestStatusResponse> {
  return api<RequestStatusResponse>(`/api/requests`);
}
