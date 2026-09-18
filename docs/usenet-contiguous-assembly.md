# Usenet contiguous assembly, PAR2 fail-closed, obfuscated RAR sets

Shipped 2026-09-15 (branch `cursor/usenet-assembly-par2-obfuscation-c2d5`).

## Problem

sakms failed Usenet downloads NZBGet completed on the same Eweka account:

1. **yEnc write gaps** — `assembleFile` wrote at yEnc `begin` offsets
   (often a 768000 stride) while decoded lengths were shorter, leaving
   14–40 byte NUL holes. PAR2 reported thousands of damaged slices; unrar
   failed with checksum / corrupt header; import saw no video.
2. **PAR2 soft-complete** — `verifyAndRepair` failure still marked the
   download complete, so broken RARs looked “done.”
3. **Obfuscated dual RAR sets** — pretty-named orphan `….part01.rar` plus a
   complete hash-named `AbCd….part01–N.rar`. `archiveLeaders` tried the
   orphan first and aborted on the first unrar error.

## Fixes

| Area | Change |
|------|--------|
| Assembly | Contiguous write by cumulative **decoded** length; truncate to packed size |
| Resume | Schema **v2**; v1 sidecars discarded + staging payloads wiped |
| Upgrade | `InvalidateLegacyResumes()` at boot before reconcile (auto-wipe) |
| PAR2 | Verify/repair still runs; failure is a **warning** then unpack |
| Unpack | Rank complete RAR sets first; try every leader on failure; unpack (or flat video) is the delivery gate |

## Not in this PR (deferred)

Connection count / per-article retries (Phase D) — left at current 8 until
A–C are verified live. Operator will raise `max_conns` afterward.

## Code pointers

- `internal/usenet/manager.go` — `assembleFile`, PAR2 fail-closed, `InvalidateLegacyResumes`
- `internal/usenet/resume.go` — `resumeSchemaVersion`, legacy wipe on load
- `internal/usenet/unpack.go` — `archiveLeaders` ranking, multi-leader try
- `cmd/sakms/main.go` — boot call to `InvalidateLegacyResumes`
