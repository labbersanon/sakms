# Usenet Transport Resilience (A + B + E)

Shipped 2026-09-17 on branch `cursor/usenet-transport-abe-c2d5`.

## Goal

Before this change, a dropped TCP connection during an NZB download caused the
whole download to fail and the request to be re-searched after the usual
multi-day backoff. The 14 "broken pipe" segment failures observed in production
evidence showed 88 % of live usenet failures were connection-level blips, not
missing articles. Three coordinated changes prevent those blips from burning
retry budget:

- **A — Segment reconnect**: retry the same article on a fresh connection inside
  `fetchSegmentAny` before surfacing an error to the download layer.
- **B — Transport park**: when a download does fail with a transport error,
  park it for a short resume (minutes, not days) with the GID preserved so
  `RelaunchNZB` can continue from the existing staging dir.
- **E — Park census + hygiene**: observability and maintenance endpoints to
  inspect the pending-retry queue, tag E2E rows, and reap test debris.

C (alternate-release retry for content failures) is shipped alongside A+B+E
on the same branch — see [usenet-alternate-release.md](usenet-alternate-release.md).
D (UI wire-up) is explicitly **out of scope** for this shipment.

---

## A — Per-segment reconnect

`internal/usenet/transport.go` defines `isTransportError`, which classifies
connection-level failures: broken pipe, EOF, ECONNRESET, `*net.OpError`, etc.

`internal/usenet/manager.go` → `fetchSegmentAny` now retries each pool up to
`maxSegmentAttemptsPerServer` (= 3) times before moving to the next
subscription. On a transport failure:

1. The bad socket is returned to the pool with `put(conn, false)`, which
   discards it and releases the live-connection token.
2. A short delay (`transportRetryDelay`: 250 ms on attempt 1, 750 ms on
   subsequent) lets the server recover a momentary blip.
3. A fresh `getCtx` dials a new connection and the BODY command is retried.

Article-level responses (430, 451) are **not** retried within the same pool —
only connection-level failures are. A 430 is still forwarded to the
cross-subscription fallback unchanged.

### Error classification

`classifySegmentFailure` wraps the final error with `ErrTransport` (using
multi-`%w` so both `errors.Is(err, ErrTransport)` and
`errors.Is(err, cause)` hold). This marker travels up through
`assembleFile` → `downloadAll` → `Download.Err` → `onError` /
`sweepUsenetFailures` where the api layer tests for it.

---

## B — Short-backoff transport park

When a download terminates with a transport error, `applyUsenetFailure`
(in `internal/api/usenetretry.go`) calls `parkUsenetTransportFailure`
before falling through to the multi-day `ParkWithBackoff`.

### Eligibility

All four conditions must hold for the transport park path to fire:

| Condition | Reason |
|---|---|
| `errors.Is(failure, ErrTransport)` | Only connection failures |
| GID has `"nzb-"` prefix | Only dispatched usenet grabs |
| `DownloadURL` non-empty | Needed for `RelaunchNZB` |
| `TransportRetryCount < MaxTransportRetries` (= 4) | Prevents infinite loop |

If any condition is false, the call returns `(false, nil)` and the caller
continues with normal `parkGrabForRetry`.

### Transport backoff ladder

`grabs.TransportBackoff(n)` where `n` is the **post-increment**
`transport_retry_count`:

| `transport_retry_count` after park | Next attempt |
|---|---|
| 1 | 2 minutes |
| 2 | 5 minutes |
| 3 | 15 minutes |
| 4 | 30 minutes |
| ≥ 5 | 0 (exhausted → fall through to days ladder) |

Cap 4 → total short-park window ≈ 52 minutes before escalation.

### What the transport park writes

`grabs.Store.ParkForTransportResume` (in `internal/grabs/grabs.go`):

- `status` → `pending_retry`
- `retry_after` → `now + TransportBackoff(count+1)`
- `retry_reason` → `transportRetryReason` (operator-friendly, no host/URL)
- `transport_retry_count` → incremented by 1
- `retry_count` → **unchanged** (transport blip is not a failed search attempt)
- `download_gid` → **unchanged** (required by `RelaunchNZB` for staging dir)

`SetPendingRetry` and `Relaunch` both reset `transport_retry_count` to 0,
so a successful resume clears the transport ladder.

### Resume pass

`resumeDueTransportRetries` runs at the end of `runUsenetRetryCycle` and at
boot (via `RunBootParkHygiene`). For each transport-parked row whose
`retry_after` has arrived:

1. **Skip** excluded titles.
2. **Gate** on free Usenet slots (`freeUsenetSlots`); return immediately when
   full (torrent escalation is D, out of scope).
3. **Inspect** the live engine via `FindByGID`:
   - Terminal status (error/complete/removed) → `Forget(gid)` then attempt
     `RelaunchNZB`.
   - Non-terminal (active/paused) → re-arm the row via `Relaunch` (queued,
     retry fields cleared) and skip.
4. **RelaunchNZB** outcomes:
   - nil → success; `Relaunch` the row (queued, transport counter cleared).
   - `ErrArticlesUnavailable` → articles are gone; clear GID via
     `parkGrabForRetry` (joins days-ladder re-search).
   - other error → advance to the next transport rung; escalate to days
     ladder at the cap.

---

## E — Park census and hygiene

### GET /api/requests/park-census

Returns a JSON snapshot of all `pending_retry` rows across modes (movies,
series, adult), bucketed by schedule distance and type.

```json
{
  "total": 61,
  "dueNow": 3,
  "dueWithin1h": 7,
  "parkedWithin24h": 12,
  "parkedWithin7d": 18,
  "parkedFar": 8,
  "awaitingResume": 5,
  "awaitingResumeOverdue": 2,
  "heldPreRelease": 4,
  "airDateShaped": 4,
  "testOrigin": 0,
  "malformedSchedule": 0,
  "generatedAt": "2026-09-17T10:00:00Z"
}
```

**Bucket rules** (applied in order; a row can appear in multiple counters
except for mutually exclusive distance buckets):

| Field | Condition |
|---|---|
| `malformedSchedule` | `retry_after` non-empty and unparseable — counted first, distance buckets skipped |
| `testOrigin` | `origin` non-empty |
| `heldPreRelease` | `hold_until` parses and is in the future |
| `airDateShaped` | Series + `tmdb_id>0` + `season_specified` + `episode_number>0` |
| `awaitingResume` | `download_gid` non-empty |
| `awaitingResumeOverdue` | `awaitingResume` AND `now − retry_after > strandedResumeGrace` (6h) |
| Distance buckets | Transport rows skip these (they are handled by `awaitingResume`) |

This endpoint is also called at boot and after each usenet retry cycle — the
single structured log line (`logParkCensus`) provides time-series context.

### POST /api/requests/park-hygiene

Operator maintenance endpoint for tagging or reaping test/E2E park rows.

**Request body:**
```json
{
  "action": "tag" | "reap",
  "origin": "" | "e2e",
  "ids": [1, 2, 3],        // optional explicit filter
  "reasonContains": "...", // optional dry-run filter
  "apply": false           // default dry-run
}
```

**Rules:**

- `apply: false` (default) → dry run: matches and returns IDs, mutates nothing.
- Only `status = pending_retry` rows are ever matched.
- `action: "tag"` sets the `origin` field (allowlist: `""` or `"e2e"`).
- `action: "reap"` requires a non-empty `origin` OR an explicit `ids` list.
  `reasonContains` alone **cannot** drive a reap (dry-run only).
- Reap writes `retry_reason = testParkReapedReason` and flips `status` to
  `Failed`. No DELETE — the row is terminal and auditable; staging ages out
  via stagingsweep.

### Automatic hygiene pass (`runParkHygiene`)

Runs at boot and at the end of each `runUsenetRetryCycle`. Three non-destructive
repairs:

1. **Stranded transport-resume recovery**: `pending_retry` + non-empty `download_gid`
   + parseable `retry_after` + `now − retry_after > strandedResumeGrace` (6h) →
   `parkGrabForRetry` (clears GID, joins days-ladder re-search). Prevents a
   transport-parked row from being invisible forever when drain is off.

2. **Malformed schedule repair**: `pending_retry` + non-empty `retry_after` +
   unparseable → `SetRetryAfter(RetryBackoff(retryCount+1))`. Repairs the 4 live
   rows with `"HH24"` corruption from an external writer.

3. **Census log line** (always, even when nothing was repaired).

---

## Runbook: tagging and reaping the 54 E2E rows

The production database has ≈54 rows from E2E test runs with `retry_reason`
containing `"e2e"` or similar. Use the hygiene endpoint to clean them up:

**Step 1 — dry-run to confirm matches:**
```sh
curl -s -X POST https://media-admin.zaena.us/api/requests/park-hygiene \
  -H 'Content-Type: application/json' \
  -d '{"action":"reap","origin":"e2e","apply":false}' | jq .
```

If `matched` is 0, use the `reasonContains` filter to verify:
```sh
curl -s -X POST .../api/requests/park-hygiene \
  -d '{"action":"reap","origin":"","reasonContains":"e2e","apply":false}' | jq .
```

**Step 2 — tag rows that have no origin yet:**
```sh
curl -s -X POST .../api/requests/park-hygiene \
  -d '{"action":"tag","origin":"e2e","apply":true,"ids":[...ids...]}' | jq .
```

**Step 3 — apply reap:**
```sh
curl -s -X POST .../api/requests/park-hygiene \
  -d '{"action":"reap","origin":"e2e","apply":true}' | jq .
```

Verify: the next census log line should show `test_origin=0`.

---

## Code pointers

| File | Contents |
|---|---|
| `internal/usenet/transport.go` | `ErrTransport`, `isTransportError`, `classifySegmentFailure`, `transportRetryDelay` |
| `internal/usenet/manager.go` | `fetchSegmentAny` retry loop (`maxSegmentAttemptsPerServer = 3`) |
| `internal/grabs/retry.go` | `TransportBackoff`, `MaxTransportRetries`, `RetryBackoff` |
| `internal/grabs/grabs.go` | `ParkForTransportResume`, `DueForResume`, `Relaunch`, `SetOrigin` |
| `internal/api/usenettransport.go` | `parkUsenetTransportFailure`, `resumeDueTransportRetries`, `usenetResumeEngine` |
| `internal/api/usenetretry.go` | `applyUsenetFailure`, `runUsenetRetryCycle` |
| `internal/api/parkcensus.go` | `computeParkCensus`, `parkCensusHandler`, `logParkCensus` |
| `internal/api/parkhygiene.go` | `runParkHygiene`, `parkHygieneHandler`, `strandedResumeGrace` |
| `CHANGELOG.md` | 2026-09-17 entry |

---

## Out of scope

**C — Slot-full torrent escalation**: when all Usenet slots are full during
a transport resume and the grab cannot wait, escalate to torrent. This requires
changes to `resumeDueTransportRetries` and the `next_search_scope` column, and
was explicitly deferred.

**D — UI wire-up**: surfacing transport park status on the Requests screen.
