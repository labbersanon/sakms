#!/usr/bin/env bash
# Claude 2026-10-21: test → merge to local main → deploy. No GitHub push.
# Reason: GitHub CI is overnight verification. Live follows local main.
# Troubleshooting: merge refuses on dirty main or divergence with origin
#   (origin may lag; that is expected). Nightly pushes origin/main.
# Review if: nightly timer is removed or live goes back to GitHub-gated deploys.
# Related files: scripts/ci-local.sh, scripts/sakms-deploy-local.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

SKIP_TEST=0
SKIP_DEPLOY=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --skip-test) SKIP_TEST=1 ;;
    --skip-deploy) SKIP_DEPLOY=1 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
  shift
done

if [[ "$SKIP_TEST" -eq 0 ]]; then
  ./scripts/ci-local.sh
fi

git fetch origin main
BRANCH="$(git rev-parse --abbrev-ref HEAD)"
if [[ "$BRANCH" != "main" ]]; then
  echo "merge $BRANCH -> local main"
  git checkout main
  git merge --ff-only "$BRANCH"
fi

if [[ "$SKIP_DEPLOY" -eq 1 ]]; then
  echo "skip deploy; local main is $(git rev-parse --short HEAD)"
  exit 0
fi

./scripts/sakms-deploy-local.sh main
echo "shipped $(git rev-parse HEAD) to server1. origin/main is not updated (nightly)."
