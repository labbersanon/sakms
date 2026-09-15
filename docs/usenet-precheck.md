# Usenet pre-download article check

**Status:** always on (no Settings toggle).  
**Escalation scope:** A — only files that missed in the sample (+ small re-sample).  
**Candidate retries:** up to 3 qualified runners-up per `RunAutoGrab` / batch cycle.

## Why

Dead NZBs were consuming the single concurrent Usenet slot for ~10 minutes before failing mid-download (430 / unrepairable PAR2). SABnzbd-style “check before download” rejects those NZBs in seconds via NNTP `STAT`.

## Behaviour

1. After NZB parse, **before** staging allocation (`AddNZB`) or relaunch work (`RelaunchNZB`).
2. Sample up to **48** payload segments (skip `.par2` / `.nfo` / images when identifiable).
3. If sample missing rate ≥ **25%** → abort (`ErrArticlesUnavailable`).
4. If any miss but &lt; 25% → escalate STAT on **affected files** only (option A).
5. Abort if any confirmed missing/removed payload article (engine cannot skip holes).
6. **BODY trust probe:** if STAT says 430 but BODY works, mark STAT unreliable for the process and proceed.
7. Zero NNTP pools → no-op (keeps unit fixtures green).

## Caller outcomes

| Path | On `ErrArticlesUnavailable` |
|------|-----------------------------|
| `RunAutoGrab` / batch | Try next qualified candidate (max 3). Exhaustion → `NoMatch` / fallback list; gated triggers park `pending_retry`. |
| Search & pick / single enclosure | HTTP **409** — pick another release. |
| `RelaunchNZB` (reconcile) | Park grab with `articlesUnavailableReason`. |

## Ops notes

- Each attempt fetches another NZB from the indexer (grab quota). Cap of 3 bounds that.
- Precheck does **not** take a download slot; it does compete briefly for NNTP connections (`budget-1`, floor 1).
- Contiguous assembly still fail-closes on any missing segment during download; precheck only fails earlier.

## Code

- `internal/usenet/precheck.go` — gate + sampling
- `internal/usenet/pool.go` — `getCtx`
- `internal/api/autograb_shared.go` — candidate loop
- `internal/api/autograb_batch.go` — same
- `internal/api/search.go` — 409 mapping
- `internal/api/downloadreconcile.go` — park on relaunch miss
