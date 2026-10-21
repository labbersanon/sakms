#!/usr/bin/env bash
# Claude 2026-10-21: deploy local HEAD to server1 without touching GitHub
# Reason: live must move before origin/main. Auto-update builds from the
#   server1 bare repo; same SHA as state.json is a no-op (no container
#   recreate). Do not start this after a nightly origin push.
# Troubleshooting: git push to root@10.1.10.3:/var/lib/sakms-updater/sakms.git
#   then journalctl -u sakms-auto-update.service.
# Review if: updater ExecStart is no longer --deploy-local.
# Related files: deploy/sakms/sakms-auto-update.py, protocol_sakms_local_merge.md
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

HOST="${SAKMS_DEPLOY_HOST:-root@10.1.10.3}"
BARE="${SAKMS_DEPLOY_BARE:-/var/lib/sakms-updater/sakms.git}"
SHA="$(git rev-parse HEAD)"
REF="${1:-HEAD}"

echo "push ${REF} (${SHA}) -> ${HOST}:${BARE}"
GIT_SSH_COMMAND="${GIT_SSH_COMMAND:-ssh -o BatchMode=yes}"
export GIT_SSH_COMMAND
git push --quiet "${HOST}:${BARE}" "${REF}:refs/heads/main"

echo "start sakms-auto-update --deploy-local"
ssh "$HOST" "systemctl start sakms-auto-update.service"

echo "---- journal ----"
ssh "$HOST" "journalctl -u sakms-auto-update.service --since '-10 min' --no-pager"

echo "---- state ----"
ssh "$HOST" "cat /var/lib/sakms-updater/state.json; docker ps --filter name=^sakms\$ --format '{{.Names}} {{.Status}}'"
