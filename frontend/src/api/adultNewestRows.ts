// Admin Adult "newest" Discover row data access (Stage 2) — CRUD + reorder for
// operator-defined Prowlarr-backed rows, the distinct-genre reference lookup
// the editor's genre picker needs, and resolving a row's cached matched items
// for rendering on the Discover page itself (the Adult sibling of
// discoverSliders.ts, but Prowlarr-backed, not TMDB-backed). Every call goes
// through api() (src/api/client.ts) so it inherits the session cookie and the
// global 401 → re-boot session-expiry fallback. Request/response shapes are the
// generated DTOs (@dto), never hand-duplicated. Paths and DTOs were confirmed
// against internal/api/adult_newest_rows.go + internal/api/handler.go.
//
// Unlike Slider, there is no "target" concept and no required/forbidden
// filter-value pairing rule — genreFilter is ALWAYS optional for every row
// type, so there is no FILTER_NEEDS_VALUE-equivalent map.

import { api } from "./client";
import type {
  AdultNewestReleaseItem,
  AdultNewestRow,
  AdultNewestRowReorderRequest,
  AdultNewestRowUpsertRequest,
} from "@dto";

export type {
  AdultNewestReleaseItem,
  AdultNewestRow,
  AdultNewestRowUpsertRequest,
};

// ROW_TYPES mirrors adultnewest.RowType's fixed enum. The four defaults are
// already seeded server-side by the migration; this UI adds more rows (e.g. a
// genre-narrowed variant of an existing type).
export const ROW_TYPES = ["movie", "scene", "performer", "studio"] as const;
export type RowType = (typeof ROW_TYPES)[number];

// ROW_TYPE_LABELS is the human-readable label for each RowType, shown in the
// editor's row-type select and the row list.
export const ROW_TYPE_LABELS: Record<RowType, string> = {
  movie: "Movie",
  scene: "Scene",
  performer: "Performer",
  studio: "Studio",
};

// fetchAdultNewestRows lists every admin-defined row, already ordered by
// sortOrder (Store.List's own ordering) — GET /api/modes/adult/newest-rows.
export function fetchAdultNewestRows(): Promise<AdultNewestRow[]> {
  return api<AdultNewestRow[]>("/api/modes/adult/newest-rows");
}

export function createAdultNewestRow(
  body: AdultNewestRowUpsertRequest,
): Promise<AdultNewestRow> {
  return api<AdultNewestRow>("/api/modes/adult/newest-rows", {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export function updateAdultNewestRow(
  id: number,
  body: AdultNewestRowUpsertRequest,
): Promise<AdultNewestRow> {
  return api<AdultNewestRow>(`/api/modes/adult/newest-rows/${id}`, {
    method: "PUT",
    body: JSON.stringify(body),
  });
}

export function deleteAdultNewestRow(id: number): Promise<void> {
  return api<void>(`/api/modes/adult/newest-rows/${id}`, { method: "DELETE" });
}

// reorderAdultNewestRows sends the FULL new display order in one call — ids
// must cover every existing row exactly once (Store.Reorder's requirement),
// never a partial/per-item bulk mutation.
export function reorderAdultNewestRows(ids: number[]): Promise<void> {
  const body: AdultNewestRowReorderRequest = { ids };
  return api<void>("/api/modes/adult/newest-rows/reorder", {
    method: "POST",
    body: JSON.stringify(body),
  });
}

// fetchAdultNewestRowItems resolves one row's cached matched items for the
// given 1-based page — GET /api/modes/adult/newest-rows/{id}/resolve. Reads
// only the pre-computed cache (never Prowlarr at request time; see the
// handler's package doc). Also imported by the Discover-screen wiring later —
// keep its name/shape stable.
//
// The optional gender param narrows a Performer row to one discovered gender
// value (see fetchPerformerGenders) — the Adult Discover dynamic gender-split
// strips each pass their own gender leg. Omitting it is backward compatible:
// no gender query param is sent and the row returns items of every gender,
// exactly as before.
export function fetchAdultNewestRowItems(
  rowId: number,
  page = 1,
  gender?: string,
): Promise<AdultNewestReleaseItem[]> {
  const g = gender ? `&gender=${encodeURIComponent(gender)}` : "";
  return api<AdultNewestReleaseItem[]>(
    `/api/modes/adult/newest-rows/${rowId}/resolve?page=${page}${g}`,
  );
}

// fetchAdultNewestGenres backs the genre picker — the distinct genre names that
// ACTUALLY exist in cached matches (not a static taxonomy), so it can be empty
// on a fresh install before the background scan has run. GET
// /api/modes/adult/newest-rows/genres.
export function fetchAdultNewestGenres(): Promise<string[]> {
  return api<string[]>("/api/modes/adult/newest-rows/genres");
}

// fetchPerformerGenders backs the Adult Discover dynamic gender-split
// (Option 5A) — the distinct gender values actually present across cached
// Performer rows, sorted, non-empty. GET
// /api/modes/adult/newest-rows/performer-genders. One PaginatedStrip is
// rendered per value this returns, so a newly-backfilled/matched gender
// produces a new strip automatically, with no code change. Can be empty on a
// fresh install / before the gender backfill has populated any rows.
export function fetchPerformerGenders(): Promise<string[]> {
  return api<string[]>("/api/modes/adult/newest-rows/performer-genders");
}
