#!/usr/bin/env bash
# Claude 2026-10-21: local mirror of .github/workflows/go-test.yml
# Reason: merge/deploy is gated on wade-pc, not GitHub CI. This must be the
#   same Postgres + frontend embed + go test ./... run GitHub uses.
# Troubleshooting: podman sakms-pg on 5433; glfw headers via Fedora -devel;
#   "pattern static" means pnpm build did not populate internal/web/static.
# Review if: go-test.yml postgres image, pnpm version, or go test flags change.
# Related files: .github/workflows/go-test.yml, scripts/sakms-ship-local.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

PG_NAME="${SAKMS_TEST_PG_NAME:-sakms-pg}"
PG_PORT="${SAKMS_TEST_PG_PORT:-5433}"
PG_IMAGE="${SAKMS_TEST_PG_IMAGE:-docker.io/library/postgres:16-alpine}"

ensure_postgres() {
  if podman ps --format '{{.Names}}' | grep -qx "$PG_NAME"; then
    podman exec "$PG_NAME" pg_isready -U sakms >/dev/null
    return
  fi
  if podman ps -a --format '{{.Names}}' | grep -qx "$PG_NAME"; then
    echo "starting existing $PG_NAME"
    podman start "$PG_NAME" >/dev/null
  else
    echo "creating $PG_NAME on host port $PG_PORT"
    podman run -d --name "$PG_NAME" \
      -e POSTGRES_USER=sakms \
      -e POSTGRES_PASSWORD=sakms \
      -e POSTGRES_DB=sakms \
      -p "${PG_PORT}:5432" \
      "$PG_IMAGE" >/dev/null
  fi
  for _ in $(seq 1 30); do
    if podman exec "$PG_NAME" pg_isready -U sakms >/dev/null 2>&1; then
      return
    fi
    sleep 1
  done
  echo "postgres $PG_NAME did not become ready" >&2
  exit 1
}

ensure_postgres

export SAKMS_TEST_DATABASE_URL="postgres://sakms:sakms@127.0.0.1:${PG_PORT}/sakms?sslmode=disable"
export SAKMS_TEST_DATABASE_URL_REQUIRED=1
export CI=true

echo "postgres ready at $SAKMS_TEST_DATABASE_URL"

export COREPACK_ENABLE_DOWNLOAD_PROMPT=0
(
  cd frontend
  corepack pnpm install --frozen-lockfile
  corepack pnpm build
)

echo "go test ./... (CI flags)"
go test -count=1 -timeout 45m -p 4 ./...
echo "ci-local ok"
