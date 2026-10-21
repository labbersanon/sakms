#!/usr/bin/env python3
# Claude 2026-08-04: wipe_data() now covers Postgres + filesystem after SQLite→PG cutover.
#   --wipe-data drops/recreates DB `sakms` in container sakms-db AND clears DATA_DIR.
#   --wipe-fs-only / --wipe-db-only for single-side wipes. Each mode requires typing YES.
# Reason: post-PG, FS-only wipe leaves encrypted columns undecryptable (secret.key gone).
# Troubleshooting: needs running sakms-db; confirmations are interactive (no systemd path).
# Review if: DB name/container rename or secret.key moves off DATA_DIR.
# Related files: deploy/sakms/compose.yml, sakms_auto_update.md
# Claude 2026-07-12: removed automatic wipe_data() call from deploy() — normal updates now
#   preserve /mnt/iscsi/sakms (db + secret.key) across rebuilds instead of discarding it.
#   rollback() NO LONGER wipes (changed 2026-08-04 after a live wipe outage). Added
#   a --wipe-data CLI flag remains for manual opt-in wipes only
#   (run directly: `/usr/local/bin/sakms-auto-update.py --wipe-data`).
# Reason: Wade asked to stop wipe on normal deploy (2026-07-12); wipe-on-rollback
#   removed 2026-08-04 after it destroyed a live DB on a health-check timeout.
#   Explicitly decided in the same conversation: the already-known-compromised live API key
#   (see sakms_auto_update.md memory) is left as-is, not force-rotated by this change.
# Troubleshooting: state at /var/lib/sakms-updater/state.json; logs via
#   `journalctl -u sakms-auto-update.service`; compose.yml.bak is the last-known-good backup
# Review if: rollback()'s unconditional wipe becomes undesired, or --wipe-data needs to be
#   reachable via the systemd service (currently CLI-only — the .service unit takes no args)
# Claude 2026-07-13: added app_boot_ok() — health_ok() alone only checks /healthz, which
#   returns "ok" unconditionally and never touches the frontend or auth-boot sequence. A
#   build that compiles and passes health_ok() but ships a runtime-broken embedded SPA (the
#   sakms frontend is now a SolidJS+Vite bundle, cut over 2026-07-13) would previously be
#   marked "succeeded" with no rollback. app_boot_ok() additionally confirms the served shell
#   contains the real app marker and that /api/auth/status responds — both deploy() and
#   rollback() now require both checks before declaring success.
# Reason: sakms frontend-redesign plan's Guardrail #8 / Acceptance Criterion 15
#   (~/projects/tidyarr/.omc/plans/frontend-redesign-seerr.md) — verified locally (real
#   binary passes both checks; a simulated broken shell with /healthz=200 but a garbage body
#   and a 500 from /api/auth/status correctly fails both) before installing here.
# Troubleshooting: if app_boot_ok() fails and you believe the deploy is actually fine, check
#   APP_MARKER still matches frontend/index.html's <title> — it changes only if the app's
#   <title> tag itself is edited.
# Claude 2026-07-14: updated REPO_URL from github.com/curtiswtaylorjr/sakms to
#   github.com/labbersanon/sakms — Wade renamed his GitHub username; GitHub was still
#   auto-redirecting the old URL (confirmed working), but relying on that indefinitely is
#   fragile (the old username could be reclaimed by someone else, silently breaking pulls).
# Reason: confirmed directly with Wade this was an intentional rename, not an unauthorized
#   repo transfer, before touching anything.
# Troubleshooting: if `git ls-remote`/`docker compose build` starts failing with a 404 here,
#   confirm the current canonical URL with `git -C ~/projects/tidyarr remote get-url origin`
#   on wade-pc first — REPO_URL must match exactly.
# INCIDENT (same day, caught and fixed within the hour): the first version of this change also
#   narrowed CONTEXT_RE to match ONLY the new username. compose.yml on disk still had the OLD
#   username pinned from the last successful deploy, so the regex found 0 matches, pin_context()
#   raised, and the caught exception correctly triggered rollback() — which unconditionally wipes
#   /mnt/iscsi/sakms, discarding whatever was configured on the freshly-reset instance at the
#   time. Root cause: pin_context() used to substitute via the regex's own captured group (`\1`),
#   so it could only ever re-stamp whatever username was ALREADY on disk with a new SHA — it
#   could never actually migrate to a new REPO_URL, even after this file was updated. Fixed by
#   rebuilding the whole context value from REPO_URL (see pin_context()) and widening CONTEXT_RE
#   to match any github username generically, so it finds-and-replaces regardless of current
#   state. Verified against the real compose.yml on disk before reinstalling.
# Claude 2026-10-21: default deploy is --deploy-local from the server1 bare
#   repo, not git ls-remote of GitHub main.
# Reason: live updates happen from wade-pc after local tests; nightly only
#   fast-forwards origin/main. If systemd still polled GitHub while GitHub
#   lagged local main, ls-remote would be the old SHA and a start would
#   rebuild/roll live backward.
# Troubleshooting: push HEAD:main to /var/lib/sakms-updater/sakms.git then
#   systemctl start sakms-auto-update.service (ExecStart --deploy-local).
#   Same SHA as state.json → exit 0, no compose down. --from-github is the
#   emergency GitHub rebuild and still refuses when GitHub is behind live.
# Review if: compose build.context becomes a registry image.
# Related files: scripts/sakms-deploy-local.sh, protocol_sakms_local_merge.md
import argparse
import json
import os
import re
import subprocess
import sys
import time

REPO_URL = "https://github.com/labbersanon/sakms.git"
BRANCH = "main"
COMPOSE_DIR = "/opt/docker/media/automation/sakms"
COMPOSE_FILE = os.path.join(COMPOSE_DIR, "compose.yml")
COMPOSE_BACKUP = os.path.join(COMPOSE_DIR, "compose.yml.bak")
DATA_DIR = "/mnt/iscsi/sakms"
PG_CONTAINER = "sakms-db"
PG_USER = "sakms"
PG_DATABASE = "sakms"
STATE_DIR = "/var/lib/sakms-updater"
STATE_FILE = os.path.join(STATE_DIR, "state.json")
LOCAL_BARE = os.path.join(STATE_DIR, "sakms.git")
LOCAL_SRC = os.path.join(STATE_DIR, "src")
HEALTH_URL = "http://sakms:8080/healthz"
APP_URL = "http://sakms:8080/"
AUTH_STATUS_URL = "http://sakms:8080/api/auth/status"
APP_MARKER = "SAK Media Server"
HEALTH_RETRIES = 40
HEALTH_DELAY_S = 3

CONTEXT_RE = re.compile(
    r"context:\s*(?:https://github\.com/[^/\s]+/sakms\.git#[^\s\"']+|"
    + re.escape(LOCAL_SRC)
    + r")"
)


def log(msg):
    print(msg, flush=True)


def run(cmd, cwd=None, check=True):
    log(f"+ {' '.join(cmd)}")
    return subprocess.run(cmd, cwd=cwd, check=check, capture_output=True, text=True)


def get_remote_sha():
    r = run(["git", "ls-remote", REPO_URL, f"refs/heads/{BRANCH}"])
    line = r.stdout.strip().splitlines()[0]
    return line.split()[0]


def get_local_sha():
    if not os.path.isdir(LOCAL_BARE):
        raise RuntimeError(
            f"local mirror missing: {LOCAL_BARE} — push from wade-pc first"
        )
    r = run(["git", "--git-dir", LOCAL_BARE, "rev-parse", f"refs/heads/{BRANCH}"])
    sha = r.stdout.strip()
    if not sha:
        raise RuntimeError(f"no {BRANCH} in {LOCAL_BARE}")
    return sha


def github_is_behind(deployed_sha, remote_sha):
    """True when GitHub main is an ancestor of live (nightly has not pushed yet)."""
    if not deployed_sha or deployed_sha == remote_sha:
        return False
    if not os.path.isdir(LOCAL_BARE):
        return False
    r = run(
        [
            "git",
            "--git-dir",
            LOCAL_BARE,
            "merge-base",
            "--is-ancestor",
            remote_sha,
            deployed_sha,
        ],
        check=False,
    )
    return r.returncode == 0


def checkout_local(sha):
    git_dir = os.path.join(LOCAL_SRC, ".git")
    if not os.path.isdir(git_dir):
        run(["git", "clone", LOCAL_BARE, LOCAL_SRC])
    run(["git", "fetch", "--force", "origin", f"+refs/heads/{BRANCH}:refs/heads/{BRANCH}"], cwd=LOCAL_SRC)
    run(["git", "checkout", "-f", sha], cwd=LOCAL_SRC)
    run(["git", "clean", "-fdx"], cwd=LOCAL_SRC)


def read_state():
    if not os.path.exists(STATE_FILE):
        return {}
    with open(STATE_FILE) as f:
        return json.load(f)


def write_state(sha):
    os.makedirs(STATE_DIR, exist_ok=True)
    with open(STATE_FILE, "w") as f:
        json.dump({"deployed_sha": sha}, f)


def backup_compose():
    with open(COMPOSE_FILE) as f:
        content = f.read()
    with open(COMPOSE_BACKUP, "w") as f:
        f.write(content)


def restore_compose():
    if os.path.exists(COMPOSE_BACKUP):
        with open(COMPOSE_BACKUP) as f:
            content = f.read()
        with open(COMPOSE_FILE, "w") as f:
            f.write(content)


def pin_context(sha, local=False):
    with open(COMPOSE_FILE) as f:
        content = f.read()
    # Pin from LOCAL_SRC or REPO_URL, not the matched username (see header
    # 2026-07-14). Local checkout so docker does not fetch GitHub; SHA may
    # not be on origin/main yet.
    value = LOCAL_SRC if local else f"{REPO_URL}#{sha}"
    new_content, count = CONTEXT_RE.subn(f"context: {value}", content)
    if count != 1:
        raise RuntimeError(f"expected exactly 1 build.context match, found {count}")
    with open(COMPOSE_FILE, "w") as f:
        f.write(new_content)


def confirm_wipe(prompt):
    log(prompt)
    try:
        answer = input("Type YES to proceed (anything else aborts): ").strip()
    except EOFError:
        log("wipe aborted: no TTY / EOF")
        return False
    if answer != "YES":
        log("wipe aborted: confirmation not YES")
        return False
    return True


def wipe_fs():
    if not os.path.isdir(DATA_DIR):
        log(f"wipe-fs: {DATA_DIR} missing — nothing to clear")
        return
    for name in os.listdir(DATA_DIR):
        if name == "lost+found":
            continue
        path = os.path.join(DATA_DIR, name)
        run(["rm", "-rf", path])
    log(f"wipe-fs: cleared contents of {DATA_DIR} (kept lost+found)")


def wipe_db():
    # Terminate backends, drop + recreate application database inside sakms-db.
    sql = (
        f"SELECT pg_terminate_backend(pid) FROM pg_stat_activity "
        f"WHERE datname = '{PG_DATABASE}' AND pid <> pg_backend_pid(); "
        f"DROP DATABASE IF EXISTS {PG_DATABASE}; "
        f"CREATE DATABASE {PG_DATABASE} OWNER {PG_USER};"
    )
    run(
        [
            "docker", "exec", "-i", PG_CONTAINER,
            "psql", "-U", PG_USER, "-d", "postgres", "-v", "ON_ERROR_STOP=1",
            "-c", sql,
        ]
    )
    log(f"wipe-db: recreated database {PG_DATABASE} in {PG_CONTAINER}")


def wipe_data():
    if not confirm_wipe(
        f"WILL WIPE BOTH: filesystem {DATA_DIR} AND Postgres database "
        f"{PG_DATABASE} in container {PG_CONTAINER}."
    ):
        raise RuntimeError("wipe-data declined")
    wipe_db()
    wipe_fs()


def wipe_fs_only():
    if not confirm_wipe(
        f"WILL WIPE FILESYSTEM ONLY: {DATA_DIR} (Postgres {PG_DATABASE} untouched)."
    ):
        raise RuntimeError("wipe-fs-only declined")
    wipe_fs()


def wipe_db_only():
    if not confirm_wipe(
        f"WILL WIPE DATABASE ONLY: {PG_DATABASE} in {PG_CONTAINER} "
        f"(filesystem {DATA_DIR} untouched)."
    ):
        raise RuntimeError("wipe-db-only declined")
    wipe_db()


def compose(*args):
    return run(["docker", "compose", *args], cwd=COMPOSE_DIR)


def curl_container(url, extra_args):
    return run(
        [
            "docker", "run", "--rm", "--network", "webui",
            "curlimages/curl", "-s", *extra_args, url,
        ],
        check=False,
    )


def health_ok():
    for attempt in range(1, HEALTH_RETRIES + 1):
        r = curl_container(HEALTH_URL, ["-o", "/dev/null", "-w", "%{http_code}"])
        code = r.stdout.strip()
        log(f"healthz attempt {attempt}/{HEALTH_RETRIES}: {code}")
        if code == "200":
            return True
        time.sleep(HEALTH_DELAY_S)
    return False


def app_boot_ok():
    shell = curl_container(APP_URL, [])
    if APP_MARKER not in shell.stdout:
        log(f"app boot check: served shell missing expected marker {APP_MARKER!r}")
        return False
    auth = curl_container(AUTH_STATUS_URL, ["-o", "/dev/null", "-w", "%{http_code}"])
    if auth.stdout.strip() != "200":
        log(f"app boot check: /api/auth/status returned {auth.stdout.strip()!r}, expected 200")
        return False
    log("app boot check: shell marker present, auth-status responds — ok")
    return True


def deploy(sha, wipe=False, local=False):
    if local:
        checkout_local(sha)
    pin_context(sha, local=local)
    compose("build")
    compose("down")
    if wipe:
        wipe_data()
    compose("up", "-d", "--force-recreate")


def main(wipe=False, wipe_fs_flag=False, wipe_db_flag=False, deploy_local=False, from_github=False):
    if wipe_fs_flag and wipe_db_flag:
        log("use --wipe-data for both sides, not both --wipe-fs-only and --wipe-db-only")
        return 2
    if wipe and (wipe_fs_flag or wipe_db_flag):
        log("--wipe-data cannot combine with --wipe-fs-only / --wipe-db-only")
        return 2
    if deploy_local and from_github:
        log("use either --deploy-local or --from-github, not both")
        return 2
    if not deploy_local and not from_github:
        log("refusing GitHub poll with no flag — pass --deploy-local (systemd) or --from-github")
        return 2

    state = read_state()
    deployed_sha = state.get("deployed_sha")
    if deploy_local:
        target_sha = get_local_sha()
    else:
        target_sha = get_remote_sha()
        if github_is_behind(deployed_sha, target_sha):
            log(
                f"skip: GitHub {target_sha} is behind live {deployed_sha} "
                "(nightly has not pushed; not rolling back)"
            )
            return 0

    if deployed_sha == target_sha and not (wipe or wipe_fs_flag or wipe_db_flag):
        log(f"up to date at {target_sha}, nothing to do")
        return 0

    log(f"update detected: {deployed_sha or '(none)'} -> {target_sha} (local={deploy_local})")
    backup_compose()
    try:
        if wipe_fs_flag:
            wipe_fs_only()
        elif wipe_db_flag:
            wipe_db_only()
        deploy(target_sha, wipe=wipe, local=deploy_local)
    except RuntimeError as e:
        if "declined" in str(e):
            log(str(e))
            return 2
        log(f"deploy failed: {e}")
        return rollback()
    except subprocess.CalledProcessError as e:
        log(f"deploy command failed: {e.cmd}\nstdout={e.stdout}\nstderr={e.stderr}")
        return rollback()
    except Exception as e:
        log(f"deploy failed: {e}")
        return rollback()

    if health_ok() and app_boot_ok():
        write_state(target_sha)
        log(f"update succeeded, now at {target_sha}")
        return 0

    log("health/app-boot check failed after deploy, rolling back")
    return rollback()


def rollback():
    restore_compose()
    try:
        compose("build")
        compose("down")
        # Claude 2026-08-04: wipe_data() commented out of rollback.
        # Reason: rollback after a failed 67c2778 deploy deleted sakms.db +
        #   secret.key (live outage). Wipe is opt-in via --wipe-data only.
        # Troubleshooting: if rollback leaves a half-migrated DB that cannot
        #   boot the previous image, restore from TrueNAS
        #   HomeNAS/server1/server1-sakms@auto-* snapshot instead of wiping.
        # Review if: a half-migrated-DB recovery path is added to this script.
        # wipe_data()
        compose("up", "-d", "--force-recreate")
    except subprocess.CalledProcessError as e:
        log(f"ROLLBACK ITSELF FAILED: {e.cmd}\nstdout={e.stdout}\nstderr={e.stderr}")
        return 1
    if health_ok() and app_boot_ok():
        log("rollback succeeded, previous known-good version restored")
    else:
        log("ROLLBACK DEPLOYED BUT HEALTH/APP-BOOT CHECK STILL FAILING — needs manual attention")
    return 1


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--wipe-data",
        action="store_true",
        help="wipe /mnt/iscsi/sakms AND DROP/CREATE Postgres sakms (interactive YES; "
             "opt-in only — never on rollback)",
    )
    parser.add_argument(
        "--wipe-fs-only",
        action="store_true",
        help="wipe /mnt/iscsi/sakms only (interactive YES; leaves Postgres intact)",
    )
    parser.add_argument(
        "--wipe-db-only",
        action="store_true",
        help="DROP/CREATE Postgres database sakms only (interactive YES; leaves FS intact)",
    )
    parser.add_argument(
        "--deploy-local",
        action="store_true",
        help="build from /var/lib/sakms-updater/sakms.git (not GitHub). systemd default.",
    )
    parser.add_argument(
        "--from-github",
        action="store_true",
        help="emergency: rebuild from GitHub main. Skips if GitHub is behind live.",
    )
    args = parser.parse_args()
    sys.exit(
        main(
            wipe=args.wipe_data,
            wipe_fs_flag=args.wipe_fs_only,
            wipe_db_flag=args.wipe_db_only,
            deploy_local=args.deploy_local,
            from_github=args.from_github,
        )
    )