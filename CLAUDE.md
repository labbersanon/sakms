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

## Scope: opportunistic, not a fixed roadmap

There is **no committed target list** of what SAK will eventually absorb.
Radarr is eliminated for Movies (done). Sonarr is next for Series (not
started). Whether Jellyfin, Stash, Bazarr, Tdarr, or anything else ever
gets absorbed is an open question, decided app-by-app as the pain of
running it separately becomes concrete — not decided in advance by a
roadmap. When a new consolidation opportunity comes up, engage with it on
its own merits; don't cite (or invent) a fixed end-state that includes or
excludes it a priori.

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
  per-user roles — one login gates the whole app.
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
- **Series**: still fully Sonarr-backed. Eliminating it is the next stage
  — a bigger lift than Movies was, since Sonarr's per-episode tracking
  (missing episodes, season packs) has no flat equivalent yet. Needs its
  own scoped plan: episode/season data model, TMDB season/episode
  metadata, a one-time Sonarr library importer, and a file-naming
  convention for episodes.
- **Adult (Whisparr)**: untouched, not in any near-term plan.
- **Jellyfin**: not integrated at all yet. Whether it's ever absorbed vs.
  stays a permanent separate playback layer is genuinely undecided (see
  Scope above) — don't assume either answer.
