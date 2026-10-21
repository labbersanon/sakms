#!/usr/bin/env bash
# Claude 2026-10-21: ntfy helper for sakms nightly/deploy failures on wade-pc
# Reason: wade-pc has no bws; publisher token is systemd-creds (not the
#   personal ntfy-token.cred, which cannot publish to status-5808).
# Troubleshooting: decrypt fails → cred missing; 403 → wrong token.
# Review if: ntfy topic for systemd failures leaves status-5808.
# Related files: scripts/sakms-nightly-sync.sh, ntfy.md
set -euo pipefail

TITLE="${1:?title}"
BODY="${2:?body}"
CRED="${NTFY_PUBLISHER_CRED:-$HOME/.config/systemd/user/credentials/ntfy-publisher.cred}"
TOPIC="${NTFY_TOPIC:-status-5808}"

if [[ ! -f "$CRED" ]]; then
  echo "missing ntfy publisher cred: $CRED" >&2
  exit 1
fi

TOKEN="$(systemd-creds decrypt --name=ntfy-publisher --user "$CRED" -)"
curl -fsS \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "Title: ${TITLE}" \
  -H "Priority: high" \
  -H "Tags: warning,sakms" \
  -d "${BODY}" \
  "https://ntfy.zaena.us/${TOPIC}" >/dev/null
echo "ntfy posted to ${TOPIC}"
