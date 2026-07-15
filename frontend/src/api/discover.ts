// Discover data access — the read-only slice of SAK's Discover surface this
// wave ships (TMDB title lists / TPDB scene lists + lazy poster art). Discovery
// is sourced purely from TMDB/TPDB and the local library; Prowlarr is never
// consulted here (there is no per-card availability probe — that only happens
// later, when a grab actually retrieves a title). Every call goes through api()
// (src/api/client.ts) so it inherits the session cookie and the global 401 →
// re-boot session-expiry fallback. Request/response shapes are the generated
// DTOs (@dto), never hand-duplicated (plan Guardrail #4).

import { api } from "./client";
import type {
  AdultDiscoverItem,
  AvailabilityPreview,
  DiscoverItem,
  PerformerSummary,
  PosterResponse,
  StudioSummary,
} from "@dto";

export type {
  AdultDiscoverItem,
  AvailabilityPreview,
  DiscoverItem,
  PerformerSummary,
  StudioSummary,
};


// Mode is the three top-level libraries. Movies/Series share the TMDB
// title-shaped Discover path; Adult is scene-shaped (TPDB).
export type Mode = "movies" | "series" | "adult";

// ProposalStatus narrows the DTO's `status: string` to the four lifecycle
// values proposals.Status emits. The single shared definition for every review
// workflow (Rename/Purge/Dedup), which each re-export — the same shared-narrow
// pattern as Mode, kept out of apidto so the generated DTO stays a minimal wire
// mirror.
export type ProposalStatus = "pending" | "unmatched" | "applied" | "dismissed";

// DiscoverCategory selects which TMDB list a Movies/Series row renders —
// "trending" | "popular" | "upcoming", all three confirmed against task #5's
// committed discoverHandler (internal/api/discover.go), which also accepts
// "genre"/"studio"/"network" (with a required genreId/studioId/networkId
// query param) for the admin slider system's per-filter resolve path — those
// three aren't used directly here since Discover's genre/studio/network rows
// go through discoverSliders.ts's slider-resolve endpoint instead of a fixed
// category row.
export type DiscoverCategory = "trending" | "popular" | "upcoming";

// TMDB_POSTER_BASE builds a full image.tmdb.org URL from a bare posterPath
// (e.g. "/abc.jpg"). The browser never requests this host directly —
// proxyImage() wraps it so every byte flows through the Go image proxy (plan
// Decision #7). w342 is the grid poster size the old frontend used.
const TMDB_POSTER_BASE = "https://image.tmdb.org/t/p/w342";

// proxyImage rewrites an absolute upstream image URL into a same-origin image
// proxy request. This is the ONLY way images reach the DOM in this app: an
// <img src> must be proxyImage(...)'d, never the raw upstream URL. Returns ""
// for a blank input so callers can Show/skip a missing thumbnail.
export function proxyImage(rawURL: string): string {
  if (!rawURL) return "";
  return "/api/images/proxy?url=" + encodeURIComponent(rawURL);
}

// tmdbPoster turns a TMDB posterPath into a proxied grid image URL. A blank
// posterPath yields "" (no image), which the card renders as a text-only
// fallback.
export function tmdbPoster(posterPath: string): string {
  if (!posterPath) return "";
  return proxyImage(TMDB_POSTER_BASE + posterPath);
}

// fetchDiscover returns one TMDB category (trending/popular) for Movies/Series,
// for the given 1-based page (defaults to 1). Discover's per-row "Show more"
// requests the next page and appends it — page 1 and page 2 return different
// TMDB results (backend threads ?page through to TMDB, which paginates both
// trending and popular).
export function fetchDiscover(
  mode: Exclude<Mode, "adult">,
  category: DiscoverCategory,
  page = 1,
): Promise<DiscoverItem[]> {
  return api<DiscoverItem[]>(
    `/api/modes/${mode}/discover?category=${category}&page=${page}`,
  );
}

// fetchTitlePoster lazily resolves one library card's TMDB poster path by
// tmdbId (Movies/Series only) — the library caches no poster art, so each
// rendered existing-library card fetches its own poster on demand. The library
// row paginates, so only one page's worth of these fetch at a time rather than
// an N+1 across the whole tracked list. Returns "" when TMDB has no art (the
// card then renders its text fallback).
export function fetchTitlePoster(
  mode: Exclude<Mode, "adult">,
  tmdbId: number,
): Promise<string> {
  return api<PosterResponse>(
    `/api/modes/${mode}/poster?tmdbId=${tmdbId}`,
  ).then((r) => r.posterPath);
}

// fetchTmdbSearch runs a TMDB title search for one mode (Movies/Series) — the
// same GET /api/modes/{mode}/tmdb-search endpoint Rename's Re-pick uses.
// Discover's Mainstream search calls it for both movies and series and merges
// the results into one grid.
export function fetchTmdbSearch(
  mode: Exclude<Mode, "adult">,
  query: string,
): Promise<DiscoverItem[]> {
  return api<DiscoverItem[]>(
    `/api/modes/${mode}/tmdb-search?q=${encodeURIComponent(query)}`,
  );
}

// fetchAdultDiscover returns one page of TPDB's scene catalog (plain browse),
// or a title search when query is non-empty. This is the search path — Adult
// Discover's browse rows come from fetchAdultNewestRows/fetchAdultStudios/
// fetchAdultPerformers instead (the old fixed Recently Released/Highest Rated
// category rows were removed 2026-07-15, stale/redundant once the
// Prowlarr-matched newest rows shipped).
export function fetchAdultDiscover(query?: string): Promise<AdultDiscoverItem[]> {
  const q = query?.trim();
  const path = q
    ? `/api/modes/adult/discover?q=${encodeURIComponent(q)}`
    : `/api/modes/adult/discover`;
  return api<AdultDiscoverItem[]>(path);
}

// StashBox names the two OPTIONAL Adult Discover sources (StashDB, FansDB) —
// stash-box-protocol catalogs shown only when that connection is configured
// (unlike TPDB, the required core source). The backend returns [] (200) for an
// unconfigured box, so the frontend hides the row rather than showing an error.
export type StashBox = "stashdb" | "fansdb";

// fetchStashBoxScenes returns one page of an optional stash-box source's scene
// feed — kind "recent" (date-sorted) or "trending" (stash-box's server-side
// TRENDING order). An unconfigured box yields [] (the caller only renders the
// row when the connection exists, so this is a defensive fallback).
export function fetchStashBoxScenes(
  box: StashBox,
  kind: "recent" | "trending",
  page = 1,
): Promise<AdultDiscoverItem[]> {
  return api<AdultDiscoverItem[]>(
    `/api/modes/adult/discover/${box}/${kind}?page=${page}&perPage=20`,
  );
}

// fetchStashBoxStudios returns one page of an optional stash-box source's studio
// catalog. Same shape/drill-down contract as fetchAdultStudios (TPDB), just a
// different source.
export function fetchStashBoxStudios(
  box: StashBox,
  page = 1,
): Promise<StudioSummary[]> {
  return api<StudioSummary[]>(
    `/api/modes/adult/discover/${box}/studios?page=${page}&perPage=20`,
  );
}

// fetchStashBoxPerformers returns one page of an optional stash-box source's
// performer catalog. Same shape as fetchAdultPerformers (TPDB).
export function fetchStashBoxPerformers(
  box: StashBox,
  page = 1,
): Promise<PerformerSummary[]> {
  return api<PerformerSummary[]>(
    `/api/modes/adult/discover/${box}/performers?page=${page}&perPage=20`,
  );
}

// fetchAdultStudios returns one page of TPDB's studio (site) catalog for the
// Studios browse row. Each card's opaque id doubles as the {id} path segment of
// fetchAdultStudioScenes below.
export function fetchAdultStudios(page = 1): Promise<StudioSummary[]> {
  return api<StudioSummary[]>(`/api/modes/adult/studios?page=${page}`);
}

// fetchAdultPerformers returns one page of TPDB's performer catalog for the
// Performers browse row. Each card's opaque id doubles as the {id} path segment
// of fetchAdultPerformerScenes below.
export function fetchAdultPerformers(page = 1): Promise<PerformerSummary[]> {
  return api<PerformerSummary[]>(`/api/modes/adult/performers?page=${page}`);
}

// fetchAdultStudioScenes is the studio drill-down: one page of just the scenes
// for a studio id (a StudioSummary.id, passed verbatim as an opaque string).
// Returns the same scene shape as fetchAdultDiscover.
export function fetchAdultStudioScenes(
  id: string,
  page = 1,
): Promise<AdultDiscoverItem[]> {
  return api<AdultDiscoverItem[]>(
    `/api/modes/adult/studios/${encodeURIComponent(id)}/scenes?page=${page}`,
  );
}

// fetchAdultPerformerScenes is the performer drill-down: one page of just the
// scenes for a performer id (a PerformerSummary.id, passed verbatim as an opaque
// string). Returns the same scene shape as fetchAdultDiscover.
export function fetchAdultPerformerScenes(
  id: string,
  page = 1,
): Promise<AdultDiscoverItem[]> {
  return api<AdultDiscoverItem[]>(
    `/api/modes/adult/performers/${encodeURIComponent(id)}/scenes?page=${page}`,
  );
}

// AvailabilityPreviewParams is the union of every query param
// discoverAvailabilityHandler (internal/api/discover_availability.go) reads
// across all three modes. Every mode requires `title` (the backend's fast
// title-match filter pass needs a known canonical title to compare release
// titles against — the Discover card already has it client-side, cheaper
// than an extra TMDB call solely to recover it). Movies uses tmdbId; Series
// additionally needs season/episode (episode 0 = season pack, matching
// grabs.Grab.SeasonSpecified's convention); Adult uses studio +
// durationSeconds instead of a TMDB id.
export interface AvailabilityPreviewParams {
  title: string;
  tmdbId?: number;
  season?: number;
  episode?: number;
  studio?: string;
  durationSeconds?: number;
}

// fetchAvailabilityPreview runs DetailPopup's one upfront, user-click-
// triggered Prowlarr search for one title/scene — GET /api/modes/{mode}/
// discover/availability — and returns the full 4-resolution × 4-tier ×
// 2-protocol grid backing every selector combination the popup offers, so
// switching any selector re-renders instantly against already-fetched data
// (no refetch per selection change). This is NOT a reintroduction of the
// removed automatic per-card Discover→Prowlarr probe: it fires once, only
// when an operator explicitly opens a card's detail popup (see CLAUDE.md's
// "Discover never queries Prowlarr" note and its 2026-07-14 clarification).
export function fetchAvailabilityPreview(
  mode: Mode,
  params: AvailabilityPreviewParams,
): Promise<AvailabilityPreview> {
  const q = new URLSearchParams();
  q.set("title", params.title);
  if (mode === "adult") {
    if (params.studio) q.set("studio", params.studio);
    if (params.durationSeconds != null) {
      q.set("durationSeconds", String(params.durationSeconds));
    }
  } else {
    if (params.tmdbId != null) q.set("tmdbId", String(params.tmdbId));
    if (mode === "series") {
      if (params.season != null) q.set("season", String(params.season));
      if (params.episode != null) q.set("episode", String(params.episode));
    }
  }
  return api<AvailabilityPreview>(
    `/api/modes/${mode}/discover/availability?${q.toString()}`,
  );
}
