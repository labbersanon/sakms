#!/bin/bash
# Claude 2026-08-04: sakms init — fetch Postgres password from BW SM, write db_password + db_url
# Reason: password never in compose/git; sakms-auto-update calls compose up directly so
#   init-container (not a stack.py unit) must re-run on every up.
# Troubleshooting: if init fails, check /etc/bws/machine-token and bws binary mount;
#   retry/backoff below covers boot-time BWS API races (protocol_server1_bwsm_stack_restart.md).
# Review if: BW SM UUID changes (see bitwarden_sm.md sakms/postgres-password).
# Related files: compose.yml on server1 /opt/docker/media/automation/sakms/
# BW SM UUID: sakms/postgres-password = 549214e6-db88-4dc0-aed1-b49c0151b428

set -euo pipefail

export BWS_ACCESS_TOKEN
BWS_ACCESS_TOKEN=$(cat /etc/bws/machine-token)

UUID="549214e6-db88-4dc0-aed1-b49c0151b428"
DB_PASS=""
for attempt in 1 2 3; do
  if DB_PASS=$(bws secret get "$UUID" | python3 -c "import sys,json; print(json.load(sys.stdin)['value'])"); then
    break
  fi
  echo "bws secret get failed (attempt $attempt); retrying..."
  sleep $((attempt * 2))
done
if [ -z "$DB_PASS" ]; then
  echo "failed to fetch sakms/postgres-password after retries" >&2
  exit 1
fi

# Claude 2026-08-04: chmod 644 (not 600) — sakms runs as PUID 1001 via gosu; root-owned
#   600 files under /run/secrets cause "permission denied" on SAKMS_DATABASE_URL_FILE.
# Reason: volume is only mounted into sakms-init / sakms-db / sakms; world-readable is OK.
# Troubleshooting: if sakms loops on "permission denied" for db_url, check modes here.
# Review if: sakms runs as root or secrets move to Docker secrets with uid mapping.
umask 022
printf '%s' "$DB_PASS" > /config/db_password
chmod 644 /config/db_password

# Full DSN for sakms (SAKMS_DATABASE_URL_FILE). sslmode=disable — internal compose network.
# Password is hex-only so it is URL-safe without encoding.
printf 'postgres://sakms:%s@sakms-db:5432/sakms?sslmode=disable' "$DB_PASS" > /config/db_url
chmod 644 /config/db_url

# Sanity: DSN must parse (catches accidental non-URL-safe passwords).
python3 -c "
from urllib.parse import urlparse
u = urlparse(open('/config/db_url').read().strip())
assert u.scheme == 'postgres' and u.hostname == 'sakms-db' and u.path == '/sakms', (u.scheme, u.hostname, u.path)
print('db_password and db_url written — init complete')
"
