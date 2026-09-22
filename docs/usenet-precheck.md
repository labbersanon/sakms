# Usenet pre-download article check

**Status:** always on (no Settings toggle).  
<!-- Claude 2026-09-22: sample/escalate retired — full payload STAT instead.
Reason: assembleFile fail-closes on any hole; a 48-segment sample could miss a single 430/451.
**Escalation scope:** A — only files that missed in the sample (+ small re-sample). -->
**Check:** STAT **every** payload article (skip `.par2` / `.nfo` / images when identifiable; honor resume skip map).  
**Candidate retries:** up to 3 qualified runners-up per `RunAutoGrab` / batch cycle.

## Why

Dead NZBs were consuming the single concurrent Usenet slot for ~10 minutes before failing mid-download (430 / unrepairable PAR2). SABnzbd-style “check before download” rejects those NZBs via NNTP `STAT` before staging. A sample of 48 segments was not enough: one missing payload article still fails assembly.

## Behaviour

1. After NZB parse, **before** staging allocation (`AddNZB`) or relaunch work (`RelaunchNZB`).
2. Acquire the **precheck job semaphore** (capacity = `MaxConcurrentDownloads`, independent of the download semaphore). Overlapping prechecks wait here; they do not steal BODY download slots.
3. STAT **all** remaining payload segments (skip meta files; skip MsgIDs already in the resume sidecar).
4. Timeout scales with segment count: `15s + 100ms×N`, capped at **12 minutes** (sequential budget; worker fan-out only finishes earlier).
5. STAT workers = `concurrencyBudget()` (sum of per-server `MaxConns`). Pool sockets are still shared with in-flight BODY fetches; download jobs already hold live tokens. Tradeoff: under `MaxConns=1` plus an active download, STAT waits on `pool.getCtx`.
6. Abort (`ErrArticlesUnavailable`) if **any** confirmed missing (430) or removed (451) payload article.
7. **BODY trust probe:** if STAT says 430 but BODY works, mark STAT unreliable for the process and proceed (skip the gate for the rest of the process lifetime).
8. Zero NNTP pools → no-op (keeps unit fixtures green).

<!-- Claude 2026-09-22: previous hybrid sample path (do not restore without revisiting assembleFile holes).
2. Sample up to **48** payload segments (skip `.par2` / `.nfo` / images when identifiable).
3. If sample missing rate ≥ **25%** → abort (`ErrArticlesUnavailable`).
4. If any miss but < 25% → escalate STAT on **affected files** only (option A).
5. Abort if any confirmed missing/removed payload article (engine cannot skip holes).
6. BODY trust probe / zero pools as today.
-->

## Caller outcomes

| Path | On `ErrArticlesUnavailable` |
|------|-----------------------------|
| `RunAutoGrab` / batch | Try next qualified candidate (max 3). Exhaustion → `NoMatch` / fallback list; gated triggers park `pending_retry`. |
| Search & pick / single enclosure | HTTP **409** — pick another release. |
| `RelaunchNZB` (reconcile) | Park grab with `articlesUnavailableReason`. |

## Ops notes

- Each attempt fetches another NZB from the indexer (grab quota). Cap of 3 bounds that.
- Precheck does **not** take a download slot. It waits on its own semaphore of the same capacity, then competes for NNTP connections (`concurrencyBudget()`, all `MaxConns`).
- Contiguous assembly still fail-closes on any missing segment during download; precheck only fails earlier.

## Code

- `internal/usenet/precheck.go` — full-payload STAT gate + precheck semaphore
- `internal/usenet/manager.go` — `New` / `SetMaxConcurrentDownloads` seed both semaphores
- `internal/usenet/pool.go` — `getCtx`
- `internal/api/autograb_shared.go` — candidate loop
- `internal/api/autograb_batch.go` — same
- `internal/api/search.go` — 409 mapping
- `internal/api/downloadreconcile.go` — park on relaunch miss
