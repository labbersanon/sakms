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
  AdultDescription,
  AdultDiscoverItem,
  AdultSearchScene,
  AdultSearchScenesPage,
  AvailabilityPreview,
  DiscoverItem,
  PerformerSummary,
  PosterResponse,
  StudioSummary,
  TitleDetail,
  TrailerResponse,
} from "@dto";

export type {
  AdultDescription,
  AdultDiscoverItem,
  AdultSearchScene,
  AdultSearchScenesPage,
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

// TMDB_STILL_BASE is the size root for episode stills (w300), the landscape
// per-episode frame the season/episode picker's episode grid renders. Same host
// and same proxyImage wrapping as every other root here.
const TMDB_STILL_BASE = "https://image.tmdb.org/t/p/w300";

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

// tmdbStill turns a TMDB stillPath (episode still frame) into a proxied image
// URL; blank yields "" so the episode tile renders its title/number-only
// fallback. Mirrors tmdbPoster, only the size root differs (w300 stills).
export function tmdbStill(stillPath: string): string {
  if (!stillPath) return "";
  return proxyImage(TMDB_STILL_BASE + stillPath);
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
//
// `sections` scopes the response instead of adding a second endpoint (plan
// §3.1). Omitted — the DetailPopup's case — requests today's full bundle and
// produces a byte-identical URL to before this param existed. "seasons" asks
// for only the season/episode block, which is all SeasonEpisodePicker needs
// when it fetches for itself; the backend skips the credits/keywords/providers/
// recommendations fan-out entirely rather than computing and discarding it.
// An unrecognized value degrades to the full bundle server-side (never a 400),
// so this stays safe to extend.
export function fetchTitleDetail(
  mode: Exclude<Mode, "adult">,
  tmdbId: number,
  sections?: "seasons",
): Promise<TitleDetail> {
  return api<TitleDetail>(
    `/api/modes/${mode}/discover/detail?tmdbId=${tmdbId}` +
      (sections ? `&sections=${sections}` : ""),
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

// fetchAdultSearch runs Adult's one-shot catalog Search — GET /api/modes/adult/
// search?q=&page=N. Submitting an Adult query returns one card per identified
// scene. Clicking a card opens DetailPopup (same as browse); the popup's
// availability fetch is the grab path. page=1 (submit) fires exactly ONE
// bounded Prowlarr search alongside the RSS-pool query; page>1 pages the pool
// only and fires zero further Prowlarr (one Prowlarr call per one explicit
// operator action — the action being search-submit for Adult). Adult Discover's
// browse rows come from fetchAdultNewestRows/fetchMergedStudios/
// fetchMergedPerformers instead — this is the search path only.
export function fetchAdultSearch(
  query: string,
  page = 1,
): Promise<AdultSearchScenesPage> {
  const q = new URLSearchParams({ q: query, page: String(page) });
  return api<AdultSearchScenesPage>(`/api/modes/adult/search?${q.toString()}`);
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

// The StashBox type and its five fetchers (fetchStashBoxScenes/Studios/
// Performers/StudioScenes/PerformerScenes) lived here until 2026-08-02. They
// went with Adult Discover's optional StashDB/FansDB rows: every caller was
// deleted, and the 12 backend routes they addressed were deleted too, so they
// could only ever have 404'd. Note they were EXPORTED — noUnusedLocals cannot
// flag that shape, so this deletion was verified by grep, not by the build.
// internal/stashbox itself is untouched and still backs identification/enrich.
// Review if: an Adult Discover stash-box browse surface returns (any row, drill-
// down or search that needs a stash box's own scene/studio/performer catalog
// over HTTP) — then this tombstone has served its purpose and should be removed
// along with whatever replaces it.

// fetchAdultStudios/fetchAdultPerformers/fetchAdultStudioScenes/
// fetchAdultPerformerScenes: TPDB-only, no longer wired into the Studios/
// Performers browse rows or their drill-down — those now use
// fetchMergedStudios/fetchMergedPerformers/fetchMergedStudioScenes/
// fetchMergedPerformerScenes below (TPDB+StashDB merged). The backend routes
// these call still exist and still work; kept here pending a separate Task 8
// dead-code decision (needs sign-off before deletion — see
// .omc/plans/ralplan-merge-tpdb-stashbox-performers-studios.md's OQ3), not
// currently called anywhere in this codebase.
export function fetchAdultStudios(page = 1): Promise<StudioSummary[]> {
  return api<StudioSummary[]>(`/api/modes/adult/studios?page=${page}`);
}

export function fetchAdultPerformers(page = 1): Promise<PerformerSummary[]> {
  return api<PerformerSummary[]>(`/api/modes/adult/performers?page=${page}`);
}

export function fetchAdultStudioScenes(
  id: string,
  page = 1,
): Promise<AdultDiscoverItem[]> {
  return api<AdultDiscoverItem[]>(
    `/api/modes/adult/studios/${encodeURIComponent(id)}/scenes?page=${page}`,
  );
}

export function fetchAdultPerformerScenes(
  id: string,
  page = 1,
): Promise<AdultDiscoverItem[]> {
  return api<AdultDiscoverItem[]>(
    `/api/modes/adult/performers/${encodeURIComponent(id)}/scenes?page=${page}`,
  );
}

// fetchNewestEntityScenes is the RSS-derived Performers/Studios drill-down —
// GET /api/modes/adult/discover/newest/entity-scenes?kind=&name=&page=. page=1
// (drill-down open) returns only already-identified pool-matched items for
// this entity — fast, no live search. page=2 (only reached when the operator
// explicitly clicks "Show more") fires exactly ONE live Prowlarr search and
// checks the release-level match cache before enriching — see
// internal/api/adultdiscover_newest_scenes.go and CLAUDE.md's "Discover never
// queries Prowlarr" carve-out entry for this endpoint (one call per explicit
// Show More click, never automatically/per-scroll/per-card). The response is
// the {items, hasMore} envelope PaginatedStrip's load prop already supports
// (see the load type in screens/discover/shared.tsx): page=1's hasMore is
// always true (so "Show more" is always offered, even from an empty pool —
// that means the live search hasn't been tried yet, not a bug); page=2's
// hasMore is always false (there is no page 3).
export function fetchNewestEntityScenes(
  kind: "performer" | "studio",
  name: string,
  page = 1,
): Promise<{ items: AdultDiscoverItem[]; hasMore: boolean }> {
  const q = new URLSearchParams({ kind, name, page: String(page) });
  return api<{ items: AdultDiscoverItem[]; hasMore: boolean }>(
    `/api/modes/adult/discover/newest/entity-scenes?${q.toString()}`,
  );
}

// fetchAdultDescription is the Adult Discover description/bio fetch — ONE call per
// explicit operator action (a popup open, a drill-down open), one entity, zero
// Prowlarr. See CLAUDE.md's Adult drill-down carve-out.
//
// GET /api/modes/adult/discover/description?kind=&source=&id=&name= — a scene
// requires source (tpdb|stashdb|fansdb) + id; a performer/studio requires name
// and takes an OPTIONAL source (a box hint the drill already knows — omitted
// entirely, not sent as "", when the caller doesn't have one, so the backend
// resolves it itself from the matched-entity pool). See
// internal/api/adultdescription.go.
export function fetchAdultDescription(
  params:
    | { kind: "scene"; source: string; id: string }
    | { kind: "performer" | "studio"; name: string; source?: string },
): Promise<AdultDescription> {
  const q = new URLSearchParams({ kind: params.kind });
  if (params.kind === "scene") {
    q.set("source", params.source);
    q.set("id", params.id);
  } else {
    q.set("name", params.name);
    if (params.source) q.set("source", params.source);
  }
  return api<AdultDescription>(
    `/api/modes/adult/discover/description?${q.toString()}`,
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
//
// box/sceneId carry the catalog scene identity, sent only for catalog-sourced
// items with a non-empty id (A2(c) guard — never for prowlarr-sourced Show More
// items). The backend keys its persisted-release cache on box:sceneId so a
// re-open can be served without a Prowlarr search. performers is a soft identity
// signal server-side; it never hard-rejects a release. downloadUrl/protocol/
// sizeBytes carry an Adult card's already-known RSS/Show More enclosure so a
// zero-result Prowlarr search cannot erase a release the card already proved exists.
interface AvailabilityPreviewParams {
  title: string;
  tmdbId?: number;
  season?: number;
  episode?: number;
  studio?: string;
  releaseTitle?: string;
  durationSeconds?: number;
  box?: string;
  sceneId?: string;
  performers?: string[];
  downloadUrl?: string;
  protocol?: string;
  sizeBytes?: number;
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
    if (params.box) q.set("box", params.box);
    if (params.sceneId) q.set("sceneId", params.sceneId);
    if (params.performers && params.performers.length > 0) {
      q.set("performers", params.performers.join(","));
    }
    // Claude 2026-08-11: seed Adult availability with a known card enclosure.
    // Reason: an RSS/Show More download link remains usable even when Prowlarr
    // returns no raw title-search releases.
    // Troubleshooting: if the popup shows a zero-release empty state for a card
    // with a direct link, confirm these three params are present in the request.
    // Review if: availability accepts a request body instead of query params.
    if (params.downloadUrl) q.set("downloadUrl", params.downloadUrl);
    if (params.protocol) q.set("protocol", params.protocol);
    if (params.sizeBytes != null) q.set("sizeBytes", String(params.sizeBytes));
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
