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

FROM golang:1.25-trixie AS build
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
# Fetch the static aria2c binary so //go:embed assets/aria2c resolves.
# Docker equivalent of `make aria2c`; network is available during build.
RUN --mount=type=cache,target=/go/pkg/mod \
    go run ./cmd/download-aria2c
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
