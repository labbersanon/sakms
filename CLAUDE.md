# SAK Media Server — project guidance

This file orients any session (yours or a future one) picking this project
back up. Read it before making scope decisions, not just implementation
ones — it captures the *why* behind choices already made, so you don't
re-litigate settled questions or, worse, quietly reverse them.

## Mission

Consolidate the redundant single-purpose apps a self-hosted media setup
normally runs (Radarr, Sonarr, Whisparr, Prowlarr, download clients, and
whatever else accumulates around them) into one application. The problem
isn't just that running N containers wastes resources — each one carries
its own base OS image, its own config surface, its own auth, its own
partial view of "the library." That fragmentation is *why* libraries stay
messy even when every individual tool is doing its job correctly: no tool
owns the seams between them, so drift, duplicates, and inconsistent
organization accumulate in the gaps between apps that each only see their
own narrow slice.

**A clean library means the filesystem is exactly as organized as the UI
says it is — no drift between them — and there are no duplicates.** If
SAK's own records say a title is tracked and placed at a given path, that
path is correct and current, right now, not "eventually consistent" after
some reconciliation pass. This is the concrete, checkable bar every
workflow (Rename, Purge, Dedup, Tag, Search/grab) is ultimately in service
of.

**SAK is the sole backend for file management — metadata, renaming, file
placement, and deduplication — across all three modes. Jellyfin and Stash
are downstream media players with zero organizational authority.**
(Decided 2026-07-10.) This is the same displacement already done to
Radarr/Sonarr, stated as a first-class mission principle rather than left
implicit: a player app may read and present SAK's library, but it never
decides what's tracked, where a file lives, what a duplicate is, or what
a file is named. If a player's own convention is useful (e.g. Jellyfin's
naming scheme, adopted as SAK's own default preset), SAK adopts the
*convention*, not a *dependency* on the app that documented it.

## Scope: opportunistic in general, settled for the *arr apps

Radarr is eliminated for Movies (done); Sonarr is eliminated for Series,
including Series Dedup (done); **Whisparr will eventually be eliminated
for Adult too** (decided 2026-07-10, not yet started) — same pattern,
Adult gets its own library-owned Rename/Purge/Dedup/Tag path instead of
depending on Whisparr. Beyond the three `*arr` apps, there is still **no
committed target list**: whether Bazarr, Tdarr, or anything else ever
gets absorbed is an open question, decided app-by-app as the pain of
running it separately becomes concrete. Jellyfin and Stash specifically
are **not** being absorbed as services (SAK does not become a media
player) — see the Mission section above: their *organizational* role is
what's being eliminated, not the apps themselves as viewers. When a new
consolidation opportunity comes up, engage with it on its own merits;
don't cite (or invent) a fixed end-state that includes or excludes it a
priori.

## Automation: manual by default, scheduling earns its way back in

Every workflow SAK has built so far is human-triggered: Scan proposes,
a person approves, Apply commits exactly that one item — no bulk actions,
no background pollers, no scheduler infrastructure exists anywhere in this
codebase today. That's the default for anything new, too — **don't build
speculative scheduling ahead of proven manual usage.**

But this isn't a permanent ban on automation. The *arr apps automate
things (RSS, scheduled searches, quality-cutoff upgrades) safely and
well — once a piece of SAK's manual workflow has been used enough to be
trusted, scheduled automation for that specific piece is a legitimate,
considered upgrade, not a betrayal of the human-in-the-loop principle.
The sequencing matters: manual and proven first, scheduled second, never
the other way around.

## Established engineering conventions

These aren't just style preferences — they're load-bearing for the mission
above, so don't drop them for convenience:

- **Staged-for-approval, one item at a time.** Every mutating action
  (Apply, grab, tag) acts on exactly one already-approved thing. There is
  no "apply everything" path anywhere, by design.
- **Secrets encrypted at rest** (`internal/secrets`, a locally generated
  key file, not an OS keychain — the primary deployment target is a
  headless container with no keychain to use).
- **Single-operator auth**, not multi-tenant. No permissions system, no
  per-user roles — one login gates the whole app, across all three
  supported auth strategies (`password`, `oidc`, `none`). In `oidc` mode
  SAK is a real OpenID Connect Relying Party (Authorization Code flow with
  PKCE, `internal/oidcauth`): successfully completing the IdP login — a
  valid ID token, signature-verified against the IdP's JWKS, with
  issuer/audience/nonce/expiry all checked — IS the one operator
  authenticating. There is no subject-claim allowlist step; restricting
  *who* may complete the IdP's login screen is the IdP's own
  Application/Provider policy job, not SAK's. No user table, no roles, no
  permissions surface is introduced by any mode. The `X-Api-Key` header
  (additive to whichever mode is active, for out-of-process clients)
  doesn't change this either: a key inherits the one operator's full access
  in every mode, it is not a second user or a permissions surface.
  - **Why `oidc` replaced the earlier `forward` + `authentik` modes**
    (2026-07-11): `forward` mode trusted reverse-proxy-injected headers
    (`Remote-User` + a shared `X-Proxy-Secret`), which forced a live secret
    into the proxy's config — against this deployment's secrets policy — and
    isn't even Authentik/Authelia's own model (they use header-stripping +
    network isolation, no shared secret). `authentik` mode was RFC 7662
    bearer-token introspection only: built for API/machine clients that
    already hold a token, never a real browser redirect/callback login. The
    single OIDC flow is provider-agnostic and cryptographically verified
    (JWKS signature check, not a trusted header) and needs no proxy-held
    secret. Both old modes were deleted outright, not deprecated in place —
    see the CHANGELOG entry for full detail.
- **Honesty about unverified assumptions.** When a client's response shape
  is modeled from documentation but not confirmed against a live instance,
  say so explicitly in the package doc — don't present a guess as fact.
- **House HTTP client pattern**: `Config` struct + `Client{cfg, http}` +
  `func New(cfg, httpClient) *Client`, hand-built requests, no interfaces
  for external clients — testable via a concrete `*Client` against
  `httptest.NewServer`. Reserve interfaces for cases where two genuinely
  different concrete backends must satisfy the same internal contract
  (e.g. a workflow package's Servarr-backed path vs. its library-backed
  path) — and even then, prefer parallel sibling functions over a shared
  interface until a second real caller proves the abstraction is worth it.
- **No premature abstraction.** A new backend (e.g. Movies' own library)
  gets its own sibling functions in each workflow package rather than a
  forced-shared code path with the thing it's replacing — especially
  while the replacement (e.g. Series/Sonarr) isn't designed yet and might
  not fit the same shape.
- **No dead code left behind.** When a capability stops being used (e.g.
  Radarr for Movies), remove the application-level wiring that only
  existed to serve it — but don't strip generic, still-valid capability
  from a shared library (e.g. `internal/servarr` keeping Radarr support
  even though `mode.Build` no longer constructs one) just because one
  caller moved on.

## Current state (update this as stages land)

- **Movies**: fully off Radarr. Owns its own library (`internal/library`),
  own Rename/Purge/Dedup/Tag paths, own root-folder + quality-tier
  settings. Search/grab (Prowlarr + qBittorrent/NZBGet) and Discover
  (TMDB) are shared infrastructure, already live for both Movies and
  Series.
- **Series**: fully off Sonarr. Owns its own episode-aware library
  (`internal/library`'s `Series`/`Episode` types — genuinely different
  tables from Movies' `Item`, since Series needs rows for episodes TMDB
  knows about but that aren't on disk yet, to make "missing episodes" a
  real query; see `internal/library`'s package doc). Own
  ScanLibrarySeries/ApplyLibrarySeries Rename and Purge paths, own
  root-folder + quality-tier settings, own episode/season-aware Search →
  grab → check-import. A one-time, human-triggered importer
  (`internal/sonarrimport`, "Import from Sonarr" in Settings) migrates an
  existing Sonarr library by walking disk directly + resolving TVDB→TMDB
  ids via TMDB's `/find` endpoint — read-only against Sonarr, safe to
  re-run. `internal/servarr`'s Sonarr support is kept (still a valid
  generic capability, same precedent as Radarr) even though nothing in
  `mode.Build` constructs one anymore. Series Dedup is built too
  (`dedup.ScanLibrarySeries`/`ApplyLibrarySeries`): duplicates group by
  `(show TMDB id, season, episode)` rather than a single id — "the tracked
  copy" for a key is just the one `library.Episode` row for that exact
  slot (the schema's own `UNIQUE(series_id, season_number,
  episode_number)` constraint rules out ambiguity), and a season-pack
  duplicate groups with a loose single-episode duplicate naturally, since
  a pack is broken into individual files
  (`library.ResolveEpisodeVideoFiles`) before grouping happens.
- **Adult**: fully off Whisparr. Owns its own library
  (`internal/library`'s `Scene` type + `library_scenes` table, keyed on the
  separate `(box, scene_id)` columns a stash-box scene's identity actually
  is), own library-backed Rename/Purge/Dedup/Tag paths
  (`rename.ScanLibraryAdult`/`ApplyLibraryAdult` and the matching Dedup/Purge
  siblings, plus scene-level tags via the `/api/modes/adult/scenes/...`
  routes), own free-typed root-folder setting, and its own fixed Adult naming
  scheme (`naming.AdultFileName`: `Studio - Title (Date) [phash-HASH]`, the
  filename-embedded phash CLAUDE.md always wanted). `mode.Build` constructs no
  Servarr client for Adult anymore — the same displacement already done to
  Radarr/Sonarr. `internal/servarr`'s Whisparr support is kept (still a valid
  generic capability, same precedent as Radarr/Sonarr) even though nothing in
  `mode.Build` constructs one. **Stash is unchanged and still used** — for
  identification (`mode.Session.Stash`, phash-first `rename.scanAdultPhashFirst`
  reads a phash Stash already computed; SAK never computes one) and for
  player-rescan-notify (`Session.NotifyPlayers`); it is a downstream player,
  never an organizational authority, exactly like Jellyfin for Movies/Series.
  A one-time, human-triggered importer (`internal/whisparrimport`, "Import from
  Whisparr" in Settings) migrates an existing Whisparr-tracked Adult library by
  walking disk directly + re-resolving each scene's stash-box UUID against
  StashDB/FansDB — it builds its own standalone `servarr.Client{App: Whisparr}`
  from the saved connection (not through `mode.Build`), so it keeps working now
  that Adult's own Servarr wiring is gone.
- **Adult phash (future work, unchanged by the Whisparr elimination above):**
  the concrete shape decided so far is that SAK will build its own frame-decode
  + StashDB-compatible phash hasher (the `PHASH` algorithm StashDB/FansDB's
  stash-box network indexes — 25-frame collage, 64-bit, a *different* algorithm
  from `internal/phash`'s Movies/Series one, which stays as-is and unrelated)
  so SAK can identify and dedupe Adult content by talking to StashDB/FansDB/TPDB
  directly, without needing a live local Stash instance as a bridge. One hash,
  multiple consumers (identification, Dedup, and the filename-embedded phash for
  fast rescans now that Adult has its own renaming feature). See
  `docs/ROADMAP.md`'s phash entry and `.omc/autopilot/spec-phash-dedup-adult.md`
  (superseded recommendation, kept for its StashDB-algorithm research) for
  detail.
- **Jellyfin**: a live connection now exists (`internal/jellyfin`, the
  "jellyfin" connection type), but for ONE narrow purpose — receiving
  targeted rescan notifications (`mode.Session.NotifyPlayers`, Movies/Series
  only) so its library index stays fresh after SAK's own file ops, the same
  information-flow direction as Radarr/Sonarr/Whisparr's own rescan
  commands. This grants Jellyfin NO organizational authority — SAK still
  owns tracking, placement, naming, and dedup — consistent with the Mission
  section above: Jellyfin stays a downstream player, not an absorbed
  service. Its documented naming convention is separately adopted as SAK's
  own default preset (see below) precisely because that's a convention, not
  a dependency.
- **Naming, scanning, and Season-0 (Stage 2c)**:
  - `library.ScanRootFolder` is now recursive (`filepath.WalkDir`,
    `internal/library/library.go`) — a directory is reported whole only if
    it has no real subdirectories (ignoring bonus-content names in
    `config.ExcludedDirNames`) and no already-known direct children;
    otherwise it's opened up. Fixes a real bug: previously, once any
    episode of a show (or file of a movie) was tracked, the entire
    wrapping folder was masked from ever being rescanned — a new season,
    or a new file dropped alongside a tracked one, was invisible forever.
    Rename and Dedup (Movies and Series) all inherit this fix for free;
    Purge never walked the filesystem, so it needed no change.
  - `internal/naming` is a new package: a small, fixed set of on-disk
    naming presets — `Jellyfin` (default: `Title (Year) [tmdbid-N]`
    folders/files, space-separated episode names) and `Legacy` (this
    project's original dash-separated Series shape, no tag on Movies).
    Configurable per-mode via `GET/PUT /api/modes/{mode}/naming-preset`.
    Movies gets real renaming for the first time (`rename.RelocateMovie`)
    — before this, Movies' Rename only ever relocated a file, preserving
    whatever scene-release name it arrived with.
  - `naming.MatchesMovieSchema`/`MatchesSeriesSchema` are structural
    conformance checks wired into `rename.ScanLibrary`/`ScanLibrarySeries`:
    a file/folder that already matches the active preset is never
    proposed, even if it was never in `libStore` (e.g. a library someone
    already organized by hand).
  - `proposals.Proposal.Year` (new field, populated from TMDB's release
    date at Scan time) finally populates the previously-dead
    `library.Item.Year`/`library.Series.Year` columns on Apply.
  - `grabs.Grab.SeasonSpecified` (new field) fixes a real Season-0/
    Specials bug: `SeasonNumber == 0` used to be treated as "no season
    info" during Search's check-import, which silently dropped a
    deliberate Season-0 (Specials) grab whose filename didn't parse — the
    new bool distinguishes "a season was actually picked" from "none
    was," which a bare `int` can never do on its own.
