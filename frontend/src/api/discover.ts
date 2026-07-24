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
  TitleDetail,
  TrailerResponse,
} from "@dto";

export type {
  AdultDiscoverItem,
  AvailabilityPreview,
  DiscoverItem,
  PerformerSummary,
  StudioSummary,
  TitleDetail,
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

// TMDB_PROFILE_BASE / TMDB_LOGO_BASE are the size-specific image roots for cast/
// crew headshots (w185) and watch-provider logos (w92) the DetailPopup renders.
// Same host as TMDB_POSTER_BASE — every one of these is wrapped by proxyImage so
// the byte flows through the Go image proxy, never a direct browser→TMDB request
// (plan Decision #7 / F1 acceptance: no image.tmdb.org host reaches an <img src>).
const TMDB_PROFILE_BASE = "https://image.tmdb.org/t/p/w185";
const TMDB_LOGO_BASE = "https://image.tmdb.org/t/p/w92";

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

// tmdbProfile turns a TMDB profilePath (cast/crew headshot) into a proxied image
// URL; blank yields "" so the card renders its text fallback. Mirrors tmdbPoster
// exactly, only the size root differs (w185 headshots).
export function tmdbProfile(profilePath: string): string {
  if (!profilePath) return "";
  return proxyImage(TMDB_PROFILE_BASE + profilePath);
}

// tmdbLogo turns a TMDB logoPath (watch-provider logo) into a proxied image URL;
// blank yields "". Mirrors tmdbPoster, only the size root differs (w92 logos).
export function tmdbLogo(logoPath: string): string {
  if (!logoPath) return "";
  return proxyImage(TMDB_LOGO_BASE + logoPath);
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

// DiscoverSortBy is the UI sort key fetchDiscoverFiltered sends (backend maps
// it to a TMDB sort_by allow-list). "popularity" is the default; the filter
// bar surfaces it as "Most Popular"/"Highest Rated"/"Newest".
export type DiscoverSortBy = "popularity" | "rating" | "newest";

// DiscoverFilterParams is the optional ad-hoc filter surface the Mainstream
// filter bar drives — every field omitted (zero-length genreIds, null year/
// rating) simply isn't sent, so an empty params object is a plain unfiltered
// browse. studioId/networkId aren't exposed by the bar today but are accepted
// so the same function can back a studio/network-scoped filter later.
export interface DiscoverFilterParams {
  genreIds?: number[];
  year?: number;
  minRating?: number;
  sortBy?: DiscoverSortBy;
  studioId?: number;
  networkId?: number;
}

// fetchDiscoverFiltered runs the real TMDB /discover query (category=filter)
// for Movies/Series — the only TMDB path that accepts genre/year/rating/sort,
// unlike the fixed trending/popular/upcoming curated lists. Each optional
// param is set only when present, the same conditional-URLSearchParams shape
// fetchAvailabilityPreview uses. DiscoverCategory is intentionally NOT widened
// to include "filter" — this is a separate function, since a filtered browse
// replaces the carousels rather than being one of them.
export function fetchDiscoverFiltered(
  mode: Exclude<Mode, "adult">,
  params: DiscoverFilterParams,
  page = 1,
): Promise<DiscoverItem[]> {
  const q = new URLSearchParams();
  q.set("category", "filter");
  q.set("page", String(page));
  if (params.genreIds && params.genreIds.length > 0) {
    q.set("genreIds", params.genreIds.join(","));
  }
  if (params.year != null) q.set("year", String(params.year));
  if (params.minRating != null) q.set("minRating", String(params.minRating));
  if (params.sortBy) q.set("sortBy", params.sortBy);
  if (params.studioId != null) q.set("studioId", String(params.studioId));
  if (params.networkId != null) q.set("networkId", String(params.networkId));
  return api<DiscoverItem[]>(`/api/modes/${mode}/discover?${q.toString()}`);
}

// fetchTrailer resolves one Movies/Series title's YouTube trailer URL (via
// GET /api/modes/{mode}/discover/trailer) — DetailPopup's "Watch Trailer"
// link. Returns "" (not an error) when TMDB has no matching trailer on file,
// same never-an-error convention as fetchTitlePoster.
export function fetchTrailer(
  mode: Exclude<Mode, "adult">,
  tmdbId: number,
): Promise<string> {
  return api<TrailerResponse>(
    `/api/modes/${mode}/discover/trailer?tmdbId=${tmdbId}`,
  ).then((r) => r.url);
}

// fetchTitleDetail resolves one Movies/Series title's rich detail bundle for the
// DetailPopup — cast, crew, keywords, watch providers, extended metadata, and
// "more like this" recommendations — from the combined, parallel-fanned-out
// GET /api/modes/{mode}/discover/detail?tmdbId=N. The backend soft-fails each
// sub-call, so a missing section arrives as an empty array/string rather than a
// popup-wide error (see internal/api/discover_detail.go). Movies/Series only:
// Adult scenes have no TMDB id and never call this. This is one explicit-click,
// per-title fetch — NOT the banned automatic per-card availability probe, and
// not Prowlarr (see CLAUDE.md's "Discover never queries Prowlarr" note).
export function fetchTitleDetail(
  mode: Exclude<Mode, "adult">,
  tmdbId: number,
): Promise<TitleDetail> {
  return api<TitleDetail>(
    `/api/modes/${mode}/discover/detail?tmdbId=${tmdbId}`,
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

// fetchDiscoverCalendar returns the Movies-release / TV-premiere items whose
// release date falls in [from, to] (inclusive, YYYY-MM-DD) for one mode —
// GET /api/modes/{mode}/discover/calendar. Backs CalendarView's month grid; the
// caller buckets the returned DiscoverItem[] by releaseDate into day cells. v1
// is Movies release dates + TV first-air premieres; a per-episode air-date
// calendar is a documented follow-up (heavier, per-episode queries).
export function fetchDiscoverCalendar(
  mode: Exclude<Mode, "adult">,
  from: string,
  to: string,
): Promise<DiscoverItem[]> {
  const q = new URLSearchParams({ from, to });
  return api<DiscoverItem[]>(
    `/api/modes/${mode}/discover/calendar?${q.toString()}`,
  );
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

// AdultSortBy is the TPDB browse order fetchAdultDiscoverSorted passes through
// as the backend's orderBy param (allow-listed server-side). "recently_released"
// is part of the contract but the sort bar surfaces the phash-deduped merged
// "Newest Releases" feed (fetchAdultDiscoverMergedRecent) for that intent
// instead, so only recently_created ("Recently Added") / recently_updated
// ("Recently Updated") reach this function from the bar.
export type AdultSortBy =
  | "recently_released"
  | "recently_created"
  | "recently_updated";

// fetchAdultDiscoverSorted returns one page of TPDB's scene catalog in the
// given sort order — the TPDB-only sort path (Recently Added/Updated). Newest
// Releases uses fetchAdultDiscoverMergedRecent instead (TPDB+StashDB merged).
export function fetchAdultDiscoverSorted(
  sortBy: AdultSortBy,
  page = 1,
): Promise<AdultDiscoverItem[]> {
  return api<AdultDiscoverItem[]>(
    `/api/modes/adult/discover?sortBy=${sortBy}&page=${page}&perPage=20`,
  );
}

// fetchAdultDiscoverMergedRecent returns the TPDB+StashDB merged "newest"
// feed (recently_released + StashDB date-sort, deduped by phash, graceful
// TPDB-only fallback). Backs the Adult sort bar's "Newest Releases". The
// backend route (GET /api/modes/adult/discover/recent-merged) never went away
// — this wrapper was dropped in the 2026-07-15 newest-rows redesign when its
// one caller was removed, and is reintroduced here for the sort bar.
export function fetchAdultDiscoverMergedRecent(
  page = 1,
): Promise<AdultDiscoverItem[]> {
  return api<AdultDiscoverItem[]>(
    `/api/modes/adult/discover/recent-merged?page=${page}&perPage=20`,
  );
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
// grabs.Grab.SeasonSpecified's convention); Adult uses studio + durationSeconds
// instead of a TMDB id, plus releaseTitle (see AdultDiscoverItem.releaseTitle
// — the raw Prowlarr release title the backend prefers as its search query
// when present, since it's real indexer vocabulary that already matched once,
// unlike a query reconstructed from title/studio).
interface AvailabilityPreviewParams {
  title: string;
  tmdbId?: number;
  season?: number;
  episode?: number;
  studio?: string;
  releaseTitle?: string;
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
    if (params.releaseTitle) q.set("releaseTitle", params.releaseTitle);
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
