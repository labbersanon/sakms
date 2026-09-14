// Request-status data access (F4). Cross-mode worklist plus promote / missing-
// episode helpers used by the Requests Grab / Promote / series-detail flows.

import { api } from "./client";
import type {
  ExcludeTitleRequest,
  ExcludeTitlesBatchResponse,
  MissingEpisodesResponse,
  PromoteRequestRequest,
  RequestStatusResponse,
} from "@dto";

export type {
  ExcludeTitleRequest,
  MissingEpisodesResponse,
  RequestStatusResponse,
};

export function fetchRequests(): Promise<RequestStatusResponse> {
  return api<RequestStatusResponse>(`/api/requests`);
}

export function excludeTitle(body: ExcludeTitleRequest): Promise<void> {
  return api<void>(`/api/requests/exclude`, {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export function excludeTitlesBatch(
  items: ExcludeTitleRequest[],
): Promise<ExcludeTitlesBatchResponse> {
  return api<ExcludeTitlesBatchResponse>(`/api/requests/exclude-batch`, {
    method: "POST",
    body: JSON.stringify({ items }),
  });
}

// promoteRequest bumps a Pending / Pending Retry / Scheduled grab to the front
// of the retry schedule (retry_after = now).
export function promoteRequest(grabId: number): Promise<void> {
  const body: PromoteRequestRequest = { grabId };
  return api<void>(`/api/requests/promote`, {
    method: "POST",
    body: JSON.stringify(body),
  });
}

// fetchMissingEpisodes lists catalogued episodes with no file on disk for one
// tracked series.
export function fetchMissingEpisodes(
  tmdbId: number,
): Promise<MissingEpisodesResponse> {
  return api<MissingEpisodesResponse>(
    `/api/modes/series/library/tmdb/${tmdbId}/missing-episodes`,
  );
}
