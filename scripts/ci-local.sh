#!/usr/bin/env bash
# Claude 2026-10-21: local mirror of .github/workflows/go-test.yml
# Reason: merge/deploy is gated on wade-pc, not GitHub CI. This must be the
#   same Postgres + frontend embed + go test ./... run GitHub uses.
# Troubleshooting: podman sakms-pg on 5433; glfw headers via Fedora -devel;
#   "pattern static" means pnpm build did not populate internal/web/static.
#   SQLSTATE 53100 / "resize shared memory segment" → HostConfig.ShmSize
#   still 64MiB (recreate). SQLSTATE 53300 too many clients → max_connections.
# Review if: go-test.yml postgres image, pnpm version, or go test flags change.
# Related files: .github/workflows/go-test.yml, scripts/sakms-ship-local.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

PG_NAME="${SAKMS_TEST_PG_NAME:-sakms-pg}"
PG_PORT="${SAKMS_TEST_PG_PORT:-5433}"
PG_IMAGE="${SAKMS_TEST_PG_IMAGE:-docker.io/library/postgres:16-alpine}"
PG_SHM_SIZE="${SAKMS_TEST_PG_SHM_SIZE:-1g}"
PG_MAX_CONN="${SAKMS_TEST_PG_MAX_CONNECTIONS:-400}"
# Anything still on Docker's 64MiB /dev/shm default must be recreated.
PG_MIN_SHM_BYTES=$((256 * 1024 * 1024))

postgres_ready() {
  podman exec "$PG_NAME" pg_isready -U sakms >/dev/null 2>&1
}

# Claude 2026-10-21: recreate when shm is still the 64MiB Docker default.
# Reason: max_connections=400 plus parallel CREATE DATABASE TEMPLATE clones
#   allocate 32MiB POSIX DSM segments in /dev/shm; ENOSPC is SQLSTATE 53100
#   then crash recovery (the 2026-10-21 ci-local miss).
# Troubleshooting: inspect HostConfig.ShmSize; recovery-mode logs mean rm -f
#   and let this recreate. Do not raise shared_buffers to "fix" 64MiB shm.
# Review if: GitHub Actions postgres service sets an explicit shm-size.
# Related files: internal/dbtest/dbtest.go
# Old create (64MiB shm, image default max_connections):
#   podman run -d --name "$PG_NAME" \
#     -e POSTGRES_USER=sakms -e POSTGRES_PASSWORD=sakms -e POSTGRES_DB=sakms \
#     -p "${PG_PORT}:5432" "$PG_IMAGE"
postgres_needs_recreate() {
  if ! podman inspect "$PG_NAME" >/dev/null 2>&1; then
    return 0
  fi
  local shm cmd
  shm=$(podman inspect -f '{{.HostConfig.ShmSize}}' "$PG_NAME")
  if [[ "${shm:-0}" -lt "$PG_MIN_SHM_BYTES" ]]; then
    return 0
  fi
  cmd=$(podman inspect -f '{{json .Config.Cmd}}' "$PG_NAME")
  if [[ "$cmd" != *"max_connections=${PG_MAX_CONN}"* ]]; then
    return 0
  fi
  return 1
}

create_postgres() {
  echo "creating $PG_NAME on host port $PG_PORT (shm=$PG_SHM_SIZE max_connections=$PG_MAX_CONN)"
  podman run -d --name "$PG_NAME" --shm-size="$PG_SHM_SIZE" \
    -e POSTGRES_USER=sakms \
    -e POSTGRES_PASSWORD=sakms \
    -e POSTGRES_DB=sakms \
    -p "${PG_PORT}:5432" \
    "$PG_IMAGE" \
    postgres -c "max_connections=${PG_MAX_CONN}" >/dev/null
}

wait_postgres() {
  for _ in $(seq 1 "$1"); do
    if postgres_ready; then
      return 0
    fi
    sleep 1
  done
  return 1
}

ensure_postgres() {
  if postgres_needs_recreate; then
    podman rm -f "$PG_NAME" >/dev/null 2>&1 || true
    create_postgres
  elif ! podman ps --format '{{.Names}}' | grep -qx "$PG_NAME"; then
    echo "starting existing $PG_NAME"
    podman start "$PG_NAME" >/dev/null
  fi
  if wait_postgres 60; then
    return
  fi
  echo "postgres $PG_NAME did not become ready — recreating" >&2
  podman rm -f "$PG_NAME" >/dev/null 2>&1 || true
  create_postgres
  if wait_postgres 60; then
    return
  fi
  echo "postgres $PG_NAME did not become ready" >&2
  exit 1
}

ensure_postgres

export SAKMS_TEST_DATABASE_URL="postgres://sakms:sakms@127.0.0.1:${PG_PORT}/sakms?sslmode=disable"
export SAKMS_TEST_DATABASE_URL_REQUIRED=1
export CI=true

echo "postgres ready at $SAKMS_TEST_DATABASE_URL"

# Claude 2026-10-21: put distro Node 22 ahead of Cursor's Node 24 on PATH.
# Reason: engines/.nvmrc/Dockerfile pin Node 22. Cursor prepends v24.5.0;
#   pnpm then warns Unsupported engine. /usr/bin/node is already node-22
#   (Fedora nodejs22). Do not widen engines to 24 — CI and the image stay 22.
# Troubleshooting: "Unsupported engine ... current node 24" means this PATH
#   prefix was skipped. Missing /usr/bin/node-22 → dnf install nodejs22.
# Review if: Dockerfile node image major is no longer 22.
# Related files: frontend/package.json engines, frontend/.nvmrc
if [[ -x /usr/bin/node-22 ]]; then
  export PATH="/usr/bin:${PATH}"
fi
echo "node $(command -v node) $(node -v)"

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
