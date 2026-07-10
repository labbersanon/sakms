#!/bin/sh
# Runs as root (the image's default USER), fixes up ownership of a
# bind-mounted data dir that will usually belong to whatever host user
# created it — not the container's sakms user — then drops to sakms
# for the real process. Without this, a plain `docker run -v host/path:/data`
# fails on first boot with "unable to open database file", since sqlite
# can't create sakms.db in a directory sakms can't write to.
#
# PUID/PGID (both default 1000, matching the image's baked-in sakms
# uid/gid) let a caller re-point the in-container sakms user at their own
# host uid/gid at container start, instead of only being able to fix
# ownership the other way (chown-ing the mount to the image's fixed 1000).
# usermod/groupmod are already available here — they ship in the same
# `passwd` package that provides `useradd`, already used at image build
# time, so no extra package install is needed.
set -e
PUID="${PUID:-1000}"
PGID="${PGID:-1000}"

if [ "$(id -g sakms)" != "$PGID" ]; then
    groupmod -o -g "$PGID" sakms
fi
if [ "$(id -u sakms)" != "$PUID" ]; then
    usermod -o -u "$PUID" sakms
fi

chown -R sakms:sakms "${SAKMS_DATA_DIR:-/data}"
exec gosu sakms "$@"
