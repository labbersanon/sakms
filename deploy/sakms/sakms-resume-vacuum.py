#!/usr/bin/env python3
# Claude 2026-09-21: retired — DB resume mirror dropped (sidecar-only).
# Reason: usenet_resume_state is DROP'd by migration 0028; running this would
#   fail VACUUM on a missing table. Sidecar .sakms-resume.json is SoT.
# Troubleshooting: keep this file for history; timer/service are disabled.
# Review if: script can be deleted after live hosts have disabled the timer.
# Related: 0028_drop_usenet_resume_state.sql; sakms-resume-vacuum.timer
#
# Original 2026-09-21 periodic stale usenet_resume_state cleanup + VACUUM is
# retired. Do not re-enable DELETE/VACUUM — the relation is gone.

"""Retired 2026-09-21: usenet_resume_state dropped; sidecar-only resume.

Previously deleted stale usenet_resume_state rows then VACUUM'd the table.
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
    # Retired 2026-09-21: unused after early-return main(); kept for history.
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
    # Claude 2026-09-21: no-op — table dropped; do not DELETE/VACUUM.
    print(
        "sakms-resume-vacuum: retired 2026-09-21 — "
        "usenet_resume_state dropped; sidecar-only resume"
    )
    return 0
    # Retired body (unreachable). Kept so the original SQL is visible if this
    # file is ever re-enabled by mistake — it must not run against a gone table.
    # ready = subprocess.run(
    #     ["docker", "exec", CONTAINER, "pg_isready", "-U", DB_USER, "-d", DB_NAME],
    #     capture_output=True,
    #     text=True,
    # )
    # if ready.returncode != 0:
    #     print(f"sakms-resume-vacuum: pg_isready failed: {ready.stdout}{ready.stderr}", file=sys.stderr)
    #     return 1
    # cutoff = (datetime.now(timezone.utc) - timedelta(hours=STALE_HOURS)).strftime(
    #     "%Y-%m-%dT%H:%M:%S"
    # )
    # before = psql(
    #     "SELECT count(*), coalesce(pg_total_relation_size('usenet_resume_state'),0) "
    #     "FROM usenet_resume_state"
    # )
    # deleted = psql(
    #     "WITH d AS ("
    #     f"DELETE FROM usenet_resume_state WHERE updated_at < '{cutoff}' RETURNING 1"
    #     ") SELECT count(*) FROM d"
    # )
    # subprocess.run(
    #     [
    #         "docker", "exec", "-i", CONTAINER, "psql", "-U", DB_USER, "-d", DB_NAME,
    #         "-v", "ON_ERROR_STOP=1", "-c", "VACUUM usenet_resume_state;",
    #     ],
    #     check=True,
    # )
    # after = psql(
    #     "SELECT count(*), coalesce(pg_total_relation_size('usenet_resume_state'),0) "
    #     "FROM usenet_resume_state"
    # )
    # print(
    #     f"sakms-resume-vacuum: cutoff={cutoff}Z before={before} "
    #     f"deleted_stale={deleted} after={after}"
    # )
    # return 0


if __name__ == "__main__":
    raise SystemExit(main())
