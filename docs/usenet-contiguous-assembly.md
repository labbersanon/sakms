# Usenet yEnc-begin assembly, PAR2 unpack fallback, obfuscated RAR sets

Shipped 2026-09-18 (branch `cursor/usenet-yenc-stride-assemble-c2d5`).
Supersedes the 2026-09-15 contiguous-v2 layout on `cursor/usenet-assembly-par2-obfuscation-c2d5`.

## Problem

sakms failed Usenet downloads NZBGet completed on the same Eweka account. Two
assembly layouts were tried:

1. **v1 yEnc-begin (original)** — `assembleFile` wrote at yEnc `begin` offsets
   (often a 768000 stride) while decoded lengths were shorter, leaving
   14–40 byte NUL windows. That layout matches NZBGet DirectWrite *and* PAR2
   FileDesc size, but was misread as RAR damage and replaced.
2. **v2 packed / contiguous (2026-09-15, wrong for stride posters)** —
   writes used a packing cursor (`WriteAt(data, cursor); cursor += len(data)`)
   and truncated to the **decoded-byte sum**. That removed the NUL windows but
   left RAR5 ~1.5KB short of the PAR2 FileDesc (`stride×N`). go-newsgroups/par2
   then marked thousands of slices missing; unrar still often worked, which is
   why PAR2-fail → unpack (#62) was added as a delivery gate.

Other independent bugs that remain fixed (not reverted by v3):

3. **Obfuscated dual RAR sets / colliding yEnc names** — pretty-named orphan
   `….part01.rar` plus a complete hash-named set, and one yEnc filename reused
   across every RAR/PAR2 part. Naming uniquify is #59/#60 (`uniqueOutputName`
   / `usedNames`); `archiveLeaders` ranking is unchanged.
4. **PAR2 soft-complete** — verify/repair failure used to mark the download
   complete. Unchanged: PAR2 failure is a **warning**, then unpack (or a flat
   video) is the delivery gate (#62).

## Fixes (v3)

| Area | Change |
|------|--------|
| Assembly | Restore DirectWrite-style **WriteAt(yEnc Offset)** per article; **Truncate to =ybegin FileSize** (`max(yencSize, maxEnd)`). Drop the packing cursor. Ordered pipeline (concurrent fetch, serial commit) is unchanged. |
| On-disk layout | NUL windows in the stride gaps are **expected**. Never truncate to the packed decoded sum. |
| Resume | Schema **v3**. loadResumeTracker / InvalidateLegacyResumes wipe **v1 and v2** (any non-current sidecar) plus staging payloads. |
| Naming | #59/#60 uniquify colliding yEnc names (`.partNNN`) — separate from layout. |
| PAR2 / unpack | #62 remains: PAR2 fail-closed is a warning; unpack (or flat video) is the delivery gate. Rank complete RAR sets first; try every leader on failure. |

## Not in this PR

Connection count / per-article retries (Phase D) — left at current 8 until
verified live. Operator will raise `max_conns` afterward.

Do not merge to main or deploy from this branch without an operator decision.

## Code pointers

- `internal/usenet/manager.go` — `assembleFile` yEnc-begin write loop, `InvalidateLegacyResumes`
- `internal/usenet/resume.go` — `resumeSchemaVersion` (v3), `skippedOffset`, legacy wipe on load
- `internal/usenet/unpack.go` — `archiveLeaders` ranking, multi-leader try
- `internal/usenet/manager.go` `finalizeAssembled` — PAR2 warning then unpack (#62)
- `cmd/sakms/main.go` — boot call to `InvalidateLegacyResumes`
