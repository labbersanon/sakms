// Per-season monitoring data access — the Series-only season list behind
// Library's detail panel, plus the new-season discovery toggle behind Settings →
// Library (Series).
//
// Kept out of api/settings.ts for the same reason api/usenet.ts is: that module
// is the connections + per-mode settings surface, and these four routes belong
// to one feature. The discovery toggle lives here rather than there even though
// its path is under /api/settings/ — same precedent as
// /api/settings/usenet-autograb-enabled living in api/usenet.ts.
//
// THE ROUTE SEGMENT IS A LITERAL `series`, NOT `{mode}` — deliberately. Seasons
// exist only for Series (Movies has no seasons, Adult has no episode model), so
// a wildcard would have to reject two of its three possible values at runtime.
// Don't "fix" it to match /api/modes/{mode}/library/root-folder's shape; see
// internal/api/airdatemonitor.go's own note.
//
// {seriesID} is the library_series row id, which IS the `id` field of a
// TrackedItem returned by GET /api/modes/series/tracked (see
// internal/api/tracked.go's Series branch) — no separate lookup is needed.
// Discover cards only have a TMDB id; /library/tmdb/{tmdbId}/seasons is the
// alias that reads and writes the same library_season_monitored rows.
//
// Wire shapes are the generated DTOs (@dto), never hand-duplicated.

import { api } from "./client";
import type {
  SeasonState,
  SetSeasonMonitoredRequest,
  SeriesNewSeasonDiscoveryResponse,
  SeriesNewSeasonDiscoveryRequest,
} from "@dto";

export type { SeasonState };

// SeasonKey is how a panel names the series: Library has the row id,
// Discover has the TMDB id. seriesID wins if both are somehow present so a
// nested Library popup cannot drift onto a different show's TMDB id.
export type SeasonKey = { seriesID?: number; tmdbId?: number };

function seasonsBase(key: SeasonKey): string {
  if (key.seriesID != null && key.seriesID > 0) {
    return `/api/modes/series/library/${key.seriesID}`;
  }
  if (key.tmdbId != null && key.tmdbId > 0) {
    return `/api/modes/series/library/tmdb/${key.tmdbId}`;
  }
  throw new Error("season routes need a library series id or a TMDB id");
}

export function fetchSeasonStatesFor(key: SeasonKey): Promise<SeasonState[]> {
  return api<SeasonState[]>(`${seasonsBase(key)}/seasons`);
}

export function putSeasonMonitoredFor(
  key: SeasonKey,
  seasonNumber: number,
  monitored: boolean,
): Promise<void> {
  const body: SetSeasonMonitoredRequest = { monitored };
  return api<void>(`${seasonsBase(key)}/seasons/${seasonNumber}/monitored`, {
    method: "PUT",
    body: JSON.stringify(body),
  });
}

export function putAllSeasonsMonitoredFor(
  key: SeasonKey,
  monitored: boolean,
): Promise<void> {
  const body: SetSeasonMonitoredRequest = { monitored };
  return api<void>(seasonsAllWritePath(key), {
    method: "PUT",
    body: JSON.stringify(body),
  });
}

// Library's all-seasons write is .../seasons/monitored. Discover cannot use
// the same suffix under /tmdb/{id}/: Go's ServeMux treats that as overlapping
// PUT .../{seriesID}/seasons/{seasonNumber}/monitored. PUT on the TMDB
// collection is the non-overlapping sibling; both still write
// library_season_monitored.
function seasonsAllWritePath(key: SeasonKey): string {
  const base = seasonsBase(key);
  if (key.seriesID != null && key.seriesID > 0) {
    return `${base}/seasons/monitored`;
  }
  return `${base}/seasons`;
}

// fetchSeasonStates lists one series' seasons with their episode/missing counts
// and monitored flag. A season with no monitored row at all reports
// `monitored: false` — there is no tri-state.
export function fetchSeasonStates(seriesID: number): Promise<SeasonState[]> {
  return fetchSeasonStatesFor({ seriesID });
}

// putSeasonMonitored flips one season's monitored flag — 204 No Content.
// Setting it false is not a pure flag write server-side: the handler also
// cancels that season's queued air-date retries in the same request.
export function putSeasonMonitored(
  seriesID: number,
  seasonNumber: number,
  monitored: boolean,
): Promise<void> {
  return putSeasonMonitoredFor({ seriesID }, seasonNumber, monitored);
}

// putAllSeasonsMonitored flips every season of one series in ONE round trip.
// It exists because an absent row means unmonitored, so adopting the feature on
// a large library would otherwise be a click per season. It still only writes a
// monitored flag — it grabs nothing.
export function putAllSeasonsMonitored(
  seriesID: number,
  monitored: boolean,
): Promise<void> {
  return putAllSeasonsMonitoredFor({ seriesID }, monitored);
}

// fetchSeriesNewSeasonDiscovery reads the new-season discovery toggle. Off by
// default.
export function fetchSeriesNewSeasonDiscovery(): Promise<boolean> {
  return api<SeriesNewSeasonDiscoveryResponse>(
    "/api/settings/series-new-season-discovery",
  ).then((r) => r.enabled);
}

// putSeriesNewSeasonDiscovery flips the toggle — 204 No Content. Unlike the
// usenet auto-grab toggle, this PUT writes no coupled interval: the discovery
// pass has no scheduler of its own, it runs inside the existing retry cycle.
export function putSeriesNewSeasonDiscovery(enabled: boolean): Promise<void> {
  const body: SeriesNewSeasonDiscoveryRequest = { enabled };
  return api<void>("/api/settings/series-new-season-discovery", {
    method: "PUT",
    body: JSON.stringify(body),
  });
}
