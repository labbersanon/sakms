# Usenet Alternate-Release Retry (C)

Shipped 2026-09-17 on branch `cursor/usenet-transport-abe-c2d5`.

## Goal

Before this change, a PAR2-unrepairable archive, an unpack failure, or a
hollow import (no video file found after downloading) caused the request to
re-park on the normal days-ladder — which retries the **same NZB** after a
multi-day backoff. A different, working copy of the same release is often
available on Usenet. This change makes the scheduler try a different Usenet
release immediately instead.

---

## Triggers

Any of the following causes the scheduler to park the grab **due-now** with
the failed release excluded:

| Trigger | Error sentinel |
|---|---|
| PAR2 unrepairable | `usenet.ErrContentUnusable` (wrapped in `manager.go`) |
| Archive unpack failed | `usenet.ErrContentUnusable` (wrapped in `manager.go`) |
| No video file found after import | `library.ErrNoVideoFile` (wrapped in `library.go`/`library_series.go`) |

Not classified as content failures:

- `usenet.ErrTransport` — transport errors outrank content errors (§7.1): a
  dropped socket is a network blip, not a bad release. Transport takes the
  resume/backoff path, not the alternate-release path.
- `usenet.ErrUnpackToolMissing` — environment fault (no `unrar`/`7z` binary).
  A different release cannot fix a missing unpacker, so this falls straight
  to the days ladder.

---

## Exclusion Keys

Each failed release is fingerprinted with two short, one-way hashes (SHA-256
truncated to 8 bytes / 16 hex chars):

- `u:<hash>` — hash of the download URL (catches the exact NZB by provenance).
- `t:<hash>` — case/whitespace-normalised hash of the release title (catches
  a re-indexed copy of the same file from a different indexer).

Keys are stored in the `grabs.tried_release_keys` column (newline-separated)
and never contain plaintext credentials or URLs. When `RunAutoGrab` is
re-triggered for the grab, `ExcludeReleaseKeys` is populated from this column
and passed to `scoreOnePhase → filterExcludedReleases`.

---

## Cap retired (2026-09-22)

<!-- Claude 2026-09-22: MaxAlternateReleaseAttempts=3 removed.
Reason: precheck exhausts the full graded list each cycle; tried_release_keys
exclude dead NZBs. Download-time "3 then days ladder" duplicated that role.
Review if: a hard park-episode cap returns for pathological indexer churn. -->

Former behaviour: after three distinct `u:` parks, fall through to the days
ladder (which cleared `tried_release_keys`). Now content/430 failures always
requeue via `ParkForAlternateRelease` (due-now) until search+precheck find a
live NZB or `RunAutoGrab` returns NoMatch → `parkPendingRetry`.

`Relaunch` (dispatching the new release) deliberately **preserves**
`tried_release_keys` so two bad NZBs cannot alternate forever.

---

## No Torrent Escalation

Alternate-release retries stay Usenet-only. While `tried_release_keys` is
non-empty, `nextSearchPhases` returns `[ScopeUsenet]` rather than the default
`ScopeAll`. This prevents the alternate-retry path from silently implementing
torrent escalation.

---

## Due-now Pickup

The daily retry cycle (`retryDueGrabs`) handles due-now rows. A dedicated
drain pass (`drainAlternateReleaseRetries` in `autograbdrain.go`) picks up
alternate-release rows on the drain's 60-second tick — the daily cycle alone
would leave them waiting up to 24 hours.

---

## Hollow Import Bug (fixed)

`UsenetCompleteImporter` and `reconcileImportUsenet` previously logged
"no video file" and returned without parking the grab, leaving it stuck
in `queued`/`downloading` forever. Both now call `parkUsenetContentFailure`
on `library.ErrNoVideoFile`, passing the live `*usenet.Manager` so `Forget`
can release the in-memory slot immediately.

---

## Code Map

| File | Change |
|---|---|
| `internal/usenet/content.go` | New: `ErrContentUnusable`, `ErrUnpackToolMissing` sentinels |
| `internal/library/library.go` | `ErrNoVideoFile` sentinel; wrap `ResolveVideoFile` no-video return |
| `internal/library/library_series.go` | Wrap `ResolveEpisodeVideoFiles` no-video return with `ErrNoVideoFile` |
| `internal/usenet/manager.go` | Wrap PAR2 and unpack errors with `ErrContentUnusable` |
| `internal/usenet/unpack.go` | Return `ErrUnpackToolMissing` for no-unpacker and `ReadDir` failure |
| `internal/db/migrations/0025_grabs_tried_release_keys.sql` | Add `tried_release_keys TEXT NOT NULL DEFAULT ''` |
| `internal/grabs/alternate.go` | New: `MaxAlternateReleaseAttempts`, `ReleaseKeys`, `AlternateAttempts`, `ParkForAlternateRelease` |
| `internal/grabs/grabs.go` | `TriedReleaseKeys` field; all 9 SELECTs + `scanGrab`; clear in days-ladder parks |
| `internal/api/usenetcontent.go` | New: `contentUnusableFailure`, `parkUsenetContentFailure`, `contentForgetEngine` |
| `internal/api/usenetretry.go` | `applyUsenetFailure` content branch; `nextSearchPhases` Usenet-only guard; `retryDueGrabs` exclusion keys |
| `internal/api/autograb_shared.go` | `AutoGrabRequest.ExcludeReleaseKeys`; `filterExcludedReleases`; `scoreOnePhase` filter call |
| `internal/api/autograbdrain.go` | `drainAlternateReleaseRetries` pass |
| `internal/api/import.go` | `UsenetCompleteImporter` hollow-import park |
| `internal/api/downloadreconcile.go` | `reconcileImportUsenet` hollow-import park |
