# Progressive `pending_retry` backoff

Shipped 2026-09-13 (PR [#36](https://github.com/labbersanon/sakms/pull/36)),
deployed to server1 as `ebdad716da0e4d30050c62f25bf312e7488fab25`.

## Goal

When a title has no suitable download, it must not monopolize every retry
cycle. Spacing chronic failures out lets other queued `pending_retry` rows
actually get re-searched and (when they clear the quality floor) dispatched.

This is **fairness for the due re-search set**, not download-slot fairness.
A stuck active NZB/torrent holding usenet max-concurrent downloads is a
different problem.

## Schedule

Every `pending_retry` park shares one ladder in `grabs.RetryBackoff` /
`ParkWithBackoff`:

| `retry_count` after park | Next attempt |
| ------------------------ | ------------ |
| ≤ 0 (Create mint)        | 24 hours     |
| 1                        | 3 days       |
| 2                        | 10 days      |
| 3                        | 30 days      |
| 4                        | 60 days      |
| ≥ 5                      | 90 days (plateau; never stops) |

- `ParkWithBackoff` uses the **post-increment** count (`retry_count + 1`).
- Brand-new rows from `Create` keep `retry_count = 0` and park at
  `RetryBackoff(0)` (24h) themselves.
- The schedule never returns 0 duration (that would reintroduce a tight loop).

Air-date monitoring delegates to the same function (`airDateRetryBackoff` →
`grabs.RetryBackoff`). There is no separate air-date table anymore.

## What the settings key still does

`usenet_retry_interval_seconds` (and the coupled auto-grab toggle side
effect that writes 86400 / 0) controls **how often the scheduler wakes** to
look for due rows. It does **not** set how far ahead a park writes
`retry_after`.

## Who parks through this

All production parks that land on `pending_retry` go through
`ParkWithBackoff` or `RetryBackoff(0)` on Create, including:

- quality-floor / no-match re-searches (`parkPendingRetry`)
- async retrieval failures (`parkGrabForRetry`)
- stale-torrent cancel-and-requeue
- pre-release promotion no-match
- air-date shaped rows (same ladder; sweep may still rewrite reason)

## How to observe

On a parked grab row, after successive no-matches:

1. `retry_count` increments on each `SetPendingRetry` park.
2. `retry_after` should jump by the ladder step for that count
   (first Create park ≈ +24h; after the first increment ≈ +3d; …).
3. `DueForRetry` is `ORDER BY retry_after ASC, id ASC` — chronic failures
   drop out of the daily due set as their `retry_after` moves further out.

## Code pointers

- `internal/grabs/retry.go` — `RetryBackoff`, `ParkWithBackoff`
- `internal/api/autograb_shared.go` — `parkPendingRetry`, `parkGrabForRetry`
- `internal/api/airdatemonitor.go` — `airDateRetryBackoff` delegate
- `CHANGELOG.md` — 2026-09-13 entry
- `CLAUDE.md` — AMENDED 2026-09-13 under the unattended Usenet auto-grab bounds

## Related: operator Promote

Requests **Promote** (`grabs.PromoteToFront`) sets `retry_after = now` without
resetting `retry_count`, so it pulls one row forward without restarting this
ladder. See `docs/requests-grab-promote.md`.
