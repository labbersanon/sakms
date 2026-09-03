# syntax=docker/dockerfile:1.7

# Frontend build stage: compiles the SolidJS + Vite app to static assets.
# This whole stage is discarded — no Node.js reaches the final image. Its
# only output is /src/internal/web/static, COPY'd into the Go build below
# so //go:embed static picks it up. Node/pnpm versions are pinned (must match
# frontend/package.json's engines + packageManager); install uses the
# committed lockfile with --frozen-lockfile so a drift fails the build here.
FROM node:22-bookworm-slim AS frontend
WORKDIR /src/frontend
RUN corepack enable && corepack prepare pnpm@9.15.9 --activate
COPY frontend/package.json frontend/pnpm-lock.yaml ./
RUN --mount=type=cache,target=/root/.local/share/pnpm/store \
    pnpm install --frozen-lockfile
# frontend/tsconfig.json's "@dto" path alias resolves to
# ../internal/apidto/ts/dto.gen.ts relative to this stage's WORKDIR
# (/src/frontend) — i.e. /src/internal/apidto/ts inside this stage. This
# stage's build context is otherwise scoped to frontend/ alone, so that
# directory must be copied in explicitly or every @dto import fails here
# despite working fine in a normal (non-Docker) checkout.
COPY internal/apidto/ts /src/internal/apidto/ts
COPY frontend/ ./
# Writes to /src/internal/web/static (outDir is ../internal/web/static
# relative to this frontend/ workdir), mirroring the local-dev layout.
RUN pnpm build

FROM golang:1.26-trixie AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
# Overlay the compiled frontend into the Go embed dir before building. The
# static/ dir is entirely generated (gitignored/dockerignored — the Stage 5
# cutover removed the old tracked static/index.html), so the embed content
# comes only from here; without this COPY, //go:embed static fails cleanly.
COPY --from=frontend /src/internal/web/static ./internal/web/static
# CGO_ENABLED=1: required for internal/sysinfo's real NVML GPU-utilization
# path (go-nvml's type definitions live in cgo bridge files — see
# gpu_nvml.go/gpu_nvml_nocgo.go's !cgo-gated stub). This does NOT reintroduce
# a cgo sqlite driver — modernc.org/sqlite stays pure Go regardless of this
# flag, cgo is only exercised by go-nvml's dlopen-at-runtime shim, which needs
# no NVIDIA headers/libs at build time (only a C compiler, already present in
# this base image). server1's docker-compose.yml has carried `runtime:
# nvidia` + NVIDIA_VISIBLE_DEVICES since 2026-07-17 specifically for this;
# CGO_ENABLED=0 silently made that passthrough a no-op the whole time — GPU
# utilization never actually read via NVML in any deployed build until now.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 go build -trimpath -o /out/sakms ./cmd/sakms

# ffmpeg stage: fetch a pinned, checksum-verified BtbN static FFmpeg build that
# INCLUDES the libvmaf filter. Debian trixie's own ffmpeg package — base AND the
# `libavfilter-extra` flavor — is built WITHOUT `--enable-libvmaf`, and trixie
# ships no `libvmaf` package at all (both empirically confirmed 2026-07-23 by an
# actual build test), so internal/vmaf's `ffmpeg -lavfi libvmaf` call cannot work
# on the distro package. jellyfin-ffmpeg was also tested and likewise lacks the
# libvmaf filter. BtbN/FFmpeg-Builds is the community-standard prebuilt ffmpeg
# (auditable public GitHub Actions build) — and is exactly what FileFlows' own
# "FFmpeg FileFlows Edition" installs for its VMAF support. Using a third-party
# prebuilt binary here is a DELIBERATE, user-approved exception to the
# vmaf-backend spec's original "no custom/static ffmpeg build" non-goal, made
# with full awareness of the tradeoff — see NOTICE.md for the rationale and
# GPLv3/no-nonfree license posture. Pinned to a dated release tag + SHA256 (never
# `latest`) for reproducibility; the build FAILS on a checksum mismatch rather
# than silently proceeding. Bump: pick a newer dated release from
# github.com/BtbN/FFmpeg-Builds/releases and update all three ARGs from that
# release's checksums.sha256.
# Review if: internal/vmaf drops the libvmaf dependency, or trixie's ffmpeg gains
# --enable-libvmaf (then the distro package could replace this whole stage).
# Claude 2026-08-27: retarget pin off autobuild-2026-07-23-14-16 / n7.1.5.
# Reason: BtbN deletes older dated autobuild assets; that URL 404'd and
#   blocked sakms-auto-update (rollback rebuilds the previous SHA against
#   the same dead pin, so it failed too). n7.1 is no longer published;
#   n8.1 is the current GPL static linux64 line. Still a dated tag +
#   SHA256, never `latest`.
# Troubleshooting: compose build wget 404 on BtbN/FFmpeg-Builds — pick a
#   tag that still exists on the releases page and copy the asset name +
#   checksums.sha256 line.
# Review if: BtbN drops n8.1 the same way, or trixie ffmpeg gains libvmaf.
FROM debian:trixie-slim AS ffmpeg
ARG FFMPEG_TAG=autobuild-2026-08-26-13-06
ARG FFMPEG_ASSET=ffmpeg-n8.1.2-46-g139afe709a-linux64-gpl-8.1.tar.xz
ARG FFMPEG_SHA256=0814f4491c2673ea505be8fb65a76c2bfabaa5aad8f33d49b1c5b87a2262e8c5
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/var/lib/apt,sharing=locked \
    apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates wget xz-utils
RUN set -eux; \
    wget --no-verbose -O /tmp/ffmpeg.tar.xz \
      "https://github.com/BtbN/FFmpeg-Builds/releases/download/${FFMPEG_TAG}/${FFMPEG_ASSET}"; \
    echo "${FFMPEG_SHA256}  /tmp/ffmpeg.tar.xz" | sha256sum -c -; \
    mkdir -p /tmp/ffmpeg /out; \
    tar -xf /tmp/ffmpeg.tar.xz -C /tmp/ffmpeg --strip-components=1; \
    install -m0755 /tmp/ffmpeg/bin/ffmpeg  /out/ffmpeg; \
    install -m0755 /tmp/ffmpeg/bin/ffprobe /out/ffprobe; \
    /out/ffmpeg -hide_banner -h filter=libvmaf | grep -q "Calculate the VMAF"

# Debian, not Alpine, for the runtime base: the BtbN ffmpeg build links glibc
# (not musl), and the Go binary itself now links glibc too (CGO_ENABLED=1, for
# go-nvml's cgo bridge — see the build stage above; modernc.org/sqlite stays
# pure Go regardless), so glibc is doubly the right call here. ffmpeg/ffprobe
# come from the pinned BtbN stage above (NOT the
# distro package, which lacks libvmaf) and are placed on PATH at /usr/local/bin,
# which is where sakms resolves the bare `ffmpeg`/`ffprobe` names it exec's
# (internal/phash, internal/videophash, internal/vmaf).
FROM debian:trixie-slim AS base
# Claude 2026-09-03: unrar (non-free) + p7zip-full for Usenet post-PAR2 unpack.
# Reason: releases are usually multi-part RAR; import needs a flat video in the
#   GID staging dir. unrar is in non-free; enable contrib/non-free on the
#   DEB822 debian.sources shipped by trixie-slim.
# Troubleshooting: staging full of .partNN.rar after download completes.
# Review if: a pure-Go unpacker replaces the binaries.
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/var/lib/apt,sharing=locked \
    if [ -f /etc/apt/sources.list.d/debian.sources ]; then \
      sed -i 's/Components: main$/Components: main contrib non-free non-free-firmware/' /etc/apt/sources.list.d/debian.sources; \
    fi \
    && apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates gosu unrar p7zip-full \
    && useradd --create-home --home-dir /data --uid 1000 sakms

COPY --from=ffmpeg /out/ffmpeg  /usr/local/bin/ffmpeg
COPY --from=ffmpeg /out/ffprobe /usr/local/bin/ffprobe

COPY --from=build /out/sakms /usr/local/bin/sakms

ENV SAKMS_ADDR=:8080 \
    SAKMS_DATA_DIR=/data

VOLUME /data
EXPOSE 8080

# Bundled Ollama ai stage removed 2026-07-16: replaced by DB-first filename
# parsing (internal/parseentity) which needs no local LLM. BYOAI (external
# OpenAI/Gemini/Anthropic/Ollama) remains available via Settings → Connections.
# The sakms-ollama-models volume on server1 can be manually pruned after the
# next deploy confirms the new parsing pipeline works.

# Default image: lean, no AI backend bundled.
FROM base AS runtime
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

# Stays root here so the entrypoint can chown a bind-mounted /data before
# dropping to the unprivileged sakms user via gosu, and can re-map that
# user to a caller-supplied PUID/PGID (both default 1000, matching this
# baked-in uid/gid) before doing so — see docker-entrypoint.sh for why.
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["/usr/local/bin/sakms"]
