#!/usr/bin/env bash
# Claude 2026-10-21: push local main to origin/main. Never deploys.
# Reason: live is already on this SHA from sakms-deploy-local. Starting
#   sakms-auto-update after this push would be a redundant recreate (and
#   used to roll back if GitHub lagged). ntfy on push or GitHub CI failure.
# Troubleshooting: non-ff → ntfy and exit 1. gh run watch needs a working
#   gh token. --dry-run prints the plan.
# Review if: origin/main is no longer fast-forwarded from wade-pc main.
# Related files: scripts/sakms-ntfy.sh, ~/.config/systemd/user/sakms-nightly-sync.timer
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

DRY=0
if [[ "${1:-}" == "--dry-run" ]]; then
  DRY=1
fi

NOTIFIED=0
notify() {
  local title="$1" body="$2"
  NOTIFIED=1
  if [[ "$DRY" -eq 1 ]]; then
    echo "DRY ntfy: $title — $body"
    return
  fi
  ./scripts/sakms-ntfy.sh "$title" "$body" || echo "ntfy itself failed" >&2
}

on_exit() {
  local rc=$?
  if [[ "$rc" -ne 0 && "$NOTIFIED" -eq 0 ]]; then
    notify "sakms nightly failed" "exit ${rc} — journalctl --user -u sakms-nightly-sync.service"
  fi
}
trap on_exit EXIT

GIT_SSH_COMMAND="${GIT_SSH_COMMAND:-ssh -o BatchMode=yes}"
export GIT_SSH_COMMAND

git fetch origin main
LOCAL="$(git rev-parse main)"
REMOTE="$(git rev-parse origin/main)"

if [[ "$LOCAL" == "$REMOTE" ]]; then
  echo "origin/main already $LOCAL — nothing to push"
  exit 0
fi

if git merge-base --is-ancestor "$LOCAL" "$REMOTE"; then
  msg="local main ${LOCAL:0:12} is behind origin/main ${REMOTE:0:12} — not pushing, not deploying"
  echo "$msg" >&2
  notify "sakms nightly: local main behind GitHub" "$msg"
  exit 1
fi

if ! git merge-base --is-ancestor "$REMOTE" "$LOCAL"; then
  msg="local main ${LOCAL:0:12} and origin/main ${REMOTE:0:12} have diverged — not force-pushing"
  echo "$msg" >&2
  notify "sakms nightly: main diverged" "$msg"
  exit 1
fi

echo "fast-forward origin/main ${REMOTE:0:12} -> ${LOCAL:0:12}"
if [[ "$DRY" -eq 1 ]]; then
  echo "DRY: git push origin main:main"
  echo "DRY: would watch GitHub go-test for $LOCAL"
  exit 0
fi

if ! git push origin main:main; then
  notify "sakms nightly: git push failed" "git push origin main:main failed for ${LOCAL}"
  exit 1
fi

echo "pushed $LOCAL — not starting sakms-auto-update"

# GitHub CI is report-only. Failure does not redeploy or revert live.
if ! command -v gh >/dev/null; then
  notify "sakms nightly: gh missing" "pushed ${LOCAL} but cannot watch go-test (gh not on PATH)"
  exit 1
fi

echo "waiting for go-test on $LOCAL"
# Newly queued runs can take a few seconds to appear.
for _ in $(seq 1 24); do
  RUN_ID="$(gh run list --repo labbersanon/sakms --branch main --commit "$LOCAL" --workflow go-test.yml --limit 1 --json databaseId --jq '.[0].databaseId // empty')"
  if [[ -n "$RUN_ID" ]]; then
    break
  fi
  sleep 5
done

if [[ -z "${RUN_ID:-}" ]]; then
  notify "sakms nightly: no GitHub go-test" "pushed ${LOCAL} but no go-test run showed up"
  exit 1
fi

if ! gh run watch "$RUN_ID" --repo labbersanon/sakms --exit-status; then
  URL="$(gh run view "$RUN_ID" --repo labbersanon/sakms --json url --jq .url)"
  notify "sakms nightly: GitHub go-test failed" "commit ${LOCAL} run ${URL} — live was not redeployed"
  exit 1
fi

echo "go-test passed for $LOCAL"
