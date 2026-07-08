# Tidyarr

A unified, human-in-the-loop review console for Sonarr, Radarr, and Whisparr.

One self-hosted web app, three isolated modes (Movies, Series, Adult), four
review workflows (Rename, Purge, Dedup, Tag) — every disk or API mutation is
staged for approval before anything actually happens. No Stash dependency.

## Status

Early scaffolding. What's real so far: a Go server with a SQLite-backed
migration runner, the Sonarr/Radarr/Whisparr client and the full
StashDB/FansDB/TPDB/Brave/Ollama identification pipeline (ported from the
CLIs this project grew out of), a `/api/connections` endpoint to test and
persist service credentials (encrypted at rest — see below), and three full
review workflows: **Rename** (`POST /api/modes/{movies,series}/rename/scan`
finds orphaned files, identifies them, and stages one proposal per item),
**Purge** (`POST /api/modes/{movies,series}/purge/scan` matches a per-mode
tag allowlist, managed via `/api/modes/{mode}/purge/allowlist`, against
every tracked item's native tags), and **Dedup**, Movies only for now
(`POST /api/modes/movies/dedup/scan` groups unmapped files with any
already-tracked item sharing the same TMDB ID, ffprobes every candidate
directly, and stages a proposal per duplicate group with a precomputed
quality winner). All three stage proposals in one shared, persisted review
queue; `POST /api/proposals/{id}/apply` commits exactly the one a human
approved — Dedup's apply optionally takes `{"keepIndex": n}` or
`{"keepAll": true}` to override the auto-computed winner. Nothing is ever
applied in bulk. Tag, Series Dedup, Adult mode, and the React frontend
don't exist yet. Not ready to run as a media tool.

Secrets are encrypted at rest with a locally generated key
(`<data-dir>/secret.key`, mode 0600) rather than an OS keychain — the
primary deployment target is a headless Docker container, which has no
desktop session for a keychain to run in.

## Why

Sonarr, Radarr, and Whisparr each make matching, dedup, and cleanup
decisions well on their own, but there's no single place to see what a
change is *about* to do before it happens, correct a bad match, resolve a
duplicate by eye, or search for something to purge that isn't already on an
allowlist — and no shared view across all three libraries. Tidyarr is that
review layer, not a replacement for any of them.

## Install

Not published yet. The plan is a multi-arch Docker image (GHCR) as the
primary path, with prebuilt binaries on GitHub Releases for non-Docker
installs — see the design spec for details once it's linked here.

## Development

Requires Go 1.25+, plus `ffprobe` on `PATH` if you want to exercise Dedup
(it ffprobes real files directly — see `internal/mediainfo`). Every other
workflow runs without it.

```sh
go run ./cmd/tidyarr
```

Configuration is via environment variables for now:

| Variable           | Default   | Purpose                          |
|---------------------|-----------|-----------------------------------|
| `TIDYARR_ADDR`      | `:8080`   | HTTP listen address               |
| `TIDYARR_DATA_DIR`  | `./data`  | Where `tidyarr.db` and `secret.key` live — back both up together, or a backup of one without the other is useless |

## License

AGPL-3.0 — see [LICENSE](LICENSE). Once vendored, the perceptual-hash code
ported from [stashapp/stash](https://github.com/stashapp/stash) will be
credited here and in its source files; no Stash server runs at build time
or runtime.
