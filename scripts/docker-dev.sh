#!/usr/bin/env bash
# Build, (re)start, and health-check SAK's Docker image in one command —
# for the rapid build/rebuild loop while iterating on the Dockerfile itself,
# not a real deployment (docker-compose.yml, once it exists, is for that).
#
# Default (no args) does a full build+restart+health-check, so a single
# invocation is the whole iteration loop: fix Dockerfile, run this, read the
# pass/fail line and (on failure) the log tail it already printed.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

IMAGE_TAG="${IMAGE_TAG:-sak:dev}"
CONTAINER_NAME="${CONTAINER_NAME:-sak-dev}"
HOST_PORT="${HOST_PORT:-8080}"
DATA_DIR="${DATA_DIR:-$(pwd)/.dockerdata}"
HEALTH_URL="http://localhost:${HOST_PORT}/healthz"
HEALTH_TIMEOUT="${HEALTH_TIMEOUT:-30}"

export DOCKER_BUILDKIT=1

usage() {
  cat <<EOF
Usage: $(basename "$0") [command]

Commands:
  build     Build the image only.
  up        Start (or restart) the container from the current image, no rebuild.
  restart   Build + start. This is the default when no command is given.
  stop      Stop and remove the container.
  logs      Follow the container's logs.
  shell     Drop into a shell in the built image (server not started).
  clean     Stop the container and wipe ${DATA_DIR} for a fresh-install test.
  status    Show whether the container is running and its health check result.

Env overrides: IMAGE_TAG, CONTAINER_NAME, HOST_PORT, DATA_DIR, HEALTH_TIMEOUT
EOF
}

build() {
  echo "==> building ${IMAGE_TAG}"
  docker build -t "${IMAGE_TAG}" .
}

stop() {
  if docker ps -a --format '{{.Names}}' | grep -qx "${CONTAINER_NAME}"; then
    echo "==> stopping/removing ${CONTAINER_NAME}"
    docker rm -f "${CONTAINER_NAME}" >/dev/null
  fi
}

wait_healthy() {
  echo -n "==> waiting for ${HEALTH_URL}"
  local waited=0
  while (( waited < HEALTH_TIMEOUT )); do
    if curl -sf -o /dev/null "${HEALTH_URL}"; then
      echo " — up"
      return 0
    fi
    echo -n "."
    sleep 1
    waited=$((waited + 1))
  done
  echo " — FAILED after ${HEALTH_TIMEOUT}s"
  echo "==> last 50 log lines:"
  docker logs --tail 50 "${CONTAINER_NAME}" || true
  return 1
}

up() {
  stop
  mkdir -p "${DATA_DIR}"
  echo "==> starting ${CONTAINER_NAME} on port ${HOST_PORT}, data dir ${DATA_DIR}"
  docker run -d \
    --name "${CONTAINER_NAME}" \
    -p "${HOST_PORT}:8080" \
    -v "${DATA_DIR}:/data" \
    "${IMAGE_TAG}" >/dev/null
  wait_healthy
}

logs() { docker logs -f "${CONTAINER_NAME}"; }

shell() { docker run --rm -it --entrypoint /bin/bash "${IMAGE_TAG}"; }

clean() {
  stop
  echo "==> wiping ${DATA_DIR}"
  rm -rf "${DATA_DIR}"
}

status() {
  if docker ps --format '{{.Names}}' | grep -qx "${CONTAINER_NAME}"; then
    echo "${CONTAINER_NAME} is running"
    if curl -sf -o /dev/null "${HEALTH_URL}"; then echo "health: ok"; else echo "health: FAILING"; fi
  else
    echo "${CONTAINER_NAME} is not running"
  fi
}

cmd="${1:-restart}"
case "$cmd" in
  build) build ;;
  up) up ;;
  restart) build; up ;;
  stop) stop ;;
  logs) logs ;;
  shell) shell ;;
  clean) clean ;;
  status) status ;;
  -h|--help) usage ;;
  *) echo "unknown command: $cmd" >&2; usage; exit 1 ;;
esac
