# syntax=docker/dockerfile:1.7

FROM golang:1.25-trixie AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -o /out/sakms ./cmd/sakms

# Debian, not Alpine: ffmpeg's Debian package is the more predictable ffprobe
# build, and CGO is off anyway (modernc.org/sqlite is pure Go), so there's no
# musl-vs-glibc tradeoff to weigh here.
FROM debian:trixie-slim
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/var/lib/apt,sharing=locked \
    apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates ffmpeg gosu \
    && useradd --create-home --home-dir /data --uid 1000 sakms

COPY --from=build /out/sakms /usr/local/bin/sakms
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

ENV SAKMS_ADDR=:8080 \
    SAKMS_DATA_DIR=/data

VOLUME /data
EXPOSE 8080
# Stays root here so the entrypoint can chown a bind-mounted /data before
# dropping to the unprivileged sakms user via gosu, and can re-map that
# user to a caller-supplied PUID/PGID (both default 1000, matching this
# baked-in uid/gid) before doing so — see docker-entrypoint.sh for why.
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["/usr/local/bin/sakms"]
