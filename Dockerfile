# syntax=docker/dockerfile:1.7

# Frontend build stage: compiles the SolidJS + Vite app to static assets.
# This whole stage is discarded — no Node.js reaches the final image. Its
# only output is /src/internal/web/static/app, COPY'd into the Go build below
# so //go:embed static picks it up. Node/pnpm versions are pinned (must match
# frontend/package.json's engines + packageManager); install uses the
# committed lockfile with --frozen-lockfile so a drift fails the build here.
FROM node:22-bookworm-slim AS frontend
WORKDIR /src/frontend
RUN corepack enable && corepack prepare pnpm@9.15.9 --activate
COPY frontend/package.json frontend/pnpm-lock.yaml ./
RUN --mount=type=cache,target=/root/.local/share/pnpm/store \
    pnpm install --frozen-lockfile
COPY frontend/ ./
# Writes to /src/internal/web/static/app (outDir is ../internal/web/static/app
# relative to this frontend/ workdir), mirroring the local-dev layout.
RUN pnpm build

FROM golang:1.25-trixie AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
# Overlay the compiled frontend into the Go embed dir before building. The
# COPY . . above brings static/index.html (tracked); this adds the generated
# static/app/ bundle (gitignored/dockerignored, so it comes only from here).
COPY --from=frontend /src/internal/web/static/app ./internal/web/static/app
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -o /out/sakms ./cmd/sakms

# Debian, not Alpine: ffmpeg's Debian package is the more predictable ffprobe
# build, and CGO is off anyway (modernc.org/sqlite is pure Go), so there's no
# musl-vs-glibc tradeoff to weigh here.
FROM debian:trixie-slim AS base
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/var/lib/apt,sharing=locked \
    apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates ffmpeg gosu \
    && useradd --create-home --home-dir /data --uid 1000 sakms

COPY --from=build /out/sakms /usr/local/bin/sakms

ENV SAKMS_ADDR=:8080 \
    SAKMS_DATA_DIR=/data

VOLUME /data
EXPOSE 8080

# Opt-in variant: bundles Ollama as a second in-container process, giving
# AI-assisted features (Adult kids-classify, Movies/Series garbled-title-
# guess fallback — see internal/identify, internal/classify) a working
# backend with zero external setup. NOT the default image — build with
# `docker build --target ai .` explicitly; plain `docker build .` still
# produces the lean "runtime" stage below, unchanged. Ollama installs as a
# prebuilt binary here, a separate OS process from sakms; nothing in this
# stage touches sakms's own CGO_ENABLED=0 Go build.
FROM base AS ai
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/var/lib/apt,sharing=locked \
    apt-get update \
    && apt-get install -y --no-install-recommends curl zstd \
    && curl -fsSL https://ollama.com/install.sh | sh \
    && apt-get purge -y curl zstd && apt-get autoremove -y

COPY docker-entrypoint-ai.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

# OLLAMA_MODELS is deliberately its own volume, not under SAKMS_DATA_DIR —
# see docker-entrypoint-ai.sh for why (server1's auto-updater wipes /data on
# every deploy; a model cached there would re-download every push).
ENV SAKMS_BUNDLED_OLLAMA_MODEL=qwen2.5:1.5b \
    OLLAMA_MODELS=/ollama-models

VOLUME /ollama-models
# Requires an init process (`docker run --init` / compose's `init: true`) —
# see docker-entrypoint-ai.sh's header comment for why.
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["/usr/local/bin/sakms"]

# Default image: lean, no AI backend bundled — unchanged from before the ai
# stage above existed. Stays the LAST stage in this file so plain
# `docker build .` (no --target) keeps building this, not ai.
FROM base AS runtime
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

# Stays root here so the entrypoint can chown a bind-mounted /data before
# dropping to the unprivileged sakms user via gosu, and can re-map that
# user to a caller-supplied PUID/PGID (both default 1000, matching this
# baked-in uid/gid) before doing so — see docker-entrypoint.sh for why.
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["/usr/local/bin/sakms"]
