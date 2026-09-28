// Claude 2026-09-28: TMDB / IMDb list-ingest settings (local structs, not apidto).
// Reason: same pattern as Trakt watchlist ingest — opt-in toggle + ID list.
// Troubleshooting: GET/PUT /api/tmdb/list-ingest and /api/imdb/list-ingest.

import { api } from "./client";

export type TMDBListIngest = {
  enabled: boolean;
  listIds: string;
  hasAccountSession: boolean;
};

export type IMDbListIngest = {
  enabled: boolean;
  listIds: string;
};

export function fetchTMDBListIngest(): Promise<TMDBListIngest> {
  return api<TMDBListIngest>("/api/tmdb/list-ingest");
}

export function putTMDBListIngest(body: {
  enabled: boolean;
  listIds: string;
}): Promise<void> {
  return api<void>("/api/tmdb/list-ingest", {
    method: "PUT",
    body: JSON.stringify(body),
  });
}

export function fetchIMDbListIngest(): Promise<IMDbListIngest> {
  return api<IMDbListIngest>("/api/imdb/list-ingest");
}

export function putIMDbListIngest(body: {
  enabled: boolean;
  listIds: string;
}): Promise<void> {
  return api<void>("/api/imdb/list-ingest", {
    method: "PUT",
    body: JSON.stringify(body),
  });
}
