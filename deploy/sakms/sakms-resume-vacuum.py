#!/usr/bin/env python3
# Claude 2026-09-21: periodic stale usenet_resume_state cleanup + VACUUM
# Reason: per-segment DB resume mirrors create multi-GB TOAST bloat; autovacuum
#   alone cannot keep an 8G LUN healthy. Sidecar on /staging is source of truth.
# Troubleshooting: sakms-db disk full / healthz 503 / SQLSTATE 57P03 recovery loop
# Review if: SaveResume is throttled or the DB mirror is removed in sakms code
# Related: usenet_resume_state; internal/downloadstate/store.go; sakms-db LUN

"""Delete stale usenet_resume_state rows then VACUUM the table.

Stale = updated_at older than STALE_HOURS (RFC3339Nano text, lexicographic cut).
Active downloads keep updating updated_at, so they are not deleted here — a
code-side throttle/removal of per-segment SaveResume is still required to stop
mid-download refill.
"""

from __future__ import annotations

import subprocess
import sys
from datetime import datetime, timedelta, timezone

STALE_HOURS = 24
CONTAINER = "sakms-db"
DB_USER = "sakms"
DB_NAME = "sakms"


def psql(sql: str) -> str:
    r = subprocess.run(
        [
            "docker",
            "exec",
            "-i",
            CONTAINER,
            "psql",
            "-U",
            DB_USER,
            "-d",
            DB_NAME,
            "-v",
            "ON_ERROR_STOP=1",
            "-t",
            "-A",
            "-c",
            sql,
        ],
        check=True,
        capture_output=True,
        text=True,
    )
    return r.stdout.strip()


def main() -> int:
    # Ensure DB is accepting connections before touching data.
    ready = subprocess.run(
        ["docker", "exec", CONTAINER, "pg_isready", "-U", DB_USER, "-d", DB_NAME],
        capture_output=True,
        text=True,
    )
    if ready.returncode != 0:
        print(f"sakms-resume-vacuum: pg_isready failed: {ready.stdout}{ready.stderr}", file=sys.stderr)
        return 1

    cutoff = (datetime.now(timezone.utc) - timedelta(hours=STALE_HOURS)).strftime(
        "%Y-%m-%dT%H:%M:%S"
    )
    before = psql(
        "SELECT count(*), coalesce(pg_total_relation_size('usenet_resume_state'),0) "
        "FROM usenet_resume_state"
    )
    deleted = psql(
        "WITH d AS ("
        f"DELETE FROM usenet_resume_state WHERE updated_at < '{cutoff}' RETURNING 1"
        ") SELECT count(*) FROM d"
    )
    # VACUUM cannot run inside a transaction; separate invocation.
    subprocess.run(
        [
            "docker",
            "exec",
            "-i",
            CONTAINER,
            "psql",
            "-U",
            DB_USER,
            "-d",
            DB_NAME,
            "-v",
            "ON_ERROR_STOP=1",
            "-c",
            "VACUUM usenet_resume_state;",
        ],
        check=True,
    )
    after = psql(
        "SELECT count(*), coalesce(pg_total_relation_size('usenet_resume_state'),0) "
        "FROM usenet_resume_state"
    )
    print(
        f"sakms-resume-vacuum: cutoff={cutoff}Z before={before} "
        f"deleted_stale={deleted} after={after}"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
