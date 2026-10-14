# NOTICE — third-party components, licensing, and legal posture

This file records how SAK (this project — AGPL-3.0, see `LICENSE`) uses
bundled and invoked third-party software, and the honest legal reasoning
behind those choices. It is deliberately written **broadly** so a future
effort with a different risk profile (e.g. a native transcoding/TV-app
player — see `docs/ROADMAP.md`) can extend it rather than start a fresh
analysis. Read the "Scope and honesty caveats" section before treating any
statement here as a compliance guarantee: it is a good-faith rationale
resting on standard industry practice, not legal advice and not an explicit
endorsement from any upstream project.

SAK itself is licensed AGPL-3.0. Nothing below relicenses SAK; these are
external programs SAK either ships in its container image or invokes as a
subprocess.

---

## FFmpeg

**What SAK uses it for.** FFmpeg (`ffmpeg`/`ffprobe`) is invoked purely as a
command-line subprocess (never linked):

- Movies/Series perceptual-hash frame extraction and duration probing
  (`internal/phash`) — decode-only.
- Adult/StashDB perceptual-hash extraction (`internal/videophash`) — decode-only.
- VMAF perceptual-quality scoring for Dedup (`internal/vmaf`, shelling out
  to `ffmpeg -lavfi libvmaf` — see the "Packaging status" note at the end of
  this section for which FFmpeg build supplies this filter and why) — decode-only.
- In-app browser Play (`internal/playback`) — Direct Play needs no ffmpeg;
  HLS remux is stream-copy; Direct Stream encodes AAC; Transcode encodes
  H.264 (+ AAC). See "In-app HLS playback" below.

**Invocation pattern: subprocess, never linking.** SAK builds with
`CGO_ENABLED=0` (the SQLite driver is pure-Go `modernc.org/sqlite`), so the
Go binary links **no** FFmpeg library — not `libavcodec`, not
`libavfilter`, nothing. Every FFmpeg use is `os/exec` spawning the
distro-provided `ffmpeg`/`ffprobe` binary and parsing its stdout/stderr.
This is the same subprocess-only pattern SAK has always used for phash, now
extended to VMAF. It is also the pattern every mainstream self-hosted media
tool uses — **Jellyfin** and **FileFlows** both bundle/invoke FFmpeg as a
program, not as a linked library the application is a derivative work of.

**License mode of the bundled binary.** SAK's Docker image bundles a pinned,
checksum-verified prebuilt FFmpeg from the **BtbN/FFmpeg-Builds** project
(`github.com/BtbN/FFmpeg-Builds`), not Debian's distro `ffmpeg` package. See
the "Packaging status" section below for *why* the distro package could not
be used (it lacks the `libvmaf` filter) — this is a deliberate, user-approved
exception to the original "no static build" non-goal. The exact bundled build
(verified at build time — version `n7.1.5-9-gb9a218bc1e`, pinned in the
Dockerfile by release tag + SHA256) is configured with:

- `--enable-gpl` and `--enable-version3`, which makes the **resulting FFmpeg
  binary GPL (GPLv3)** — not merely LGPL. FFmpeg's own code is LGPL-2.1+ by
  default and becomes GPL only when GPL components are enabled; this build
  enables them (same posture as Debian's own build, which is GPLv2).
- `--enable-libvmaf` — the whole reason for using this build (the distro
  package omits it; see below).
- It is **not** built with `--enable-nonfree` (confirmed absent from the
  build's configuration). The `--enable-nonfree` flag is what produces a
  binary that is legally *unredistributable*; this build does not use it, so
  it is **freely redistributable under GPLv3 terms**. (BtbN's GPL builds are
  the same artifact FileFlows itself ships to its users for VMAF, and are a
  widely-used community-standard prebuilt ffmpeg.)

SAK does not modify, fork, statically link into its own binary, or relink
FFmpeg — it installs an unmodified prebuilt binary and runs it as a separate
program via subprocess. The GPL of that binary governs that binary; it does
not reach back into SAK's own AGPL-3.0 code, because there is no linking —
only process invocation. (This is the crux of the honesty caveat below: this
"no reach-back via subprocess" reasoning is standard industry practice, not
something FFmpeg's own compliance page explicitly blesses.)

## libvmaf (Netflix VMAF)

VMAF (Video Multi-method Assessment Fusion) is Netflix's perceptual video
quality metric. The library that FFmpeg's `libvmaf` filter wraps is licensed
**BSD+Patent** (SPDX `BSD-2-Clause-Patent`) — Netflix relicensed it from
Apache-2.0 to BSD+Patent on **2020-02-27**. BSD+Patent is a permissive
license with an **explicit patent grant**, which is materially reassuring for
a quality-*measurement* use: it is neither copyleft nor royalty-bearing, and
the patent grant is explicit rather than merely implied.

## Why VMAF is low-risk: decode-only, not encode

The single most important legal distinction for this feature: **VMAF only
decodes; it never encodes.** Computing a VMAF score reads two already-encoded
video files, decodes frames from each, and compares them
(`ffmpeg -i candidate -i reference -lavfi libvmaf -f null -`). No new
encoded video is produced. There is no H.264/HEVC/AV1 *encoding* step.

This matters because the codec patent-royalty regimes FFmpeg's own legal page
warns commercial distributors about (MPEG-LA / Via LA / Access Advance pools
for H.264, HEVC, etc.) are about **encoding and, in some framings,
distributing encoded content** — the commercial-encoder territory. A
decode-only measurement pass does not enter that territory. Combined with the
facts that SAK is **free, AGPL-3.0, non-commercial**, and that libvmaf itself
carries an explicit patent grant, the VMAF feature sits well clear of the
codec-royalty exposure a real transcoding feature would face.

**This decode-only argument does NOT extend to encoding.** In-app HLS
Direct Stream / Transcode (below) and a future native TV-app player (see
`docs/ROADMAP.md`) perform real re-encoding — a genuinely different and
higher risk profile that this section's decode-only rationale explicitly
does not cover.

## In-app HLS playback (browser)

SAK's Library / Series / Organize Play path matches Jellyfin's web player:

1. **Direct Play** — mp4/m4v/webm/mov via `http.ServeContent` (no ffmpeg).
2. **Remux** — codecs already browser-safe (H.264/AV1/VP8/VP9 + AAC/MP3/Opus);
   ffmpeg `-c copy` into fragmented-MP4 HLS.
3. **Direct Stream** — video copy-safe, audio is not (AC3/DTS/TrueHD/…);
   ffmpeg copies video and encodes stereo AAC.
4. **Transcode** — video is not MSE-safe in Chrome/Firefox (HEVC, MPEG-2,
   MPEG-4 ASP, VC-1, …); ffmpeg encodes H.264 (`h264_nvenc` when the
   container's ffmpeg reports that encoder, else `libx264`) plus AAC.

This is **personal, non-commercial, AGPL-3.0 homelab playback of files the
operator already owns**, the same subprocess-ffmpeg pattern Jellyfin uses
for its web client. It is not a public streaming service, not a commercial
encoder product, and not a native TV-app distribution channel.

**Native TV / mobile apps** (Android TV, tvOS, Roku, Fire TV) remain a
separate, still-gated ROADMAP item. This section covers the in-app browser
player only. The codec-royalty pools FFmpeg warns commercial encoders about
are not analyzed here as a legal opinion — this records the intended use
and the operator's explicit "Match jellyfin" go-ahead (2026-10-14) after
the four-mode explanation.

## Scope and honesty caveats

- **FFmpeg's own compliance guidance only covers linking.**
  `ffmpeg.org/legal.html` provides a compliance checklist framed entirely
  around **linking** FFmpeg's libraries into an application (LGPL vs. GPL
  obligations, `--enable-nonfree` unredistributability, patent warnings for
  commercial encoders). It is **silent on the subprocess/CLI-invocation
  pattern** SAK actually relies on. There is therefore no explicit upstream
  FFmpeg statement endorsing the "invoke the binary as a separate program, so
  its GPL doesn't reach my differently-licensed app" position. The safety of
  SAK's pattern rests on **standard, widely-practiced industry convention**
  (Jellyfin, FileFlows, and effectively every self-hosted media tool invoke
  FFmpeg this way), **not** on an FFmpeg endorsement of it. This is stated
  plainly rather than dressed up as a settled guarantee.

- **This document is a good-faith rationale, not legal advice.** It records
  the reasoning behind SAK's packaging and invocation choices so they aren't
  silently reversed or misremembered. It is not a warranty of compliance.

- **Packaging status of the VMAF filter (RESOLVED — with a deliberate,
  documented exception to a spec non-goal).** The VMAF backend spec's
  original packaging plan was to add Debian's `libavfilter-extra` package to
  supply ffmpeg's `libvmaf` filter, while keeping a hard non-goal of "no
  custom/static ffmpeg build." An **actual build test (2026-07-23)** proved
  that plan cannot work: Debian trixie's `ffmpeg` — base AND the
  `libavfilter-extra` flavor — is built **without** `--enable-libvmaf`, and
  no `libvmaf` package exists in trixie's repos at all (only the base
  `vmafmotion` filter, which is *not* the `libvmaf` filter `internal/vmaf`
  invokes). jellyfin-ffmpeg was tested next and **also** lacks the filter.
  The only apt/distro-adjacent route that ships a real, usable `libvmaf` is a
  **prebuilt static build** — which is exactly what FFmpeg's own filter
  compilation model forces (filters are compiled in at build time; no runtime
  package can add one).
  - **Decision (user-approved, made with full awareness of the tradeoff):**
    bundle a **pinned, checksum-verified BtbN/FFmpeg-Builds static build**
    (`github.com/BtbN/FFmpeg-Builds`) instead of the distro package. This is
    a **deliberate exception to the spec's "no static build" non-goal**, not
    an oversight — it is stated plainly here rather than obscured. The
    exception is narrow and justified: the non-goal's intent was "don't
    compile/maintain a custom ffmpeg *ourselves* from source," and BtbN is a
    third-party, community-standard, auditable (public GitHub Actions)
    prebuilt binary — the same artifact FileFlows itself installs for its own
    VMAF support. It is pinned to a specific dated release tag with a SHA256
    checksum verified at build time (the Docker build **fails** on a checksum
    mismatch rather than silently proceeding), never tracking a `latest`/
    rolling tag.
  - **Verified working:** the `libvmaf` filter is present and usable
    (`ffmpeg -h filter=libvmaf` → "Calculate the VMAF between two video
    streams") in the **actual final built image**, not merely an intermediate
    stage — confirmed by building the image and running the check inside it.
  - The **licensing and invocation analysis above stands** for this build:
    it is GPLv3, not `--enable-nonfree`, freely redistributable, and SAK
    still invokes it purely as a subprocess (no linking) — so the
    subprocess-not-linking model and the decode-only rationale are unaffected
    by the switch from the distro package to the BtbN build.
