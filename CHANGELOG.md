# Changelog

This is an **append-only** project history. Once an entry is written, it is
never edited or removed — only new entries get added, at the bottom. If a
past decision turns out to be wrong or gets reversed, that reversal is its
own new entry ("X, reversing the 2026-07-09 decision to Y"), not a rewrite
of the original one. The goal is a record that survives context loss across
sessions — anyone (human or Claude) picking this file up cold should be able
to reconstruct what happened and why without re-deriving it.

For the current backlog/roadmap (as opposed to history), see `docs/ROADMAP.md`.
For house engineering conventions and mission/scope, see `CLAUDE.md`.

---

## 2026-07-08 — Initial scaffold and ported foundations

Project started as **Tidyarr**, later renamed (see 2026-07-09 entry). Initial
commits: Go server skeleton, SQLite + goose migrations, AGPL-3.0 license.
Ported `internal/servarr` (Radarr/Sonarr/Whisparr client), `internal/identify`
+ `internal/ollama` + `internal/stashapi` (the AI-assisted Adult
identification pipeline) from two prior sibling CLI projects
(`sonarr-radarr-sort`, `stash-whisparr-sort`). Added `internal/secrets`
(encrypted-at-rest) and `internal/connections` (persisted service
credentials, with real reachability checks for StashDB/FansDB/TPDB/Brave).
Confirmed Whisparr V3's actual API shape against real Whisparr-Eros source
rather than assuming.

Same day: implemented all four original review workflows end-to-end for
Movies/Series (and progressively Adult) — **Rename** (Scan→stage→Apply
against Radarr/Sonarr/Whisparr Lookup), **Purge** (allowlist-tag-based
Scan→stage→Apply), **Dedup** (quality-based duplicate grouping), **Tag**
(native tag assign/remove). Adult's own Rename/Dedup landed the same day:
Rename via the AI identification pipeline (Scan proposes, Apply carries the
resolved scene id to Whisparr); Dedup groups Whisparr scenes by `foreignId`
with graceful degradation. Unmatched Adult identifications can be given back
to TPDB/StashDB as scene drafts — a separate, explicitly human-triggered
action, not automatic.

## 2026-07-09 — Frontend, auth, Docker, rename to SAK, Movies off Radarr, Series off Sonarr

Built a real frontend (the review workflows could finally be exercised
end-to-end, not just via curl). Gated the app behind a single-operator login
with an enforced setup wizard. Added a Debian-based Dockerfile + dev loop
script. Added AI title-guess fallback for Movies/Series Rename (sharing
Adult's configured AI provider/model) and Kids/general content
classification with physical relocation, including drift reconciliation for
already-tracked items, not just new orphans.

**Renamed the project from Tidyarr to SAK Media Server** (module path,
Docker image, GitHub repo all updated to `sakms`).

Added native indexer search + grab (Prowlarr + qBittorrent/NZBGet) and a
TMDB-powered Discover browse UI — shared infrastructure across Movies and
Series, independent of any `*arr` app.

**Eliminated Radarr for Movies**: Movies gained its own library
(`internal/library`), with its own Rename/Purge/Dedup/Tag paths and its own
root-folder + quality-tier settings, no Radarr involved anywhere in the
Movies path anymore.

Added `CLAUDE.md` — the project's mission, scope, and load-bearing
engineering conventions (staged-for-approval one-item-at-a-time; secrets
encrypted at rest; single-operator auth; honesty about unverified
assumptions; house HTTP client pattern; no premature abstraction; no dead
code left behind, but don't strip still-generically-valid capability).

**Eliminated Sonarr for Series**: Series gained its own episode-aware
library (genuinely different tables from Movies' `Item` — `Series`/`Episode`,
since Series needs rows for episodes TMDB knows about but that aren't on
disk yet). Own Rename/Purge paths, own root-folder + quality-tier settings,
own episode/season-aware Search→grab→check-import. A one-time,
human-triggered importer (`internal/sonarrimport`) migrates an existing
Sonarr library by walking disk + resolving TVDB→TMDB ids, read-only against
Sonarr, safe to re-run.

**Added Series Dedup**: duplicates group by `(show TMDB id, season,
episode)` rather than a single id — the tracked copy for a key is the one
`library.Episode` row for that exact slot (the schema's own
`UNIQUE(series_id, season_number, episode_number)` constraint rules out
ambiguity), and a season-pack duplicate groups naturally with a loose
single-episode duplicate since a pack is broken into individual files before
grouping happens.

## 2026-07-10 — Stage 2c: recursive scanning, Season-0 fix, schema-aware Rename, Jellyfin/Emby naming

Four related fixes/features shipped together:

1. **`library.ScanRootFolder` made recursive** (`filepath.WalkDir` instead of
   a single-level `os.ReadDir`). Fixed a real bug: once any file in a folder
   was tracked, the *entire* wrapping folder was previously masked from ever
   being rescanned — a new season added later, or a new file dropped
   alongside something already tracked, was invisible forever. Rename and
   Dedup (Movies and Series) inherit the fix automatically. Purge never
   walked the filesystem at all, so needed no change. A directory is now
   reported whole only if it has no real subdirectories (ignoring
   bonus-content names like `Sample`/`Extras`, tracked in
   `config.ExcludedDirNames`) and no already-known direct children;
   otherwise it's opened up and recursed into.

2. **Season-0/Specials sentinel bug fixed**: `grabs.Grab` gained a
   `SeasonSpecified bool` field (migration `0014`). Previously,
   `SeasonNumber == 0` was treated as "no season info" during Search's
   check-import, which silently dropped a deliberate Season-0 (Specials)
   grab whose filename didn't parse. The fix also caught a matching frontend
   bug: `seasonNumber ? {...} : {}` made "season 0 typed deliberately" and
   "season left blank entirely" produce byte-identical wire payloads — the
   naive fix (just deleting the `== 0` check) would have been unsafe without
   also fixing this, since it would have started silently misfiling
   unidentifiable plain series-wide grabs as Season-0 episodes. Caught by
   adversarial review during planning, not after the fact.

3. **Schema-conformance filtering for Rename**: new
   `naming.MatchesMovieSchema`/`MatchesSeriesSchema` structural predicates —
   a file/folder that already matches the active naming preset is never
   re-proposed by Rename's Scan, even if it was never tracked in the
   database (e.g. a library someone already organized by hand).

4. **New `internal/naming` package**: a small, fixed set of on-disk naming
   presets — `Jellyfin` (default: `Title (Year) [tmdbid-N]` folders/files,
   space-separated episode names, matching Jellyfin/Emby's documented
   convention) and `Legacy` (this project's original dash-separated Series
   shape, no tag on Movies — an explicit opt-in so an already-renamed
   library's shape never silently changes after an upgrade). **Movies gets
   real renaming for the first time** here — before this, Movies' Rename
   only ever relocated a file, preserving whatever scene-release name it
   arrived with. Configurable per-mode via `GET/PUT
   /api/modes/{mode}/naming-preset`. `proposals.Proposal` gained a `Year`
   field (migration `0015`, populated from TMDB at Scan time), finally
   populating the previously-dead `library.Item.Year`/`library.Series.Year`
   columns on Apply.

Verified via `go build/vet/test -race` across the whole module (all green)
plus a live Playwright walkthrough proving Jellyfin-standard renaming
actually happens on disk for both Movies and Series, the naming-preset
setting persists per-mode, and — the key regression proof — a new episode
file dropped into an already-organized, already-tracked season folder is
correctly discovered on rescan.

## 2026-07-10 — Redesign discussion begins (no code shipped yet)

User shared five UI mockup images depicting a much richer dashboard-style
redesign than SAK's current lightweight single-page tab UI (sidebar nav,
system dashboard, table-driven workflows with bulk actions, poster-grid
tagging). Full description of each mockup is recorded in `docs/ROADMAP.md`
under "UI mockup reference" for durability, since the images themselves
aren't stored as files.

Decided: treat the mockups as inspiration, not a literal spec — real SAK
terminology (Movies/Series/Adult, actual workflow names), only build widgets
backed by data SAK actually has. Sequencing decided: finish the
already-in-flight Stage 2c work (above) before starting on the redesign.

Follow-up discussion ("deep-interview") reviewed 13 additional candidate
capabilities across Core Media Management, Infrastructure, Automation, and
Metadata Sourcing. Key decisions from that round:
- **Naming overhaul** (token/regex-based custom renaming): dropped from
  scope for now — user will revisit later if needed. `internal/naming`'s
  fixed-preset design (from Stage 2c, above) stands as-is.
- **Bulk apply**: decided to actually build this (a deliberate, considered
  reversal of the "no apply-everything path anywhere" principle in
  `CLAUDE.md` — needs its own design pass for partial-failure handling, not
  a casual add).
- **SSO**: forward-auth header support only (trusting a reverse-proxy-set
  identity header), not a full OIDC/SAML client — keeps SAK single-operator.
- **Network mount resiliency**: verified already safe. No workflow deletes
  anything in reaction to a missing file — Purge triggers on tag membership
  only, Dedup only removes a *detected duplicate's* loser, Rename never
  deletes. A disconnected mount just errors the scan or skips an unreadable
  subdirectory. Only gap: clearer error messaging, not a redesign.
- **Hardware acceleration (GPU)**: initially flagged as a scope mismatch
  (SAK doesn't transcode or generate thumbnails today) — then reopened with
  a concrete driver, see the phash entry below.
- **Background task queue**: not building speculatively; only if/when
  watch-folders (see Automation below) actually need it.
- **Confirmed real gaps, not yet scheduled**: confidence scoring for weak
  TMDB/community-DB matches (today `items[0]` is always taken, no
  threshold); manual override/re-pick for a misidentified match; logical
  episode-splitting (one file, multiple `Episode` rows — explicitly NOT
  physical re-encoding); TVDB/IMDB as fallback metadata sources alongside
  TMDB; local `.nfo`/artwork preference (confirmed zero support today —
  `.nfo` is purely skip-listed, never parsed); watch-folders (would only
  ever auto-run Scan, never auto-Apply — that would break the one invariant
  this whole project is built on); webhooks + real API docs (the REST API
  already *is* the extensibility surface; GraphQL explicitly rejected as an
  unnecessary rewrite); Collections (Movies-only, seeded from TMDB's
  `belongs_to_collection` — Series has no TMDB equivalent); structured
  Genre/Actor tagging (richer than today's flat per-mode tag vocabulary).

## 2026-07-10 — Phash-based duplicate detection: scope decided, split into two efforts

User: perceptual hashing (phash) should be "the defacto standard across all
media for identifying duplicates," and specifically that Adult identification
against StashDB/TPDB/FansDB should already have this (`borrowed from stash`).

**Verified, not assumed**: the claim was correct and more precise than
expected. The prior CLI this project descended from
(`stash-whisparr-sort`) had phash as the **primary, authoritative**
identification signal for Adult content — files with a phash matched via a
StashDB→FansDB→TPDB-GraphQL fingerprint cascade first, falling back to
AI/text search only for files without one yet (with a force-generate step
that triggered a targeted Stash rescan for missing phashes before falling
back). When ported into this codebase, the low-level client methods came
along verbatim (`stashbox.FindScenesByFingerprints`, `stashbox.SubmitFingerprint`,
`tpdbrest.SearchByHash`, `stashapi.StashFile.PHash`) but the *orchestration*
that made phash primary did not — today's `internal/identify.Identifier.Identify`
is pure UUID-lookup + AI-parsed-title text search + web-search grounding,
never touching a hash. The dead client methods are exercised only by their
own unit tests.

Also surfaced a subtlety while verifying: the old CLI's own code comment
claimed a 4-stage cascade (`...→TPDB-GraphQL→TPDB-REST`), but the actual
implementation only ever queried 3 stages — TPDB-REST was never part of the
fingerprint cascade, only used for AI-fallback text search. The restoration
will implement the real 3-stage cascade, not the comment's stale claim.

Also clarified: **the old CLI never computed a phash itself** — it always
read one already computed by the user's own separately-running Stash
instance, and forced Stash to compute one (via a targeted rescan) when
missing. This splits "phash as the defacto standard across all media" into
two genuinely different efforts:

1. **Adult identification** (in progress — design finalized, not yet
   implemented): restore the phash-first cascade, leaning on Stash's own
   already-computed fingerprint via a new `mode.Session.Stash *stashapi.Client`
   field (reusing the already-recognized, already-testable `"stash"`
   connection key that exists but was never wired into a live session).
   Give-back (submitting a confirmed fingerprint back to StashDB/FansDB)
   moves from Scan-time (as in the old CLI) to Apply-time, since Scan only
   ever proposes in this project — submitting to a community database based
   on an unapproved proposal would violate staged-for-approval.
2. **Movies/Series Dedup** (deferred, not yet designed in detail): there's
   no Stash instance for Movies/Series to lean on, so SAK would need to
   compute phashes itself for the first time in either codebase — real
   frame-decode work. Decided: CPU baseline by default, GPU (QuickSync/NVENC)
   as an opt-in speedup, scoped comparison to start (not full library
   all-pairs), across all three modes including Adult once available.

This is where the GPU-acceleration item from the deep-interview round
reopened: it's a real, well-motivated need for effort #2's frame decoding,
not the vague "transcoding" scope mismatch it looked like in isolation.

User also requested this changelog and `docs/ROADMAP.md` be created and
kept up going forward, given the volume of undocumented decisions
accumulating in conversation alone.

## 2026-07-10 — Adult phash-first identification restoration shipped

Implemented effort #1 from the previous entry: phash is now Adult's
**primary** identification signal, restoring (and improving on) the prior
CLI's behavior.

- **`mode.Session.Stash *stashapi.Client`** (new field): populated only when
  a `"stash"` connection exists; nil otherwise (fully additive, every other
  mode/path unaffected).
- **`identify.Identifier.LookupFingerprints`**: batched (25 phashes/request)
  StashDB→FansDB→TPDB-GraphQL fingerprint cascade — the real 3-stage
  cascade, not the old CLI's stale 4-stage comment (see previous entry). A
  box that errors or isn't configured is skipped, not fatal; the cascade
  falls through stage by stage using the *original* candidate order, not a
  shrinking one.
- **`proposals.Proposal`** gained `PHash`, `DurationSeconds`, `GiveBackBox`,
  `GiveBackSceneID`, `FingerprintSubmittedAt` (migration `0016`).
  `GiveBackBox`/`GiveBackSceneID` are captured directly from the
  identification match, not reconstructed from `ForeignID` later — a real
  ambiguity would otherwise bite here: `MatchResult.WhisparrForeignID()`
  returns the *same* raw UUID string for both a StashDB and a FansDB match
  (only TPDB gets a distinguishing `"tpdbId:"` prefix), so `ForeignID` alone
  can never say which community box a match came from.
- **`rename.Scan`** now routes Adult candidates through a new
  `scanAdultPhashFirst` orchestrator whenever `sess.Stash != nil`: batch-load
  every candidate's phash from Stash, force-generate (targeted Stash rescan,
  `scanGeneratePhashes: true`) for anything still missing one, run the
  fingerprint cascade, and fall back to the existing AI/text pipeline
  (`proposeOneAdult`) only for candidates the cascade still can't resolve.
  A cascade hit skips the AI/text pipeline entirely. Fails open to the
  legacy per-item pipeline if Stash itself is unreachable — Adult
  identification never blocks on Stash's availability.
- **Give-back moved to Apply-time**, not Scan-time as in the old CLI:
  `rename.Apply` now submits a matched proposal's fingerprint back to its
  origin community box right after registration succeeds (best-effort —
  failure never turns an otherwise-successful Apply into an error), since
  Scan only ever proposes in this project; submitting to a community
  database off an unapproved proposal would violate staged-for-approval.
  New exported `rename.SubmitFingerprintRetry` covers the case Stash's own
  phash generation is asynchronous and may still be missing at Apply time —
  a separate, human-triggered retry (mirroring `SubmitDraft`'s precedent),
  wired to `POST /api/proposals/{id}/submit-fingerprint` and a new "Give
  back fingerprint" button on Applied Adult Rename proposals with an
  unsubmitted give-back target.

Verified via `go build/vet/test -race` across the whole module (all green),
including new fake-Stash/fake-stash-box test coverage for: a cascade hit
(AI pipeline never runs), a cascade miss (falls through correctly), a
missing phash getting force-generated mid-scan and then resolving, Stash
being unreachable (fails open), the give-back-box/scene-id capture
correctness case above, Apply's give-back submission (both when it fires
and when it correctly doesn't), and the retry endpoint end-to-end.

## 2026-07-10 — phash-refined Movies Dedup

Movies Dedup no longer auto-dedupes every file sharing a TMDB id. Within
each same-TMDB group it now computes a CPU perceptual hash over several
sampled frames of each candidate and only treats two files as duplicates if
their hashes are also within a Hamming-distance threshold — a strictly MORE
conservative change: same-TMDB-but-perceptually-different files (a wrong
TMDB match, a different cut, an extras file) are kept, not removed. Series
and Adult Dedup are unchanged (deferred — see the ROADMAP entry).

This is the first Movies/Series slice of "phash as the defacto standard
across all media"; unlike Adult (which leans on Stash's own phashes), SAK
computes the hash itself here for the first time, decoding real frames.

**Algorithm — shipped Option B (released PHash), not PDQ.** Spec decision #3
named `ajdnik/imghash`'s **PDQ**. During planning that was verified against
ground truth and found unshippable as stated: imghash's latest *tagged*
release (v1.1.0) contains no PDQ — its PDQ lives only on the unreleased
`main` branch. Pinning a *deletion-gating* dedup signal to untagged upstream
cuts against the project's conservative posture, so the human confirmed
**Option B**: ship on imghash v1.1.0's released `PHash` (64 bits/frame) with
the algorithm isolated behind `internal/phash/algo.go` as a single swap
point — moving to PDQ once imghash tags a release containing it changes only
that one file plus the `Scheme` constant, nothing downstream (hashes are
compared as scheme-tagged byte composites by Hamming distance regardless of
which algorithm produced them).

- **New `internal/phash`**: an injected-runner `Hasher` mirroring
  `internal/mediainfo`'s ffprobe test seam. The real runner shells out to
  `ffprobe` once for duration, then `ffmpeg` for N (=5) evenly-spaced
  *interior* frames (deliberately avoiding head/tail intros, black frames,
  and credits), hashes each via imghash, and concatenates them into one
  scheme-tagged (`phash64/5f:<hex>`) composite. ffmpeg was already installed
  in the image for ffprobe; this is its first frame-decode use. The
  algorithm is constructed inside `Hash` (not `New`) so a future
  error-returning PDQ constructor stays isolated to `algo.go`. Hamming
  distance is a plain popcount, deliberately not imghash's own
  `similarity.Hamming` (whose raw-bits-vs-normalized return semantics
  couldn't be confirmed from its docs).
- **`library_items` gains a cached phash keyed to file identity** (path +
  size + mtime) so a tracked item is decoded once, not every Scan (migration
  `0017`, mirroring `0016`'s ALTER TABLE ADD COLUMN pattern; all
  `NOT NULL DEFAULT`, safe on a populated table — existing rows get an empty
  phash = "compute on next Scan"). `library.Store` gains `UpdatePHash` for
  the mid-scan write-back. The scheme tag embedded in the stored value makes
  a hash cached under an old algorithm/frame-count self-invalidating via
  `SimilarityWithin` (returns not-similar, never a silent wrong distance).
- **`dedup.ScanLibrary`** refines each TMDB group by phash before
  `markWinner`: it hashes each candidate (reusing the tracked item's cached
  hash when file identity + scheme still match — the decode-once win), picks
  the tracked item as the reference (else the first candidate), and drops any
  candidate outside the threshold. A group refined below 2 survivors produces
  **no proposal** (keep-both). An uncomputable candidate is dropped, matching
  `probeCandidate`'s existing tolerant posture.
- **Per-mode tunable threshold** via `GET`/`PUT
  /api/modes/{mode}/phash-threshold` (default 10 average Hamming bits/frame),
  mirroring the naming-preset settings pattern; PUT validates 0–64.
- **`proposals.Candidate` carries its phash** for display/audit (zero
  migration — candidates persist as `candidates_json`).

**Bug found and fixed during validation (verified, not assumed).** The
Phase-4 review caught a real panic: when *every* candidate in a group failed
to hash (e.g. ffmpeg missing or all files corrupt), `attachPHashes` returned
a 0-length slice and `refineByPHash` indexed `candidates[0]` unconditionally
→ index-out-of-range crash mid-Scan. Fixed with a `len < 2` guard at the top
of `refineByPHash` (return as-is; the caller's own `len < 2` check makes the
no-proposal call) plus a regression test
(`TestScanLibrary_PHashAllCandidatesUncomputable`) that drives the
whole-group-uncomputable path and asserts no panic + no proposal.

Verified via `go build/vet/test -race` across the whole module (all green),
both without and **with** `-tags integration`. Coverage includes the
`internal/phash` unit tier (fake runner, canned PNGs: determinism,
wrong-frame-count, runner-error, undecodable-frame, `SimilarityWithin`
scheme/length safety), a synthetic-image calibration test guarding the
default's separation *margin*, the `internal/dedup` refinement tier (keeps a
near-identical pair, drops a divergent orphan to no-proposal, tracked-item-
as-reference, cache-reuse-avoids-rehash, the panic regression above), and a
build-tagged real-ffmpeg integration test that generates tiny `testsrc`/
`testsrc2` lavfi clips and drives the real `Hash`. Its measured numbers
(this machine): the same clip re-decoded hashes **identically** (0 bits),
while `testsrc` vs `testsrc2` differ by **153/320 bits** — far outside the
50-bit composite budget the default (10/frame) allows. A separate full-flow
walkthrough (real ffmpeg + real ffprobe through `dedup.ScanLibrary`, fake
TMDB since synthetic clips can't match live TMDB) measured a re-encoded
near-duplicate at **6** average Hamming bits/frame (kept) and a genuinely
different same-TMDB clip at **31** (dropped): exactly one proposal holding
the tracked copy + its near-duplicate, with the perceptually-different file
correctly left out. The default of 10 sits cleanly between the two on real
ffmpeg-decoded frames — but it remains a *starting* default and a per-mode
tunable, not a value proven correct for arbitrary real-world movie frames.

## 2026-07-10 — phash-refined Series Dedup

Series Dedup no longer auto-dedupes every file resolving to the same
`(show, season, episode)`. Within each such group it now computes a CPU
perceptual hash over several sampled frames of each candidate and only treats
two files as duplicates if their hashes are also within the tunable
Hamming-distance threshold — the same strictly MORE conservative keep-both
behavior Movies shipped in the entry above: same-slot-but-perceptually-
different files (a wrong match, a different cut, an extras file) are kept, not
removed. Adult Dedup is unchanged (still deferred — see the ROADMAP entry).

**Almost pure reuse — no new phash infrastructure.** This is the notable part:
nothing in `internal/phash` changed, `refineByPHash` is reused verbatim, and
the per-mode `phash-threshold` setting/resolver/routes were already
mode-generic, so the `series_phash_dedup_threshold` key path works with zero
new wiring. The slice is the Movies mechanism pointed at episodes, not a second
implementation of it.

- **Migration `0018`** clones `0017`'s columns onto `library_episodes`, adding
  a file-identity-keyed phash cache (`phash` + `phash_file_size` +
  `phash_file_mtime`) so a tracked episode is decoded once, not every Scan; all
  `NOT NULL DEFAULT`, safe on a populated table (existing and not-yet-on-disk
  missing-episode rows get an empty phash = "compute on next Scan"). The
  missing-episode rows (`file_path = ''`) are skipped before any phash logic
  runs, so their empty default is never read. `library.Store` gains
  `UpdateEpisodePHash` — a targeted mid-scan write-back (`WHERE id = ?`) that
  caches a tracked episode's hash without ever touching its title/air_date/
  file_path — and the three fields ride through `UpsertEpisode`'s INSERT/
  CONFLICT clause and the `GetEpisode`/`ListEpisodes`/`MissingEpisodes` SELECT
  column lists.
- **`dedup.ScanLibrarySeries`** gains `hasher`+`threshold` params and refines
  each `(show, season, episode)` group before `markWinner`, via
  **`attachPHashesSeries`** — an Episode-typed sibling of `attachPHashes` with
  an identical body, differing only in the tracked type and the write-back
  method. This follows CLAUDE.md's parallel-sibling-function convention over a
  forced-shared interface: smallest blast radius, and the just-shipped Movies
  path is left completely untouched. `refineByPHash` (its `len < 2` panic guard
  included) is shared as-is — no Series variant. `ApplyLibrarySeries` persists
  the winner's phash + file identity via `UpsertEpisode`, so the next Scan
  finds it cached.
- **The Dedup Scan handler** now resolves the threshold for any library-backed
  mode and passes `hasher`+`threshold` to both `ScanLibrary` and
  `ScanLibrarySeries`, dropping the Movies-only special-case gate.
- **Season packs are orthogonal**: they're flattened into per-episode files
  (`library.ResolveEpisodeVideoFiles`) upstream of grouping, so the phash
  helpers stay pack-unaware — a pack-split duplicate refines against a loose
  single-episode duplicate on the flat candidate list with no pack-specific
  code path.

Unlike the Movies slice above, this one passed Phase-4 review with zero
blocking findings — a clean pass, no fix-cycle. Verified via `go build/vet/test
-race` across the whole module (all green), both without and **with** `-tags
integration`. Coverage mirrors the Movies refinement tier for Series (keeps a
near-identical pair, drops a divergent orphan to no-proposal, tracked-episode-
as-reference, cache-reuse-avoids-rehash, the whole-group-uncomputable panic
regression) plus a season-pack duplicate refining together with a loose
duplicate, `library_episodes` phash round-trip + `UpdateEpisodePHash` store
tests, and Series `phash-threshold` API round-trip/validation. The
`internal/phash` integration tier already proved `Hash` mode-agnostically, so
it needed no new work — only that the module still passes under `-tags
integration`.

## 2026-07-10 — Mission clarified: SAK is the sole backend, Jellyfin/Stash are players; Whisparr and Stash's organizational role will both be eliminated

Before scoping Adult phash-based Dedup (the natural next slice after Movies
and Series), asked the user to confirm what "the phash must match what
StashDB expects" actually required — the answer reframed the whole
direction, so recorded here before any code.

**Investigation first.** Researched what algorithm StashDB/FansDB's
stash-box network actually indexes under `PHASH`: a **single 64-bit** DCT
hash of a **25-frame collage** (goimagehash-style PerceptionHash), computed
by the user's local Stash instance. Confirmed this is **incompatible** with
`internal/phash` (Movies/Series' algorithm: `ajdnik/imghash` PHash over 5
separately-hashed frames, a 320-bit composite) — different library, frame
composition, and bit-length; not just differently tuned. Full research
(cited sources) preserved in `.omc/autopilot/spec-phash-dedup-adult.md`.

**Then the mission question.** The investigation's first-pass recommendation
was "Adult Dedup should read Stash's already-computed phash read-only, no
new hashing infra" — cheap and correct *if* Adult keeps depending on a live
Stash instance forever. Asking the user to confirm that assumption surfaced
that it's wrong: **the actual goal is that SAK becomes the sole backend for
file management — metadata, renaming, file placement, and deduplication —
across all three modes, with Jellyfin and Stash reduced to pure downstream
media players with zero organizational authority.** This is the same
displacement already done to Radarr (Movies) and Sonarr (Series), now
named explicitly as a mission principle rather than left implicit, and
extended: **Whisparr will eventually be eliminated for Adult too** (Adult
gets its own library-owned path, same pattern), and Stash's role as
Adult's identification bridge goes with it.

**What this changes for phash specifically.** Since Stash the *app* is
going away as a dependency, "match what StashDB expects" isn't about
reading Stash's live value — it's about SAK computing its **own** hash in
the same `PHASH` format the stash-box network (StashDB/FansDB/TPDB) already
indexes, so SAK can do fingerprint-based identification and Dedup similarity
gating **directly** against those community databases, without a local
Stash instance bridging it. One SAK-owned hasher, three eventual consumers:
Adult identification (replacing `rename`'s current Stash-read dependency),
Adult Dedup's similarity gate, and a filename-embedded phash for fast
rescans if Adult ever gets its own renaming feature (mirroring Movies'
Stage 2c naming work). This is a new, separate frame-decode path — NOT a
change to `internal/phash`, which stays exactly as shipped for Movies/Series
(they never needed StashDB compatibility and still don't).

**Recorded, not yet built.** `CLAUDE.md`'s Mission and Scope sections and
`docs/ROADMAP.md`'s phash entry were updated to capture this; the original
Adult-phash-dedup spec doc is marked superseded (its StashDB-algorithm
research stays accurate and reusable, only its recommendation changed). No
code shipped this entry — Whisparr elimination and the new hasher both need
their own Phase 0/1 design pass, not yet started.

## 2026-07-10 — internal/videophash: SAK-owned, StashDB-compatible video phash hasher

Built the SAK-owned hasher named in the previous entry — a new, fully
independent package that computes a video perceptual hash in the exact
format StashDB/FansDB's stash-box network indexes under `algorithm: "PHASH"`,
so SAK will eventually be able to identify and dedupe Adult content without
depending on a live local Stash instance. **Hasher + validation only this
slice** — deliberately NOT wired into Rename's identify path or Dedup yet
(the obvious next slice, per `docs/ROADMAP.md`).

- **Algorithm, verified against Stash's actual source** (`stashapp/stash`
  `pkg/hash/videophash`), not assumed: 25 frames sampled at
  `offset + i*stepSize` where `offset = 0.05*duration`,
  `stepSize = (0.9*duration)/25` — the middle 5%-95% of the video, no
  half-step centering — each scaled to width 160 (aspect preserved),
  composited row-major into a single 5x5 collage image, hashed via
  `goimagehash.PerceptionHash` (SAK implements none of the DCT/median/
  threshold math itself — only correct frame sampling, collage assembly, and
  output encoding). Encoded as `strconv.FormatUint(hash, 16)` — lowercase,
  **unpadded** hex, byte-identical in shape to Stash's own
  `Fingerprint.Value()`. Deliberately zero shared code with `internal/phash`
  (Movies/Series' algorithm — different library, different frame
  composition, different bit length, and it stays exactly as shipped).
- `goimagehash` pinned to `v1.1.0` (exact tag); the package doc states that
  any version bump requires re-running the live cross-validation below, the
  same self-invalidation discipline `internal/phash`'s `Scheme` tag
  documents, without adding a scheme tag (which would break byte-identity).
- **Validated, not just structurally plausible: live-cross-checked against
  a real production Stash instance (`stash.zaena.us`) using a real file from
  the user's library.** Fetched Stash's own already-computed phash via the
  existing `stashapi.Client` (the same client Adult identification already
  uses), independently computed this package's hash for the same file, and
  compared them as parsed `uint64` via Hamming distance — **byte-identical:
  Hamming distance 0/64 bits, on the first attempt.** This is the gold-
  standard proof this hasher is genuinely StashDB-compatible, not just
  algorithmically plausible. The reference-vector tier (a synthetic-clip
  fixture from Stash's own test suite) was investigated and not found —
  `pkg/hash/videophash/` ships no test file — so live cross-validation was
  the only tier available, and it succeeded outright.
- New `internal/videophash/integration_test.go` (`//go:build integration`)
  carries both the real-ffmpeg determinism check (same clip hashes
  identically twice through actual decode) and the live-Stash cross-
  validation, gated behind `SAK_STASH_URL`/`SAK_STASH_APIKEY`/
  `SAK_STASH_TEST_FILE` environment variables — `t.Skip()`s cleanly when
  unset, so CI stays green with no live dependency. No credential is
  hardcoded or written to any file; sourced at test-run time only.

Verified via `go build/vet/test -race` across the whole module (all green,
`internal/phash`/`internal/rename`/`internal/dedup` genuinely untouched —
confirmed via `git status`, not assumed) and `-tags integration` (real-ffmpeg
determinism + the live Stash cross-validation above, both passing).

## 2026-07-10 — Rejected unifying phash onto videophash; split by purpose instead

Investigated (not built) unifying all three modes' Dedup onto
`internal/videophash` and deleting `internal/phash` entirely, per an initial
"unify then remove the competing variant" request. The investigation surfaced
a real risk before any code was written: `internal/videophash` is mechanically
coarser than `internal/phash` — a single 64-bit hash of one 25-frame collage
versus `internal/phash`'s 320 bits from 5 separately-hashed frames — and
Stash's collage algorithm was tuned for adult-scene content, never validated
against arbitrary movies/TV. Because Dedup deletes the losing file, using the
coarser, unvalidated algorithm as the deletion gate would have been a real
data-loss risk, not just a maintenance simplification.

**Reversing course, not the earlier decisions**: `internal/phash` and
`internal/videophash` both stay, split by purpose rather than by mode:
- **`internal/phash`** (the higher-fidelity, SAK-only system, never needing
  external compatibility) becomes the one **Deduplication** signal across all
  three modes. Movies/Series already have it, unchanged by this decision.
  Adult Dedup will get it next — SAK computing its own hash for Adult files,
  not reading Stash's live value.
- **`internal/videophash`** (StashDB-compatible, byte-identical to Stash) stays
  reserved for **identification** only — the still-planned replacement for
  Adult Rename's Stash-read dependency. It is explicitly not a Dedup signal;
  Dedup never needed StashDB compatibility since it's a purely local
  file-vs-file comparison, a point the original Adult phash investigation
  (2026-07-10, "Mission clarified" entry) had already established but that
  got blurred when unification was first proposed.

No code changed this entry — Movies/Series required no migration, reset, or
recalibration since `internal/phash` is untouched. `docs/ROADMAP.md`'s phash
entry was rewritten to state the two-system split explicitly, replacing the
prior "wire videophash into Adult identify and Dedup" framing (which would
have put the coarser algorithm on the deletion path). The full risk analysis
is preserved in `.omc/autopilot/spec-phash-unification.md`, marked superseded
on its conclusion (unify + delete) but not its algorithm-fidelity findings
(§1), which are what prompted this course correction.
