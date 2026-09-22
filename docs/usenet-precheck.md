# Usenet pre-download article check

**Status:** always on (no Settings toggle).  
<!-- Claude 2026-09-22: sample/escalate retired — full payload STAT instead.
Reason: assembleFile fail-closes on any hole; a 48-segment sample could miss a single 430/451.
**Escalation scope:** A — only files that missed in the sample (+ small re-sample). -->
**Check:** STAT **every** payload article (skip `.par2` / `.nfo` / images when identifiable; honor resume skip map).  
<!-- Claude 2026-09-22: candidate retries exhaust the full graded list (was max 3).
Reason: precheck owns source selection; download-time alternate caps retired. -->
**Candidate retries:** every qualified runner-up in the graded list per `RunAutoGrab` / batch cycle, then park / NoMatch.

## Why

Dead NZBs were consuming the single concurrent Usenet slot for ~10 minutes before failing mid-download (430 / unrepairable PAR2). SABnzbd-style “check before download” rejects those NZBs via NNTP `STAT` before BODY. A sample of 48 segments was not enough: one missing payload article still fails assembly.

Precheck is also the **source-selection** gate: when one NZB fails STAT (or later fails mid-download / unpack), the grab is requeued with that release fingerprinted out and the next search walks remaining candidates through precheck again. Download-time “try N alternates” caps are gone.

## Behaviour

1. After NZB parse, register the Downloads row as `phase=precheck`, then STAT.
2. Acquire the **precheck job semaphore** (capacity = `MaxConcurrentDownloads`, independent of the download semaphore). Overlapping prechecks wait here; they do not steal BODY download slots.
3. STAT **all** remaining payload segments (skip meta files; skip MsgIDs already in the resume sidecar).
4. Timeout scales with segment count: `15s + 100ms×N`, capped at **12 minutes** (sequential budget; worker fan-out only finishes earlier).
5. STAT workers = `concurrencyBudget()` (sum of per-server `MaxConns`). Pool sockets are still shared with in-flight BODY fetches; download jobs already hold live tokens. Tradeoff: under `MaxConns=1` plus an active download, STAT waits on `pool.getCtx`.
6. Abort (`ErrArticlesUnavailable`) if **any** confirmed missing (430) or removed (451) payload article — row cleaned (`dropPrecheckFailed`), no Failed pill.
7. On STAT ok: if a BODY slot is free → `downloading`; if `MaxConcurrentDownloads` is full → `waiting`, then `downloading` when a slot opens.
8. **BODY trust probe:** if STAT says 430 but BODY works, mark STAT unreliable for the process and proceed (skip the gate for the rest of the process lifetime).
9. Zero NNTP pools → no-op (keeps unit fixtures green).

<!-- Claude 2026-09-22: previous hybrid sample path (do not restore without revisiting assembleFile holes).
2. Sample up to **48** payload segments (skip `.par2` / `.nfo` / images when identifiable).
3. If sample missing rate ≥ **25%** → abort (`ErrArticlesUnavailable`).
4. If any miss but < 25% → escalate STAT on **affected files** only (option A).
5. Abort if any confirmed missing/removed payload article (engine cannot skip holes).
6. BODY trust probe / zero pools as today.
-->

## Caller outcomes

| Path | On `ErrArticlesUnavailable` / content fail |
|------|-----------------------------|
| `RunAutoGrab` / batch | Try **every** qualified candidate. Exhaustion → `NoMatch` / fallback list; gated triggers park `pending_retry`. |
| Search & pick / single enclosure | Create grab + `ParkForAlternateRelease` (due-now, URL/title fingerprinted) so drain re-searches through precheck. |
| Mid-download 430 / unpack / no-video | Same alternate park (requeue); `drainAlternateReleaseRetries` → `RunAutoGrab` → precheck. Transport resume still short-parks the **same** NZB. |
| `RelaunchNZB` (reconcile) | Park grab with `articlesUnavailableReason`. |

## Ops notes

- Each attempt may fetch another NZB from the indexer (grab quota). Exhausting the graded list bounds that per cycle; `tried_release_keys` prevent re-picking known-dead NZBs.
- Precheck does **not** take a download slot. It waits on its own semaphore of the same capacity, then competes for NNTP connections (`concurrencyBudget()`, all `MaxConns`).
- Contiguous assembly still fail-closes on any missing segment during download; precheck only fails earlier. Surviving mid-download holes requeue through precheck rather than a separate download-fallback policy.

## Code

- `internal/usenet/precheck.go` — full-payload STAT gate + precheck semaphore
- `internal/usenet/manager.go` — `New` / `SetMaxConcurrentDownloads` seed both semaphores; Waiting phase
- `internal/usenet/pool.go` — `getCtx`
- `internal/api/autograb_shared.go` — candidate loop (full qualified order)
- `internal/api/autograb_batch.go` — same
- `internal/api/search.go` — manual grab requeue on precheck miss
- `internal/api/usenetcontent.go` — download-fail → alternate park (precheck re-entry)
- `internal/api/downloadreconcile.go` — park on relaunch miss
