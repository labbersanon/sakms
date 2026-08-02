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

// fetchSeasonStates lists one series' seasons with their episode/missing counts
// and monitored flag. A season with no monitored row at all reports
// `monitored: false` — there is no tri-state.
export function fetchSeasonStates(seriesID: number): Promise<SeasonState[]> {
  return api<SeasonState[]>(`/api/modes/series/library/${seriesID}/seasons`);
}

// putSeasonMonitored flips one season's monitored flag — 204 No Content.
// Setting it false is not a pure flag write server-side: the handler also
// cancels that season's queued air-date retries in the same request.
export function putSeasonMonitored(
  seriesID: number,
  seasonNumber: number,
  monitored: boolean,
): Promise<void> {
  const body: SetSeasonMonitoredRequest = { monitored };
  return api<void>(
    `/api/modes/series/library/${seriesID}/seasons/${seasonNumber}/monitored`,
    { method: "PUT", body: JSON.stringify(body) },
  );
}

// putAllSeasonsMonitored flips every season of one series in ONE round trip.
// It exists because an absent row means unmonitored, so adopting the feature on
// a large library would otherwise be a click per season. It still only writes a
// monitored flag — it grabs nothing.
export function putAllSeasonsMonitored(
  seriesID: number,
  monitored: boolean,
): Promise<void> {
  const body: SetSeasonMonitoredRequest = { monitored };
  return api<void>(`/api/modes/series/library/${seriesID}/seasons/monitored`, {
    method: "PUT",
    body: JSON.stringify(body),
  });
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
