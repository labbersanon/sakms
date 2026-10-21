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

# Claude 2026-10-21: npx pnpm@9.15.9 instead of corepack.
# Reason: wade-pc PATH's node is Cursor's (no corepack). packageManager is
#   pnpm@9.15.9; npx does not need `corepack enable`.
# Troubleshooting: "corepack: command not found" means this fallback was
#   reverted. "ERR_PNPM_OUTDATED_LOCKFILE" means the lockfile needs 9.x.
# Review if: PATH has a system node with corepack and we want to use that.
export NPM_CONFIG_YES=true
(
  cd frontend
  npx --yes pnpm@9.15.9 install --frozen-lockfile
  npx --yes pnpm@9.15.9 build
)

# Claude 2026-10-21: generate glfw Wayland protocol headers (Fedora).
# Reason: go-gl/glfw #includes *-client-protocol.h that `go test` does not
#   generate. GitHub Ubuntu has them via apt; Fedora RPM %build runs this
#   same wayland-scanner loop (packaging/rpm/sakms-node.spec). Without it,
#   ./cmd/sakms-node-config fails: xdg-shell-client-protocol.h missing.
# Troubleshooting: wayland-scanner is in wayland-devel. XML lives under
#   vendor/.../glfw/deps/wayland. Headers are untracked under glfw/src.
# Review if: go-gl/glfw vendors the generated headers or CI drops ./...
GLFW_SRC="$ROOT/vendor/github.com/go-gl/glfw/v3.4/glfw/glfw"
if [[ ! -f "$GLFW_SRC/src/xdg-shell-client-protocol.h" ]]; then
  echo "generating glfw Wayland protocol headers"
  for proto in wayland xdg-shell xdg-decoration-unstable-v1 viewporter \
               relative-pointer-unstable-v1 pointer-constraints-unstable-v1 \
               fractional-scale-v1 xdg-activation-v1 idle-inhibit-unstable-v1; do
    wayland-scanner client-header "${GLFW_SRC}/deps/wayland/${proto}.xml" \
      "${GLFW_SRC}/src/${proto}-client-protocol.h"
    wayland-scanner private-code "${GLFW_SRC}/deps/wayland/${proto}.xml" \
      "${GLFW_SRC}/src/${proto}-client-protocol-code.h"
  done
fi

echo "go test ./... (CI flags)"
go test -count=1 -timeout 45m -p 4 ./...
echo "ci-local ok"
