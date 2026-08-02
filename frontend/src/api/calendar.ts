// Calendar data access (Wave 1 of calendar-grabs-requests): typed clients for
// the three new Calendar routes — Upcoming/Series, Upcoming/Movies, and the
// pre-release Request click. Every call goes through api() (src/api/client.ts)
// so it inherits the session cookie and the global 401 → re-boot
// session-expiry fallback. Request/response shapes are the generated DTOs
// (@dto), never hand-duplicated (plan Guardrail #4).
//
// Wave-structure note: the backend handlers for these three routes
// (internal/api/calendar_upcoming.go, calendar_prerelease.go) are Wave 2
// work and do not exist yet as of this file landing — these clients exist so
// Wave 3's frontend screens (Upcoming.tsx) can import them once Wave 2 lands.
// Both Upcoming routes require from/to (YYYY-MM-DD, inclusive), the same
// contract fetchDiscoverCalendar already follows (discover.ts) and
// discoverCalendarHandler enforces server-side (discover_detail.go:314-318).

import { api } from "./client";
import type {
  PreReleaseRequestRequest,
  PreReleaseRequestResponse,
  UpcomingMovieEntry,
  UpcomingSeriesEntry,
} from "@dto";

export type {
  PreReleaseRequestRequest,
  PreReleaseRequestResponse,
  UpcomingMovieEntry,
  UpcomingSeriesEntry,
};

// fetchUpcomingSeries returns not-yet-aired episodes of already-tracked
// series whose air date falls in [from, to] (inclusive, YYYY-MM-DD) —
// GET /api/calendar/upcoming/series.
export function fetchUpcomingSeries(
  from: string,
  to: string,
): Promise<UpcomingSeriesEntry[]> {
  const q = new URLSearchParams({ from, to });
  return api<UpcomingSeriesEntry[]>(
    `/api/calendar/upcoming/series?${q.toString()}`,
  );
}

// fetchUpcomingMovies returns upcoming (digital or planned) movie releases
// whose release date falls in [from, to] (inclusive, YYYY-MM-DD) — open-ended,
// not bounded to existing Requests — GET /api/calendar/upcoming/movies.
export function fetchUpcomingMovies(
  from: string,
  to: string,
): Promise<UpcomingMovieEntry[]> {
  const q = new URLSearchParams({ from, to });
  return api<UpcomingMovieEntry[]>(
    `/api/calendar/upcoming/movies?${q.toString()}`,
  );
}

// requestPreRelease is the click-to-request affordance on an un-requested
// upcoming movie: it parks a grab row held until the release date —
// POST /api/calendar/prerelease-request. alreadyRequested is true when the
// click matched an existing outstanding request instead of creating one.
export function requestPreRelease(
  body: PreReleaseRequestRequest,
): Promise<PreReleaseRequestResponse> {
  return api<PreReleaseRequestResponse>(`/api/calendar/prerelease-request`, {
    method: "POST",
    body: JSON.stringify(body),
  });
}
