# SAK Media Server

A unified, human-in-the-loop review console and media manager for Movies,
Series, and Adult content. Movies and Series both own their own library
directly now (no Radarr or Sonarr involved); Adult still layers on
Whisparr.

One self-hosted web app, three isolated modes (Movies, Series, Adult), five
review/management workflows (Search, Rename, Purge, Dedup, Tag) — every disk
or API mutation is staged for approval before anything actually happens. No
Stash dependency.

## Status

Early scaffolding. What's real so far: a Go server with a SQLite-backed
migration runner, the Sonarr/Radarr/Whisparr client (still used for Adult,
and by the one-time Sonarr importer — see below) and the full
StashDB/FansDB/TPDB/Brave identification pipeline (ported from the CLIs this
project grew out of), a `/api/connections` endpoint to test and persist
service credentials (encrypted at rest — see below), and all four review
workflows the design calls for, three of them queue-staged:
**Rename** (`POST /api/modes/{movies,series,adult}/rename/scan` finds orphaned
files, identifies them, and stages one proposal per item — Movies via TMDB
search, Series via TMDB's TV search plus its season-details endpoint
(resolving each orphan file's own season/episode from its name — one
proposal per episode file, even inside a season-pack folder), Adult via
the StashDB/FansDB/TPDB + AI identification pipeline, with Apply carrying
the resolved scene identifier through to Whisparr V3. Every AI-assisted
feature (Adult identification, Movies/Series' title-guess fallback, and
Kids/general classification below) shares one configured provider+model —
Ollama, OpenAI, Gemini, or Anthropic, `GET`/`PUT /api/settings/ai-provider`
and `/api/settings/ai-model` — behind a single internal interface every
prompt is written against, so switching providers needs no other code
changes. When Adult identification confidently identifies a file via web
search but it matches no existing scene anywhere, the resulting Unmatched
proposal can be given back to the community databases as a new scene
draft — `POST /api/proposals/{id}/submit-draft`, preferring TPDB when
configured and falling back to StashDB — a separate, explicitly
human-triggered action, unlike the original CLIs' automatic submission
during scan. Movies/Series Rename also classifies matched content as
kids-appropriate or not (certification/genre first for Movies — TMDB's
season/episode data carries neither for Series, so Series always falls
back to the shared AI classifier when a Kids root is configured) and
routes it to a per-mode Kids root folder — `GET`/`PUT
/api/modes/{movies,series}/rename/kids-root-path` (`{"path": "..."}`; an
empty path is the "off" state, not an error), free-typed for both modes
now (see below) rather than picked from a live *arr app or guessed from a
naming convention. Kids/general drift reconciliation for already-tracked
items (auditing whether a tracked item's classification has since changed)
is a known v1 simplification neither Movies' nor Series' library-backed
Rename does yet — new orphans still get classified, just not
already-tracked items on every Scan). Movies/Series' orphan discovery walks
the filesystem recursively now (`internal/library.ScanRootFolder`), so a new
season added later, or a new file dropped alongside something already
tracked, is still found — not masked forever the way a single-level scan
would. Rename renames Movies/Series files into a configurable naming
convention on Apply — `GET`/`PUT /api/modes/{movies,series}/naming-preset`
(`{"preset": "jellyfin"}` or `"legacy"`) — defaulting to a Jellyfin/Emby-
standard shape (`Title (Year) [tmdbid-N]` folders/files; Movies get real
renaming for the first time here, not just relocation), with a "legacy"
option preserving this project's original dash-separated Series
convention so an already-renamed library's shape never silently changes
after an upgrade. A file/folder that already matches the active preset is
never re-proposed, even if it was never tracked (e.g. a library organized
by hand). **Purge** (`POST
/api/modes/{movies,series,adult}/purge/scan` matches a per-mode tag
allowlist, managed via `/api/modes/{mode}/purge/allowlist`, against every
tracked item's tags — Movies/Series against their own local library tags,
Adult against Whisparr's native tag resource), and **Dedup** (`POST
/api/modes/{movies,series,adult}/dedup/scan` groups unmapped files with any
already-tracked item sharing the same identifier — TMDB ID for Movies,
`(show TMDB id, season, episode)` for Series, the resolved scene's
foreignID for Adult — ffprobes every candidate directly, and stages a
proposal per duplicate group with a precomputed quality winner. For
Movies and Series, sharing an identifier is necessary but no longer
sufficient: within each such group SAK also computes a CPU perceptual hash
over several sampled frames of each candidate and only treats two files as
duplicates if their hashes are within a Hamming-distance threshold (tunable
per mode via `GET`/`PUT /api/modes/{mode}/phash-threshold`, default 10) — so
same-identifier files that look different (a wrong match, a different cut, an
extras file) are kept, not auto-deduped. Adult still groups by
identifier alone. For
Series, "the tracked copy" for a duplicate group is simply the one
`library.Episode` row for that exact season/episode — the schema's own
uniqueness constraint on that triple rules out ambiguity — and a
duplicate file inside a season-pack folder groups with a duplicate loose
single-episode file naturally, since a pack is broken into individual
files before grouping happens).
These three stage proposals in one shared, persisted review queue; `POST
/api/proposals/{id}/apply` commits exactly the one a human approved —
Dedup's apply optionally takes `{"keepIndex": n}` or `{"keepAll": true}` to
override the auto-computed winner. Nothing is ever applied in bulk.
**Tag** is the fourth, and the first workflow live for all three modes —
Movies, Series, and Adult (Whisparr V3): `GET /api/modes/{mode}/tags` for
the live vocabulary and `POST`/`DELETE
/api/modes/{mode}/items/{itemId}/tags[/{tagId}]` to assign or remove one —
Movies/Series both use local string-label tags (no numeric id — `id` and
`label` are the same string), Adult still creates a genuinely new tag
upstream on Whisparr automatically. Not staged through the review queue,
since assigning a tag is already a single deliberate action, not an
automatic decision needing approval. AI-suggested tags don't exist yet.
All three Adult workflows (Rename, Purge, Dedup) are now live, though
tracked-vs-orphan Adult Dedup rests on an unverified
assumption about Whisparr's API response shape (see the commit history) —
not yet run against a real Whisparr instance.

Movies and Series also get a fifth capability, **Search** — a
reimplementation of Radarr's/Sonarr's own indexer-search and download-grab
functionality directly into SAK, with a Seerr/Overseerr-style browse UI in
front of it, rather than treating them as a separate app to review after
the fact. It deliberately isn't staged through the same proposals queue as
Rename/Purge/Dedup: a grab's lifecycle (queued → downloading → completed →
imported → failed) doesn't fit that Pending/Unmatched/Applied/Dismissed
review-queue model, so it's tracked in its own `internal/grabs` store
instead. `GET /api/modes/{movies,series}/search?q=...` proxies a query to
**Prowlarr** (one indexer aggregator client, not one per tracker), covering
both torrent (**qBittorrent**) and usenet (**NZBGet**) protocols, and
scores every result with `internal/release`'s pragmatic title parser
(resolution/source/codec/group) against each mode's configured quality
tier (see below). For Series, an optional season/episode picker next to
the search box builds a targeted `"Show S03E05"`/`"Show S03"` query for a
single episode or a whole season pack — free-text search still works too.
`POST /api/modes/{mode}/search/grab` sends the picked release to
qBittorrent or NZBGet (by protocol) and records a `Grab` (carrying
season/episode numbers for Series); `GET /api/modes/{mode}/grabs` lists
them for that mode. Nothing auto-imports: `POST
/api/grabs/{id}/check-import` is a manual, human-clicked refresh that polls
the download client's current status and, once it reports complete,
relocates the finished download and records it directly in Movies'/Series'
own library (reusing Rename's own file-relocation logic; a completed
season-pack grab resolves every episode file inside the download, not just
one) — there is no background poller or scheduler anywhere in this
codebase, by design, matching every other workflow's "nothing happens
without a click" rule. A **Discover** browse view sits in front of Search:
`GET /api/modes/{mode}/discover` proxies TMDB's trending/popular titles
(poster art, overview, rating) for that mode's catalog, and picking a
title auto-fills Search's query. None of this touches Adult/Whisparr
search, or Jellyfin — Jellyfin integration is a distinct, not-yet-started
piece of the roadmap, kept deliberately separate. Also out of scope for
now: RSS/automatic search, a calendar, and blocklist/retry-on-failure.

**Neither Movies nor Series uses a *arr app at all anymore** — each owns
its own library directly (`internal/library`): Movies' flat `Item`
(`{tmdbId, title, file path, root folder}` plus freeform tags), and
Series' genuinely different `Series`/`Episode` pair — a `Series` parent row
plus one `Episode` row per season/episode TMDB reports, whether or not a
file exists for it yet (`file_path == ""` is exactly what makes "missing
episodes" a plain query instead of an inferred state). The practical
differences for both modes: no more `radarr`/`sonarr` connection required
to do anything — Settings has a plain **library root-folder path** for
each (`GET`/`PUT /api/modes/{movies,series}/library/root-folder`,
`{"path": "..."}`, free-typed since neither has a *arr app left to
enumerate folders from); Rename resolves orphans via TMDB search (movie
search for Movies, TV search + season-details for Series) and records new
items straight into the library (no register-then-rescan round trip — the
record itself IS the tracked state); Purge and Tag both work off the
library's own tags/ids directly (Purge deletes files itself; Tag's
vocabulary is local free-form strings — `GET /api/modes/{mode}/tags`
returns `{id, label}` pairs where `id` is the label itself, since a local
tag has no numeric id). The setup wizard's Movies and Series steps are
both a root-folder path instead of a connection test, and `GET
/api/setup/status`'s per-mode `arrConfigured` flag reports whether that
path is set rather than whether a *arr connection exists.

**Migrating an existing Sonarr library**: `POST
/api/series/import-from-sonarr` ("Import from Sonarr" in Series' Settings,
shown only while a `sonarr` connection still exists) is a one-time,
human-triggered import — for every series Sonarr currently tracks, it
resolves the show's TVDB id to a TMDB id (TMDB's `/find` endpoint, the
reverse of the `/tv/{id}/external_ids` call Discover already uses), walks
that series' folder on disk directly (Sonarr exposes no per-episode-file
API to ask instead — matching this project's existing "compute what's on
disk yourself" philosophy), and records both the episodes actually found
and, from TMDB's season data, the ones that aren't (real "missing
episodes" data from the very first import). Read-only against Sonarr —
makes no changes there — and safe to run more than once, since every write
is an idempotent upsert.

Quality tiers (**Low/Medium/High/Lossless**, `GET`/`PUT
/api/modes/{mode}/quality-prefs`) drive Search's scoring for both Movies
and Series — deliberately a bitrate/compression preference (source +
codec: Low favors smaller WEBRip/x265, Lossless favors an uncompressed
remux/Blu-ray), never a resolution one. Maximum resolution is its own
independent setting in the same request (`{"tier": "high", "maxResolution":
1080}`, 0 = no cap) — a soft preference that reorders results toward
at-or-below-cap without ever excluding an over-cap result outright, so
Search never comes back empty just because nothing meets the cap. Search's
scoring also weighs a torrent's seeder count and a usenet post's age
(capped, favoring more-established posts) heavily, plus a small bonus for
Prowlarr's own freeleech/internal indexer flags — the one "reputation"
signal used, sourced entirely from Prowlarr with no additional lookup.

A working frontend now exists: a single dependency-free HTML/JS page (no
build step, no framework) embedded into the Go binary and served at `/` —
Settings (connections, AI provider/model, per-mode library root folder,
per-mode Kids root path, per-mode Purge allowlist) plus all five workflow
views (Search/Rename/Purge/Dedup/Tag) for each mode (Search only for
Movies/Series, matching the backend), driving the exact same API a script
would. Functional, not polished — it's for exercising the real workflows
against a real Whisparr/Prowlarr/qBittorrent/NZBGet/TMDB instance (Movies
and Series need none of these to be reviewed — just their own library root
folder), not a finished design.

Every install is gated behind three layers, most fundamental first, each
hiding all navigation until it's satisfied: **login**, then the **connections
setup wizard**, then the normal app. A fresh instance's very first screen is
"Create your SAK login" (`POST /api/auth/setup`, one username/password,
bcrypt-hashed) — there is no unauthenticated path to anything else, since an
unauthenticated SAK could otherwise be used to control every connected
service. `POST /api/auth/login` and `/logout` manage the session afterward;
`GET /api/auth/status` reports `{configured, authenticated}`. The session
itself is a stateless, AES-GCM-encrypted cookie (same key as connection
secrets, see below) carrying only an expiry — 30-day TTL, `HttpOnly`,
`SameSite=Lax`, deliberately not `Secure` since SAK's primary deployment
is plain HTTP on a LAN, same as Radarr/Sonarr/Whisparr themselves. Once
logged in, the setup wizard walks through setting Movies' and Series'
library root folders — required, since neither mode can do anything
without one — plus Whisparr and an AI provider (both optional); "Continue
to SAK" stays disabled with an inline explanation until both root folders
are actually configured, so dismissing the wizard can never strand a user
on a bare, useless Scan button.

Secrets are encrypted at rest with a locally generated key
(`<data-dir>/secret.key`, mode 0600) rather than an OS keychain — the
primary deployment target is a headless Docker container, which has no
desktop session for a keychain to run in.

## Why

Sonarr, Radarr, and Whisparr each make matching, dedup, and cleanup
decisions well on their own, but there's no single place to see what a
change is *about* to do before it happens, correct a bad match, resolve a
duplicate by eye, or search for something to purge that isn't already on an
allowlist — and no shared view across all three libraries. SAK started as
that review layer, not a replacement for any of them — Movies and Series
have since grown into genuine replacements for Radarr and Sonarr
specifically (each owns its own library, its own indexer search and grab),
while Adult still layers the same review workflows on top of Whisparr
underneath.

## Install

Not published yet. The plan is a multi-arch Docker image (GHCR) as the
primary path, with prebuilt binaries on GitHub Releases for non-Docker
installs — see the design spec for details once it's linked here.

## Development

Requires Go 1.25+, plus `ffprobe` and `ffmpeg` on `PATH` if you want to
exercise Dedup (it ffprobes real files directly — see `internal/mediainfo`
— and, for Movies, decodes sampled frames with `ffmpeg` for the perceptual
hash, see `internal/phash`). Every other workflow runs without them; the
`internal/phash` unit tests fake the ffmpeg runner, and its build-tagged
real-ffmpeg integration test (`go test ./internal/phash/ -tags integration`)
skips cleanly when `ffmpeg`/`ffprobe` are absent.

```sh
go run ./cmd/sakms
```

Then open `http://localhost:8080/` for the UI (the API itself lives under
`/api/...`, same port).

Configuration is via environment variables for now:

| Variable           | Default   | Purpose                          |
|---------------------|-----------|-----------------------------------|
| `SAKMS_ADDR`      | `:8080`   | HTTP listen address               |
| `SAKMS_DATA_DIR`  | `./data`  | Where `sakms.db` and `secret.key` live — back both up together, or a backup of one without the other is useless |

### Docker

A `Dockerfile` builds a Debian-based image (multi-stage: `golang:trixie` to
compile, `debian:trixie-slim` plus `ffmpeg` to run — `ffprobe` needs a real
build, not Alpine's, and there's no CGO to make musl-vs-glibc a tradeoff
either way). The container starts as root only long enough for
`docker-entrypoint.sh` to `chown` a bind-mounted `/data` to the image's
non-root `sakms` user, then drops to it via `gosu` — without that, a plain
`docker run -v host/path:/data` fails on first boot, since sqlite can't
create `sakms.db` in a directory owned by whatever host user made the
mount point.

By default the in-container `sakms` user is uid/gid `1000:1000`. Set
`PUID`/`PGID` to match your host user instead (common when the bind-mounted
`/data` is owned by something other than 1000:1000) — the entrypoint
re-maps `sakms` to those ids at container start, before the `chown`/`gosu`
step above, e.g.:

```sh
docker run -e PUID=$(id -u) -e PGID=$(id -g) -v host/path:/data ...
```

`scripts/docker-dev.sh` wraps the build-run-check loop into one command for
iterating on the image itself (not a deployment tool — a `docker-compose.yml`
for real use, with the volume mounts Rename's file moves need to match
Radarr/Sonarr's own paths, is still to come). Default (no args) rebuilds,
restarts, and polls `/healthz`, printing the container's own logs
automatically on failure instead of requiring a second command:

```sh
./scripts/docker-dev.sh          # build + restart + health-check (the default)
./scripts/docker-dev.sh logs     # follow the running container's logs
./scripts/docker-dev.sh shell    # a shell in the built image, server not started
./scripts/docker-dev.sh clean    # stop the container and wipe its dev data dir
```

Env overrides: `IMAGE_TAG`, `CONTAINER_NAME`, `HOST_PORT`, `DATA_DIR`,
`HEALTH_TIMEOUT` — see the script's `--help`.

## License

AGPL-3.0 — see [LICENSE](LICENSE). Once vendored, the perceptual-hash code
ported from [stashapp/stash](https://github.com/stashapp/stash) will be
credited here and in its source files; no Stash server runs at build time
or runtime.
