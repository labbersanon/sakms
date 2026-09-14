# Requests: Grab, Search & pick, Promote, series episodes

Shipped 2026-09-14 (PR [#38](https://github.com/labbersanon/sakms/pull/38)),
deployed to server1 as `bd1bc619ac252cc2aa70e2650c9b8fea07bff09e`.

## Goal

Give operators direct control on the Requests worklist without waiting for the
next DueForRetry cycle — especially for titles parked far out on the progressive
`pending_retry` ladder (`docs/pending-retry-backoff.md`).

## UI surface

| Action | Where | Meaning |
| ------ | ----- | ------- |
| **Grab** | Requests row (auto) | Same auto-grab path as Discover (`GrabDialog` / `GrabTarget`) |
| **Search & pick** | Requests row | Open release search / `DetailPopup` (Adult falls back to GrabDialog) |
| **Promote** | Requests row with `grabId` | Move that grab to the front of the **regular** retry schedule |
| Series detail | Click a Series row | Missing-episode list + per-episode Grab / Search & pick |

Row actions appear for every Requests status **except** In Library and
Downloading. Promote additionally requires `grabId > 0`.

Promote is **not** download-client priority (NZB/torrent queue rank). It only
affects when sakms considers the grab due for re-search / dispatch.

## Backend: Promote

`POST /api/requests/promote`

```json
{ "grabId": 123 }
```

→ `204` on success; `404` if the grab is missing or not eligible.

Implementation: `grabs.PromoteToFront` (`internal/grabs/grabs.go`), wired by
`promoteRequestHandler` (`internal/api/requests_promote.go`).

### What it writes

| Field | Effect |
| ----- | ------ |
| `status` | Forced to `pending_retry` |
| `retry_after` | Set to **now** (due on the next scheduler pass that calls `DueForRetry`) |
| `retry_reason` | `"operator promoted to top of schedule"` (operator-facing only) |
| `download_gid` | Cleared — promoted row rejoins the retry track and must not claim a live download |
| `hold_until` | If still in the future, written to **now** (clears `DueForRetry`'s hold guard for Calendar pre-release rows) |

### What it deliberately leaves alone

- **`retry_count` is not reset and not incremented.** Promote is not an
  attempt. After the next no-match park, the row stays on the same
  `RetryBackoff` step it already earned (e.g. still at the 30d rung if
  `retry_count` was 3). See interaction with backoff below.
- Download-client priority / NZB category / torrent queue position — untouched
  (and there is no live GID after the clear above).

### Eligibility (store)

`WHERE status IN ('pending_retry', 'queued')`. Other statuses (downloading,
imported, failed, …) → `ErrNotFound` → HTTP 404.

UI "Scheduled" / "Pending" rows map onto those store statuses (queued /
pending_retry), including held Calendar rows that still have a future
`hold_until`.

## Backend: missing episodes

`GET /api/modes/series/library/tmdb/{tmdbId}/missing-episodes`

→ `MissingEpisodesResponse` (`tmdbId`, `title`, `episodes[]` with
`seasonNumber`, `episodeNumber`, `title`, `airDate`).

Requires the series to already be in the library store (`404` otherwise).
Backed by `library.MissingEpisodes` via
`missingEpisodesByTMDBHandler` (`internal/api/missing_episodes.go`).

Frontend: `RequestsSeriesDetail` + `fetchMissingEpisodes`
(`frontend/src/api/requests.ts`).

## Interaction with progressive `pending_retry` backoff

Backoff (`docs/pending-retry-backoff.md`) spaces chronic no-matches so other
rows get due cycles. Promote is the operator escape hatch for one title:

1. Chronic no-match parks with `ParkWithBackoff` → large `retry_after`,
   `retry_count` bumped.
2. Operator clicks **Promote** → `retry_after = now`, **`retry_count` unchanged**.
3. Next DueForRetry cycle can pick the row up immediately (subject to
   scheduler tick / auto-grab gates).
4. If that attempt no-matches again, `ParkWithBackoff` uses the **existing**
   count — it does **not** restart at 24h.

So Promote buys an early attempt without rewriting attempt history. If product
later wants "Promote also restarts the 24h ladder", that is an explicit
`retry_count` reset — see the `Review if:` note on `PromoteToFront`.

`usenet_retry_interval_seconds` still only wakes the scheduler; Promote does
not change that key.

## Code pointers

- `internal/grabs/grabs.go` — `PromoteToFront`
- `internal/grabs/promote_test.go`
- `internal/api/requests_promote.go` / `requests_promote_test.go`
- `internal/api/missing_episodes.go`
- `internal/apidto/dto.go` — `PromoteRequestRequest`, `MissingEpisodeItem`,
  `MissingEpisodesResponse`
- `frontend/src/api/requests.ts` — `promoteRequest`, `fetchMissingEpisodes`
- `frontend/src/screens/Requests.tsx` / `RequestsSeriesDetail.tsx`
- `CHANGELOG.md` — 2026-09-14 entry
- `docs/pending-retry-backoff.md` — ladder Promote does not reset
