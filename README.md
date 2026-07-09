# Tidyarr

A unified, human-in-the-loop review console for Sonarr, Radarr, and Whisparr.

One self-hosted web app, three isolated modes (Movies, Series, Adult), four
review workflows (Rename, Purge, Dedup, Tag) — every disk or API mutation is
staged for approval before anything actually happens. No Stash dependency.

## Status

Early scaffolding. What's real so far: a Go server with a SQLite-backed
migration runner, the Sonarr/Radarr/Whisparr client and the full
StashDB/FansDB/TPDB/Brave identification pipeline (ported from the CLIs this
project grew out of), a `/api/connections` endpoint to test and persist
service credentials (encrypted at rest — see below), and all four review
workflows the design calls for, three of them queue-staged:
**Rename** (`POST /api/modes/{movies,series,adult}/rename/scan` finds orphaned
files, identifies them, and stages one proposal per item — Movies/Series via
the *arr app's own TVDB/TMDB lookup, falling back to an AI-guessed title when
that lookup finds nothing, Adult via the StashDB/FansDB/TPDB + AI
identification pipeline, with Apply carrying the resolved scene identifier
through to Whisparr V3. Every AI-assisted feature (Adult identification,
Movies/Series' title-guess fallback, and Kids/general classification below)
shares one configured provider+model — Ollama, OpenAI, Gemini, or Anthropic,
`GET`/`PUT /api/settings/ai-provider` and `/api/settings/ai-model` — behind a
single internal interface every prompt is written against, so switching
providers needs no other code changes. When Adult identification confidently
identifies a file via web search but it matches no existing scene anywhere,
the resulting Unmatched proposal can be given back to the community
databases as a new scene draft — `POST /api/proposals/{id}/submit-draft`,
preferring TPDB when configured and falling back to StashDB — a separate,
explicitly human-triggered action, unlike the original CLIs' automatic
submission during scan. Movies/Series Rename also classifies matched content
as kids-appropriate or not (certification/genre first, the same shared AI as
a fallback when that signal is weak) and routes it to a per-mode Kids root
folder — `GET`/`PUT /api/modes/{movies,series}/rename/kids-root-path`, picked
explicitly from the mode's own real root folders rather than guessed from a
naming convention; Apply physically relocates the file into that root before
registering it, since Sonarr/Radarr can only import from where it's actually
sitting), **Purge** (`POST
/api/modes/{movies,series,adult}/purge/scan` matches a per-mode tag allowlist,
managed via `/api/modes/{mode}/purge/allowlist`, against every tracked
item's native tags — Adult needed no code changes, since Whisparr's tracked
scenes resolve through the same `movie` resource Radarr already uses), and
**Dedup** (`POST /api/modes/{movies,adult}/dedup/scan` groups unmapped
files with any already-tracked item sharing the same identifier — TMDB ID
for Movies, the resolved scene's foreignID for Adult — ffprobes every
candidate directly, and stages a proposal per duplicate group with a
precomputed quality winner; Series isn't wired up).
These three stage proposals in one shared, persisted review queue; `POST
/api/proposals/{id}/apply` commits exactly the one a human approved —
Dedup's apply optionally takes `{"keepIndex": n}` or `{"keepAll": true}` to
override the auto-computed winner. Nothing is ever applied in bulk.
**Tag** is the fourth, and the first workflow live for all three modes —
Movies, Series, and Adult (Whisparr V3): `GET /api/modes/{mode}/tags` for
the live vocabulary and `POST`/`DELETE
/api/modes/{mode}/items/{itemId}/tags[/{tagId}]` to assign or remove one,
creating a genuinely new tag upstream automatically — this one isn't staged
through the review queue, since assigning a tag is already a single
deliberate action, not an automatic decision needing approval. Series Dedup,
AI-suggested tags, and the React frontend don't exist yet. All three Adult
workflows (Rename, Purge, Dedup) are now live, though tracked-vs-orphan Adult
Dedup rests on an unverified assumption about Whisparr's API response shape
(see the commit history) — not yet run against a real Whisparr instance.
Not ready to run as a media tool.

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
