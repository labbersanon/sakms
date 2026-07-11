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

## 2026-07-10 — Adult Dedup gets internal/phash

Implemented the first half of the purpose-split decided in the previous
entry. `dedup.scanAdult` (Servarr/Whisparr-backed, groups by `ForeignID`) no
longer auto-dedupes every candidate sharing a foreignID — it now refines
each group by perceptual similarity exactly as Movies and Series already do,
using `internal/phash` unchanged (no edits to that package; the existing,
already-validated algorithm and default threshold carry over as-is — no new
calibration pass, per explicit direction, since the algorithm itself hasn't
changed, only its third caller).

- New `attachPHashesAdult`, a sibling of `attachPHashes`/`attachPHashesSeries`
  but deliberately simpler: no cache-read, no write-back, no library-store
  parameter. Adult has no SAK-owned row to cache a hash against (unlike
  Movies' `library_items` or Series' `library_episodes`) — every Scan
  recomputes fresh. This is a genuinely smaller, honestly-scoped capability,
  not a missing feature; caching was a decode-once optimization for
  Movies/Series, never a correctness requirement.
- `scanAdult` gains the same attach→refine→keep-both-on-`<2` block Movies and
  Series already have, reusing `refineByPHash` verbatim — including its
  `len<2` panic guard from the original Movies fix, which now protects this
  third caller too. The tracked Whisparr item gets a nonzero `TrackedID` via
  `probeCandidate` exactly like Movies/Series, so `refineByPHash`'s existing
  reference-selection logic (prefer the tracked candidate) needed zero
  adjustment for Adult's Servarr-backed shape.
- Closed a real wiring gap in `internal/api/dedup.go`: Adult's Scan branch
  previously called `dedup.Scan` with neither a hasher nor a resolved
  threshold at all — the already mode-generic `resolvePHashThreshold` (used
  by Movies/Series since Series shipped) now resolves for Adult too, and the
  in-scope hasher is forwarded. `/api/modes/adult/phash-threshold` already
  worked with zero changes (the config route was built mode-generic from the
  start); this just makes Adult's Scan actually use it.
- Added a direct unit test of `refineByPHash`'s reference-selection logic
  (`TestRefineByPHash_TrackedCandidateSelectedRegardlessOfPosition`) that
  places the tracked candidate deliberately last in the slice, with a hash
  arrangement chosen so a wrong (position-based) selection produces a
  disjoint survivor set from the correct (TrackedID-based) one — every prior
  "uses tracked as reference" test (Movies, Series, Adult) happened to always
  put the tracked candidate first, so none of them could actually distinguish
  correct selection from index-0-by-coincidence. Verified this new test both
  passes against the real code and fails when the selection logic is broken
  (confirmed by temporarily disabling it and watching the test catch it,
  then restoring).

Verified via `go build/vet/test -race` across the whole module (all green,
`internal/phash`/`internal/videophash`/`internal/rename` genuinely untouched
— confirmed via `git diff --stat`) and `-tags integration`. Safety property
traced end to end: an uncomputable-hash candidate is dropped in
`attachPHashesAdult`, never enters `refineByPHash`, and can never be treated
as a match or deleted — including when the tracked reference itself is the
uncomputable one, which correctly degrades to comparing remaining orphans to
each other rather than silently matching everything.

Adult identification (replacing `rename.scanAdultPhashFirst`'s Stash-read
dependency with `internal/videophash`) remains a separate, not-yet-started
slice, per the purpose split.

## 2026-07-10 — Adult identify computes its own phash, drops live-Stash dependency

Adult phash-first identification previously read a live Stash instance's
already-computed phash (`scanAdultPhashFirst` → `sess.Stash.FindSceneInfoByPaths`)
and force-generated missing ones via a scan-job poll. It now computes its own
StashDB-compatible hash directly via `internal/videophash`, the same package
built and live-cross-validated earlier today. `identify.LookupFingerprints`
and fingerprint give-back were already phash-source-agnostic — they talk to
StashDB/FansDB/TPDB directly, never through local Stash — so this was a
contained source swap, not a rework.

Deleted `refreshMissingPhashes` and the `forceGenerate*` constants entirely:
`videophash.Hash` is synchronous, so the async force-generate/poll dance
that only existed because Stash computes phashes in the background is now
dead weight.

**Correctness fix, not just a mechanical swap.** `DurationSeconds` used to
ride in on the same Stash read as the phash. `videophash.Hash` returns only
a hash string — duration is required by fingerprint give-back, which
silently no-ops on a non-positive duration in two independent places
(`submitFingerprintGiveBack` and `GiveBack.SubmitFingerprint` itself),
neither raising an error or failing a test. Missing this would have shipped
a silent regression in a working feature. `mediainfo.Probe` gained a
`Duration float64` field (via ffprobe `-show_format`, matching videophash's
own internal duration probe rather than stream-level duration, which is
often absent on MKV) and now supplies `DurationSeconds` instead. Verified
with a dedicated end-to-end test that drives a cascade-hit proposal through
the real `rename.Apply` and asserts the submitted duration on a recording
fake give-back box — the only test that actually catches this regression,
since a bare "was PHash stamped" check does not.

New `GET|PUT /api/modes/adult/identify-enabled` toggle (default on) is now
the sole gate for Adult phash-first identification, replacing the implicit
`sess.Stash != nil` check — a real toggle didn't exist before (`Available`
in the setup wizard is computed from Whisparr connectivity, not a manual
switch; verified before assuming otherwise). Per-file compute is bounded to
4 concurrent workers, each capped by videophash's own ~2-minute internal
timeout; a hash error degrades that one candidate to the legacy AI/text
path rather than failing the whole batch — an improvement over the old
all-or-nothing Stash-read fail-open.

**Honest performance note:** this trades one batched Stash GraphQL read for
up to N local ffmpeg decodes (4x bounded). Materially slower per scan — the
accepted cost of owning identification without a Stash bridge.

`sess.Stash`, `SubmitFingerprintRetry`, `buildStashClient`, `mode.Session.Stash`,
and the `"stash"` connection type are all left in place, unmodified — they
become unreachable in practice (nothing calls them anymore) but their
removal is a deliberate, separate follow-up, not bundled here.

Verified via `go build/vet/test -race` across the whole module (all green,
`internal/phash`/`internal/videophash`/`internal/dedup` genuinely untouched)
and `-tags integration` (compiles clean; the new live-identify test —
which validates a SAK-computed hash actually resolves against a real
StashDB, not just that it matches Stash's own value — skips cleanly with
no credentials configured for this pass).

## 2026-07-10 — SubmitFingerprintRetry retired (after making it genuinely dead)

**Part 1 is the correctness fix, not the deletion.** The retry was NOT a pure
no-op: `scanAdultPhashFirst`'s fallback (cascade-miss + text-match) proposals
discarded the already-computed local phash/duration, so give-back silently
no-op'd at Apply for text-matched Adult scenes — `SubmitFingerprintRetry` was
their only recovery. `scanAdultPhashFirst` now stamps the local
phash/duration onto EVERY hashed candidate's proposal, cascade hit or
legacy/text fallback alike, so give-back fires at Apply, Stash-free. The
fail-open (cascade-lookup-error) path now also carries the local phash.
Output order changed from "cascade hits first, then legacy fallbacks" to
candidate-index order (still fully deterministic).

**Only then Part 2:** removed `SubmitFingerprintRetry`, its
`/submit-fingerprint` route + handler, and the frontend "Give back
fingerprint" button/JS — genuinely unreachable once give-back fires at
Apply.

**Accepted residual (explicit, not buried):** give-back at Apply fires for a
text match only when BOTH the local hash AND probe succeed
(`submitFingerprintGiveBack` gates on `PHash != "" && DurationSeconds > 0`).
A file SAK cannot hash, or can hash but not probe (duration 0), that only
text-matches loses fingerprint give-back entirely — previously recoverable
via the retry's live-Stash read. Accepted: an unhashable/unprobeable file is
a strong corruption signal, not worth a Stash dependency. This is NOT "all
text matches now give back" — it's "text matches whose file also hashed and
probed cleanly."

**Retained, deliberately:** `internal/stashapi`, `sess.Stash`,
`buildStashClient`, `mode.Session.Stash`, the `"stash"` connection type, and
`testStash` are KEPT — repurposed from "identification data source"
(retired) to the upcoming **player-rescan-notify** feature (SAK triggers a
targeted Stash rescan whenever it updates a file, so a downstream player's
index stays fresh). They are written-but-not-read after this slice ON
PURPOSE; a future "no dead code" pass must not delete them.

Also deleted the now-orphaned `fakeStash`/`newFakeStash`/`sceneJSON` test
fixtures and the five `TestSubmitFingerprintRetry_*` tests. The
player-rescan-notify slice will reintroduce a Stash fake tailored to its own
ScanPaths/WaitJob API surface — this is intentional, not a loss.

Verified via `go build/vet/test -race` across the whole module (all green)
and `-tags integration` (compiles clean, skips with no live env). Grep
confirms zero remaining references to `SubmitFingerprintRetry`,
`submitFingerprintHandler`, `/submit-fingerprint`, or `submitFingerprint()`
outside this note; `sess.Stash` now shows only `mode.go`'s write and the
retained `mode_test.go` reads.

## 2026-07-10 — Add internal/jellyfin client + "jellyfin" connection type (Slice 1 of player-rescan-notify)

New `internal/jellyfin` package: a minimal REST client (`Config`/`Client`/`New`,
house HTTP-client pattern — hand-built requests via `internal/httpx`'s
`DoJSON`/`DoJSONAllowEmpty`, no interfaces) exposing `NotifyMediaUpdated`
(`POST {base}/Library/Media/Updated`, fire-and-forget, 204 expected) and `Ping`
(`GET {base}/System/Info`). Auth is the `Authorization: MediaBrowser
Token="<key>"` header. **HONESTY NOTE, carried into the package doc:** the
request/response shapes are modeled from Jellyfin's master source
(`LibraryController.PostUpdatedMedia`, `SystemController.GetSystemInfo`), not
confirmed against a live instance — `System/Info` was chosen over the
unauthenticated `System/Info/Public` specifically because it actually
exercises the API key.

Wired a `"jellyfin"` connection type end to end for Settings' Test Connection
flow only: `TestConnection` dispatch, `testJellyfin` (mirrors `testOllama`),
and the frontend's `CONNECTION_SERVICES` array (its render loop already
treats every service generically — URL + API key fields, no per-service
casing needed).

**This slice is standalone and inert** — a user can add/test a Jellyfin
connection in Settings today, but nothing in SAK calls
`NotifyMediaUpdated` yet. The actual notify-on-Apply wiring
(`internal/mode.Session.NotifyPlayers` and its call sites) lands in later
slices of the same feature.

Verified via `go build/vet/test -race` across the whole module (all green)
and `-tags integration` (compiles clean).

## 2026-07-10 — Add player-notify foundation: PathChange, Session.NotifyPlayers, phash-free RescanPaths (Slice 2 of player-rescan-notify)

`internal/mode` gains the contract every later slice of this feature builds
on: `ChangeKind` (`Created`/`Modified`/`Deleted`) and `PathChange{Path,
Kind}` — one file-level change a workflow's Apply committed to disk — plus
`Session.Jellyfin *jellyfin.Client`, populated ONLY for Movies/Series (a new
`buildJellyfinClient`, symmetric to the existing Adult-only
`buildStashClient`, wired into `Build` in a new `if m != Adult` block).
`buildStashClient`'s existing Adult-only scoping is untouched — this is the
hardcoded per-mode scoping confirmed in `CLAUDE.md`'s Mission section: Stash
is notified only for Adult, Jellyfin only for Movies/Series, via which
client field is non-nil, no cross-notification, no toggle.

`internal/stashapi.ScanPaths` is refactored into a shared private
`scanPaths(ctx, paths, rescan, generatePhashes bool)` core, with `ScanPaths`
unchanged (`generatePhashes:true`, same public signature, same test) and a
new sibling `RescanPaths(ctx, paths)` (`rescan:false, generatePhashes:false`)
for the player-notify path, which only needs Stash to notice a file changed
— SAK computes its own StashDB-compatible phash now, so asking Stash to also
generate one on every notify would be redundant work. Sibling function over
a bool param on `ScanPaths`, per house "no premature abstraction" — zero
production callers meant the refactor was free.

`Session.NotifyPlayers(ctx, changes []mode.PathChange)` is nil-safe and
NEVER returns an error — every failure path is log-only, since a player
being unreachable must not fail SAK's own Apply, which has already
committed by the time this runs. It routes Stash `Deleted` paths to
`CleanMetadata(ctx, paths, false)` and `Created`/`Modified` paths to the new
phash-free `RescanPaths` — **never crossed**, the single most important
correctness guardrail in the whole feature (a purge-shaped batch must never
look like a scan to Stash) — and fires Jellyfin's `NotifyMediaUpdated` for
all kinds in one POST. No `WaitJob` either way: fire-and-forget. Derives its
8-second timeout from `context.WithoutCancel(ctx)` rather than `ctx`
directly, so a committed change still gets notified even if the HTTP request
that triggered the Apply disconnects mid-request — cheap insurance inside
the best-effort envelope, not a correctness requirement.

Tests (`internal/mode`, httptest fakes): Jellyfin POST shape (path, auth
header, decoded body); the Stash scan-vs-clean split, explicitly asserting a
Deleted-only batch produces zero `metadataScan` calls; a rename-shaped batch
scans the new path (`scanGeneratePhashes:false`, proving `RescanPaths` and
not `ScanPaths` fired) and cleans the old one; both-clients-nil and
empty-changes no-ops; an exact-path assertion that the fake never receives a
`RootFolderPath`-shaped key; best-effort — a 500 from the fake Jellyfin
still returns (void) and logs; and the cross-arm independence guard — a
rename-shaped batch whose `metadataScan` 500s still fires `metadataClean`,
so a scan failure on the new path never leaves the old path's Stash record
uncleaned.

This slice is still inert in practice — nothing calls `NotifyPlayers` yet.
The Movies/Series and Adult Apply call sites land in the next two slices.

Verified via `go build/vet/test -race` across the whole module (all green).

## 2026-07-10 — Notify Jellyfin on Movies/Series Apply: rename/purge/dedup (Slice 3 of player-rescan-notify)

Wires Slice 2's `mode.PathChange`/`Session.NotifyPlayers` contract into the
six Movies/Series library-backed Apply functions
(`rename.ApplyLibrary`/`ApplyLibrarySeries`, `purge.ApplyLibrary`/
`ApplyLibrarySeries`, `dedup.ApplyLibrary`/`ApplyLibrarySeries`), each now
returning `[]mode.PathChange` via a named return alongside their existing
id/err. `internal/api.applyByWorkflow` gains a single `changes`
accumulator and a deferred `sess.NotifyPlayers(ctx, changes)` call, so every
one of Movies/Series' six call sites funnels through one notify site instead
of each wiring it separately.

Path precision follows the verified call-site table exactly: Movies rename's
Deleted side is the *resolved video file* (`library.ResolveVideoFile(p.SourcePath)`),
not `p.SourcePath` itself (which can be a wrapping directory) — Series
rename's Deleted side is `p.SourcePath` directly, since Series' Apply never
has that directory indirection. This asymmetry between the two Apply
functions is intentional, not a bug. Both rename functions additionally
guard against RelocateMovie/RelocateEpisode's own self-collision no-op (a
file already sitting at its preset-computed destination): when the returned
destination equals the source, nothing moved, so no PathChange is emitted —
avoiding a bogus Deleted+Created pair for an unchanged path.

Purge and Dedup only ever emit Deleted entries, each guarded against an
empty `FilePath`/candidate path so a library row with no file on disk can
never produce a bogus notify. Series purge reports every removed episode
file in one batch (N deletes, not just the first). Dedup's Movies path
required widening `removeLibraryCandidate`'s signature to `(string, error)`
so the *exact* removed path is captured — the tracked-loser branch returns
the library item's own `FilePath` (looked up fresh via `libStore.Get`), not
the proposal's (possibly stale, scan-time) candidate path; the untracked
branch returns `c.Path`. Series dedup's inline loop, lacking that same
lookup indirection, emits `c.Path` directly per the verified table — winners
never move, so they never appear; `keepAll` removes nothing and always
returns nil changes.

Partial-success discipline (Critic fix #1/#2 from planning): every dispatch
call site receives the Apply function's returned changes into a fresh local
and assigns the outer accumulator with plain `=`, never `:=` — a `:=` here
would shadow the deferred closure's accumulator. Every one of the six Apply
functions uses named returns for `changes` so a post-mutation failure (e.g.
`libStore.Upsert` erroring right after a successful file move) still reports
the change that physically committed — notify fires on whatever actually
landed on disk, then the original error still propagates to the caller.

Tests (`internal/rename`, `internal/purge`, `internal/dedup` unit level;
`internal/api` end-to-end against a fake Jellyfin server and the real HTTP
dispatch): exact-path assertions for both rename asymmetries; the
no-physical-move guard; Series purge's N-deletes-in-one-batch; Dedup's
tracked-loser-uses-library-FilePath-not-candidate-path distinction (a
dedicated test with a deliberately stale candidate path); `keepAll` →
zero `PathChange`s and zero notify calls; a collision-renamed destination
(pre-occupied path forces `place.UniquePath`'s `.2`-suffix fallback) →
notify reports the actual returned path, never the originally intended one;
best-effort — a 500 from the fake Jellyfin still leaves the proposal
Applied; and a scoping check — a Movies Apply with a `"stash"` connection
fully configured sends zero requests to it, since `sess.Stash` stays nil
outside Adult mode (hardcoded per-mode scoping, unchanged from Slice 2).

Adult's three Apply functions (rows 3/6/9) are unwired still — that's Slice
4, which also needs the partial-success mechanism landed here to actually
matter (row 3's `Add`-fails-after-a-successful-`Relocate` sub-case).

Verified via `go build/vet/test -race` across the whole module (all green,
including `-tags integration`).

## 2026-07-10 — Notify Stash on Adult Apply: rename/purge/dedup (Slice 4 of player-rescan-notify)

Wires Slice 2's `mode.PathChange`/`Session.NotifyPlayers` contract into the
three Adult Servarr-backed (Whisparr) Apply functions — `rename.Apply`,
`purge.Apply`, `dedup.Apply` — the counterpart to Slice 3's Movies/Series
wiring, notifying Stash instead of Jellyfin. Scoping is hardcoded and needs
no extra branching: `sess.Jellyfin` is nil for Adult and `sess.Stash` is nil
for Movies/Series, so `NotifyPlayers`'s existing nil-checks route correctly
on their own.

`rename.Apply` now returns `changes []mode.PathChange` alongside its
existing `trackedID`/`fingerprintSubmitted`/`err`. Nothing is emitted on the
`p.TrackedID != 0` reclassify early-return (SAK does no local `os.Rename`
there) or when the relocate branch is skipped (source and destination
already share a root — no move, Stash already has the file where it is).
When the relocate branch *does* run, `changes` is captured **immediately
after `Relocate` succeeds, unconditionally** — not gated on the subsequent
`Add`/`ScanForDownloaded` calls, or on `trackedID` ending up nonzero. This
was flagged during planning (Critic fix #3) as the slice's highest-risk
detail: a naive "only report changes when the whole function succeeds"
implementation would silently drop the notify for the sub-case where
`Relocate` succeeds but `Add` itself then fails — the file has genuinely
moved on disk, `trackedID` stays 0 so the proposal is correctly left
Pending (unchanged pre-existing behavior), but without this fix Stash would
never be told the file moved, leaving a phantom/stale scene with no
corresponding SAK record to reconcile it later. Both partial-success
sub-cases are covered end to end: `Add` failing after a successful
`Relocate` (proposal stays Pending, Stash still notified) and
`ScanForDownloaded` failing after a successful `Add` (proposal still gets
`MarkApplied`, per the pre-existing partial-success design — Stash is now
*also* notified).

`purge.Apply` now returns `changes []mode.PathChange`, appending
`{p.SourcePath, Deleted}` after `DeleteTracked` succeeds, guarded against an
empty `SourcePath` the same way Slice 3's library-backed purge paths are.
`p.SourcePath` is the Whisparr-tracked path by construction — set directly
from the same Whisparr record `DeleteTracked` acts on — so it cannot
disagree with what Whisparr itself deleted.

`dedup.Apply` now returns `changes []mode.PathChange`, appending
`{c.Path, Deleted}` per removed loser inside the removal loop, after
`removeCandidate` for that candidate succeeds — a mid-loop removal failure
still reports whatever was actually deleted before the error propagates.
Both `removeCandidate` branches (tracked-via-Whisparr `DeleteTracked` and
untracked-via-`os.Remove`) key off the candidate's own `c.Path` here, unlike
Slice 3's Movies dedup path, which needed a library lookup for the exact
tracked-loser path instead. The survivor never moves, so it never appears;
`keepAll` removes nothing and always returns nil changes.

`internal/api.applyByWorkflow`'s three Adult dispatch call sites (rename,
purge, dedup) feed the same `changes` accumulator Slice 3 introduced, using
the same fresh-local-then-plain-`=`-assign discipline (Critic fix #1) to
avoid shadowing the deferred `sess.NotifyPlayers` closure's accumulator.

Tests (`internal/rename`, `internal/purge`, `internal/dedup` unit level;
`internal/api` end-to-end against fake Whisparr and Stash GraphQL servers
and the real HTTP dispatch): Adult rename with a dir-change → Stash
phash-free scan of the actual moved-to path plus a clean of the vacated
source; Adult rename with no dir-change → zero Stash calls; **both
partial-success sub-cases** (`Add` fails after a successful `Relocate`;
`ScanForDownloaded` fails after a successful `Add`) → the file is
confirmed to have actually moved on disk in both cases, and Stash is
notified in both cases, with the pre-existing MarkApplied/Pending behavior
otherwise unchanged; Adult purge → a single clean of `p.SourcePath`, no
scan; Adult dedup loser → a single clean of the tracked loser's `c.Path`,
`keepAll` → zero calls; a scoping check the mirror image of Slice 3's — an
Adult Apply with a `"jellyfin"` connection fully configured sends zero
requests to it, since `sess.Jellyfin` stays nil for Adult mode; and
confirmation that the existing Whisparr `ScanForDownloaded` scan-trigger
behavior is unchanged (still fires once per successful registration, in
both the rename and dedup happy paths).

Verified via `go build/vet/test -race` across the whole module (all green,
including `-tags integration`).

## 2026-07-10 — Notify configured player on grab-import: search.go (Slice 5 of player-rescan-notify, added post-Critic)

The Critic flagged that `internal/api/search.go`'s `checkImportHandler` — a
completed download landing in the library via `rename.Relocate` — is a
file-relocate event of the exact same category as the 9 call sites already
wired in Slices 3/4, but fell outside the spec's literal 9-row table (it's
grab-import, not one of Rename/Purge/Dedup's Apply functions). User
confirmed: add it now as a 5th notify site rather than deferring it.

Investigation confirmed at execution time (not settled during planning):
`checkImportHandler`'s `sess` comes from `mode.Build(ctx, …, g.Mode)` —
the identical codepath `applyByWorkflow` already uses — so `sess.Jellyfin`/
`sess.Stash` are populated with the same hardcoded per-mode scoping as
every other call site; no divergent session-building exists here. Grab-import
reaches Adult too, via the `switch g.Mode`'s `default` branch (only Movies
and Series get explicit cases) — Whisparr registration + scan-trigger, same
as today, now followed by a Stash notify.

The investigation also surfaced a nuance the plan's literal "notify with
`movedPath`" description didn't anticipate: `rename.Relocate` moves
`contentPath`'s whole tree, so its return value can be a wrapping
*directory* (proven by the pre-existing
`TestCheckImportHandler_QBittorrentCompleted_PerformsImport` test, whose
download completes as a directory). Sending a directory to Jellyfin's
exact-path `Library/Media/Updated` endpoint would defeat the point of the
notify. This is the same "actual file, not the wrapping directory"
discipline Slice 3's row 1 already established for `rename.ApplyLibrary`
(`library.ResolveVideoFile`), so it's applied the same way here: Movies
notifies with the resolved `videoPath` (post-`library.ResolveVideoFile`);
Series notifies with one Created `PathChange` per resolved episode file
that's actually upserted (unparsed/untracked files, which `continue` out of
the loop, are correctly excluded); Adult (the `default` branch) notifies
with `movedPath` directly, since no per-file resolution happens in that
branch at all — Whisparr owns file placement — and Stash's `RescanPaths`
scans directory trees fine, unlike Jellyfin's exact-file-path contract.

`sess.NotifyPlayers` is called once per import, after the per-mode switch's
library/Servarr writes succeed — not immediately after `Relocate`, as the
plan's literal wording suggested — because the exact notify path only
exists post-resolution (Movies/Series) or post-registration in spirit
(Adult, matching the existing branch shape). A deliberate scope decision:
unlike Slice 4's Critic fix #3 (Adult rename's `changes` captured
unconditionally, even across a later `Add`/`ScanForDownloaded` failure),
this handler makes no attempt to notify on a partial failure — every branch
still `http.Error`s and returns immediately on any error, exactly as
before this slice, and `checkImportHandler` has no `MarkApplied`-despite-
error partial-success state the way `applyByWorkflow` does to build on. If
Relocate itself fails, nothing is committed and nothing is emitted,
matching the plan's stated contract exactly.

Tests (`internal/api`, reusing Slice 3/4's `fakeJellyfin`/`fakeStash`/
`fakeAdultServarr` harnesses against the real HTTP dispatch): a completed
Movies grab-import notifies Jellyfin with exactly one Created `PathChange`
for the resolved video file (not the wrapping download directory); a failed
`Relocate` (source vanished) produces zero notify calls and a non-200
response; a fake Jellyfin 500 still leaves the grab reporting `Imported`
with a 200 (best-effort, Guardrail #1's counterpart for this call site);
and a completed Adult grab-import (through the `default` switch branch)
notifies Stash with a phash-free `RescanPaths` of `movedPath` and zero
`metadataClean` calls, proving both that this codepath applies to Adult and
that the Created-only shape holds (no old path to mark Deleted — a
grab-import is always a brand-new file, never a move of something
previously tracked).

Verified via `go build/vet/test -race` across the whole module (all green).

This is the last planned slice of player-rescan-notify — all 5 call-site
categories (Rename/Purge/Dedup × Movies/Series/Adult, plus grab-import) are
now wired.

## 2026-07-10 — player-rescan-notify: Phase 4 fix-up (dedup DB-failure gap, Stash-move honesty note)

Two findings from the independent Phase 4 review of the player-rescan-notify
feature (Slices 1-5, `b0a93e7`..`b2cc6d1`), applied before pushing:

1. **Fixed a real gap**: `dedup.removeLibraryCandidate`'s tracked-loser
   branch discarded the just-removed file's path when the subsequent
   `libStore.Delete` (DB row removal) failed — even though `os.Remove` had
   already committed. `ApplyLibrary` (Movies Dedup) would then drop that
   iteration's `PathChange` from `changes` entirely, so a rare
   remove-succeeds-then-DB-delete-fails case would silently leave a phantom
   entry in any notified player. This was the one place in the whole
   feature that didn't follow the "capture at the point the os-level
   mutation lands, regardless of what fails afterward" rule (Slice 4's
   Critic fix #3) that `purge.ApplyLibrary` and Series dedup already
   followed correctly. Fixed both `removeLibraryCandidate` (returns
   `removedPath` alongside a `Delete` error, not `""`) and its caller
   (appends to `changes` before checking `err`, not after). New regression
   test `TestApplyLibrary_TrackedLoserDBDeleteFails_StillReportsPhysicalDeletion`
   forces the DB failure by dropping the `library_tags` table mid-test (no
   mock `Store` needed) and asserts the physical deletion still surfaces —
   verified by reverting the fix and confirming the test fails
   (`got []`) before restoring it.
2. **Documented a previously-unflagged assumption**: `Session.NotifyPlayers`'s
   doc comment now states, per the house "unverified assumptions"
   convention, that a move's `RescanPaths(new)` + `CleanMetadata(old)`
   sequencing is modeled from Stash's own `CleanMetadata` doc, not
   confirmed against a live Stash instance — mirroring the honesty note
   `internal/jellyfin` already carried for the Jellyfin side of the same
   convention, which had been correctly flagged in Slice 1 but had no
   Stash-side counterpart until now.

Verified via `go build/vet/test -race` across the whole module (all green).
Both fixes are inside the feature's existing best-effort envelope — neither
changes what Apply itself reports to its caller, only what the player
learns about after the fact.

## 2026-07-10 — PUID/PGID Docker support

The image bakes in uid/gid 1000 for the `sakms` user; a bind-mounted
`/data` owned by a different host uid previously had no way to line up
except accepting `docker-entrypoint.sh`'s existing `chown -R` re-owning the
mount to 1000 regardless of what the host side actually used.

`docker-entrypoint.sh` now reads `PUID`/`PGID` (both default `1000`,
matching today's baked-in ids — fully backward compatible with no env vars
set) and re-maps the `sakms` user/group to them via `groupmod -o -g` /
`usermod -o -u` before the existing `chown -R` + `gosu` drop-privileges
step. No new package needed — `usermod`/`groupmod` ship in the same
`passwd` package `useradd` already relies on at image build time.
Documented in `README.md`'s Docker section with a usage example.

**Not live-tested** against a real `docker build`/`docker run` in this
session — the sandboxed dev environment's Docker daemon socket required
`sudo`, which wasn't used for a routine dev-tooling change without being
asked. Verified via `sh -n` (clean) and a manual trace of the id-remap +
unconditional-`chown -R`-afterward logic instead. Per this project's own
script-verification convention, treat this as unverified end-to-end until
confirmed with `./scripts/docker-dev.sh` (or a manual `docker run -e
PUID=... -e PGID=...`) against a real container.

## 2026-07-10 — API-key auth (X-Api-Key), additive to session login

Any `/api/...` route now accepts either the existing session cookie or a
new `X-Api-Key: <key>` header, so an out-of-process client (a script, a
test harness) can call SAK without carrying a browser session. This is
additive — `Authenticated` (cookie verification) is byte-for-byte
unchanged, and `/healthz` + `/api/auth/*` stay exactly as public as
before.

**Honest framing of what this actually buys (the point isn't "the API
couldn't be scripted before"):** a script could already authenticate today
by `POST`ing `/api/auth/login` and reusing the resulting `Set-Cookie` —
that path was never blocked. The real value of a dedicated API key is (a)
keeping the master password out of scripts entirely — a leaked script no
longer means a leaked login credential, (b) independent rotation — a key
can be regenerated without touching the session password, and (c)
avoiding session-cookie lifecycle/expiry in a long-running unattended
script. It is not "the API previously had no way to be scripted."

**Boot model:** `SAKMS_API_KEY`, if set, is hashed in memory and used for
the lifetime of the process — never persisted, since it's supplied fresh
by whoever sets the env var on every boot (and SAK's own server1
deployment wipes its DB roughly every 15 minutes, so persisting it would
be pointless). If unset, SAK reuses whatever key hash is already
persisted in Settings; if none exists yet (first boot ever), it
auto-generates one, logs the full raw value exactly once, and persists
only its SHA-256 hash (`crypto/subtle.ConstantTimeCompare` at verify time,
never a plain `==`) plus a last-4 suffix for masked display. The key is
managed from Settings → API Access — status (masked, source: env /
settings / none), Generate/Regenerate, and a one-time full-key reveal
with a copy affordance. Regenerating while `SAKMS_API_KEY` is active is
refused with 409 (env precedence would make a freshly regenerated
settings key a silent no-op, and it would be discarded again on the next
boot anyway) — the UI disables the button in that state instead of
sending a request that's certain to fail.

**Operational tradeoff, stated plainly:** on an auto-generated (no
`SAKMS_API_KEY`) deployment, the generated key appears in full in
container/stdout logs until it's rotated — confirmed live against the
real binary during backend development, not just inferred from the code.
Anyone with log access during that window has the key; rotate it (or set
`SAKMS_API_KEY` instead) if that log surface isn't trusted.

**Critic minor finding #1 (documented, not re-planned):** removing
`SAKMS_API_KEY` after it was once set does **not** fall back to "no key"
— it reactivates whatever key hash was last persisted in the settings KV
(e.g. an earlier auto-generated key from before the env var was ever
introduced). Env-set always wins verification precedence over the
persisted settings hash while it's active, but settings persistence
itself is untouched during that time, so unsetting the env var falls back
to that stale persisted hash rather than to nothing. This is a real
operational surprise on a deployment with a persistent database — it
cannot manifest on the server1 target, whose ~15-minute auto-wipe means
no stale persisted hash ever survives that long, but it's worth stating
plainly rather than leaving as a silent gotcha. It is not a
double-credential bug (only one hash is ever active at a time verifying
against `X-Api-Key`), just a "which key" surprise. See README's
`SAKMS_API_KEY` row for the matching operator-facing note.

Backend: `internal/auth/apikey.go` (new — key generation, hashing,
verify, status), `internal/auth/session.go`'s `Middleware` (cookie first,
falls back to the header, fails closed with a 500 on a genuine
settings-store read error rather than ever falling through to allow),
`internal/api/apikey.go` (new, session-protected `GET /api/apikey` +
`POST /api/apikey/regenerate` on their own dedicated mux — kept out of
`NewMux`, whose 20 existing test call sites and "stays unaware auth
exists" convention are both preserved untouched), `internal/config`
(`SAKMS_API_KEY` read), `cmd/sakms/main.go` (boot wiring). Frontend: a
new "API Access" fieldset in Settings (`internal/web/static/index.html`).
Full `go build/vet/test -race` green throughout, plus live end-to-end
verification against the real binary for all three boot paths (fresh
auto-generate, reuse-on-restart, `SAKMS_API_KEY` precedence).

## 2026-07-10 — API-key auth: Phase 4 fix-up

Independent Phase 4 review (architect/security/code-reviewer, all three
APPROVE, zero blocking findings) surfaced two real-but-minor issues in
`POST /api/apikey/regenerate`, applied before push:

1. `Regenerate` re-read `APIKeyStatus` after persisting the new key, purely
   to obtain its suffix for the response. If that unrelated second read
   failed, the handler returned 500 and discarded the raw key — which is
   shown exactly once and unrecoverable — even though rotation had already
   succeeded and the old key was already dead. `Regenerate` now derives and
   returns the suffix directly from the raw key it just generated (it
   already computes the same value internally via `persistKey`), so a
   successful rotation can never be lost to a later, unrelated read error.
2. The status and regenerate handlers echoed raw internal error strings
   (e.g. settings-store failure detail) back to the client instead of a
   generic message — inconsistent with `Middleware`'s own
   `"authentication error"` posture for the same class of failure. Both
   now log the detail server-side and return a generic `"internal error"`.

Also noted, not code-changed (an operational/deployment observation, not a
bug in this app): on any host that ships container stdout to a central log
store, the auto-generated boot key (logged in plaintext exactly once, by
design) becomes a retained, searchable credential there rather than an
ephemeral console line — already covered by the "shown once" tradeoff
disclosed in the entry above, but worth restating plainly: prefer setting
`SAKMS_API_KEY` explicitly on any log-shipping deployment rather than
relying on the auto-generate-then-rotate path.

Verified via `go build/vet/test -race` across the whole module (all green).

## 2026-07-11 — Four-mode auth strategy switch (password/forward/authentik/none)

A human-directed, net-new feature — not a pre-existing item anywhere in
`docs/ROADMAP.md`'s backlog (same framing as the PUID/PGID entry above:
described here on its own terms, not as a checkbox off an existing list).
Auth is now chosen at first-run and switchable later from Settings,
across four strategies, all built on ONE mode-aware `Middleware` that
reads `auth_mode` per request and fails closed on any read error:

1. **`password`** — today's session-cookie login, unchanged behavior for
   every existing install (regression-verified).
2. **`forward`** (new) — trusts a reverse-proxy-set identity header,
   gated by a shared secret header the proxy must also present
   (`subtle.ConstantTimeCompare` against a stored hash — never a plain
   `==`). The identity header's value is cosmetic (logging only); the
   secret header is the entire authorization decision.
3. **`authentik`** (new) — validates a presented `Authorization: Bearer`
   token via RFC 7662 introspection against an Authentik OAuth2 provider.
   SAK is an API-client bearer-token *validator* here, not an OIDC
   client — no redirect flow, no JWKS, no browser-mediated login. Any
   introspection error, timeout, or `active:false` fails closed to 401.
4. **`none`** (new) — no auth at all, gated by a mandatory
   `acknowledgeInsecure:true` flag at both the point it's chosen (setup)
   and the point it's switched into later (`PUT /api/auth/mode`), plus a
   persistent warning banner in the UI while active.

**The universal `X-Api-Key` decision — a deliberate, informed reversal of
the original Analyst spec, not an oversight.** The spec's default
recommendation was to scope `X-Api-Key` to `password` mode only, so that
`forward` and `authentik` modes could offer a clean "only reachable
through the proxy" / "only reachable with a valid bearer token" trust
boundary. The operator was shown this tradeoff explicitly and chose the
opposite: `X-Api-Key` is accepted in **all four modes**, checked in
`Middleware`'s own top-level body (not inside any per-mode helper), for
uniform out-of-process/script access regardless of which mode is active.
This is a real, accepted narrowing of `forward`/`authentik` mode's trust
boundary — a caller who obtains the API key can reach every protected
route without ever touching the proxy or holding a real Authentik token
— documented here plainly rather than left as a silent gap. The
`SAKMS_API_KEY` environment-variable key reconciles the same way: it now
works in every mode too (reversing spec Edge Case #6, which assumed the
old mode-1-only key scoping), verified with a real cross-mode Middleware
test (`TestMiddleware_EnvAPIKeyUniversal_AcrossModes`, slice 5) that
exercises the env-supplied key against both `none` and `forward` as the
active mode, not just asserted in prose.

**The `authentik` mode UI-scope correction — a UI-surface decision, not a
capability reduction.** The first-run/create-user screen offers only
THREE mode options (`password`/`forward`/`none`); `authentik` was
deliberately dropped from that specific screen by a later human decision
recorded mid-slice-4, after the user's initial mental model for
"Authentik mode" (which turned out to already be `forward` — Authentik
doing its own login+2FA upstream at the proxy) was clarified against what
`authentik` mode actually is (SAK as its own independent OAuth2 client
introspecting a bearer token an API/script caller already holds). Once
clarified, the user confirmed they still want `authentik` mode exactly as
built — they just don't want it cluttering a browser setup wizard whose
natural audience (a script/API client) never uses that screen anyway.
`authentik` remains a fully-built, fully-tested backend mode from slices
1-3 with zero scope reduction: reachable via the Settings panel's
four-way mode selector (post-setup, once some initial credential already
exists to authenticate the switch), or via a direct `POST /api/auth/setup`
call with `mode:"authentik"` at first run, which the backend still
accepts unchanged — only the setup screen's `<select>` narrows.

**The status-endpoint amplification fix — a deliberate, scoped narrowing,
not a silent deviation from the spec's AC7.** `/api/auth/status`'s
`authenticated` field means a REAL, per-request check for `password`
(cookie), `forward` (the same constant-time secret compare `Middleware`
itself uses — cheap, purely local, no amplification concern), and `none`
(always true, nothing to check). For `authentik` specifically, it is
**presence-only**: `true` if a non-empty `Authorization: Bearer <token>`
header is present on the status request, without ever calling the real
RFC 7662 introspection endpoint. This is a deliberate narrowing to avoid
letting an unauthenticated caller trigger unbounded outbound
introspection calls against the operator's own Authentik instance simply
by hitting the public status endpoint with an `Authorization: Bearer
<anything>` header once per request — a real amplification vector an
attacker fully controls the rate of. Real enforcement is unchanged and
happens on every actual protected API call via `Middleware`, which does
call the real, fully-enforced `AuthentikAuth` (with real introspection)
per request as designed; a present-but-invalid token surfaces as a normal
401 on the app's first real API call, no new error-handling path needed.
Proven, not just asserted: `TestStatus_AuthentikMode_PresenceOnly_
NeverIntrospects` injects a fake introspection server that fails the test
outright if it receives any request at all.

**The `Configured()` redefinition — an instance-takeover guard, not just
UX.** `Configured` used to key off `auth_username` presence alone.
Redefining it as "`auth_mode` is set" ALONE (the naive reading of the
spec) would have been a real security regression: every pre-existing
install has `auth_username` set but no `auth_mode` row (that setting
didn't exist before this feature), so a naive redefinition would report
`Configured=false` for every existing install — re-showing "Create your
login" on every boot AND, critically, making the setup handler's 409
already-configured guard stop firing, opening a window for an
unauthenticated visitor to re-POST `/api/auth/setup` and overwrite the
real operator's credentials. The definition actually shipped is the
migration-safe OR: `Configured = auth_mode is set OR auth_username is
set`. A brand-new install is ungated until either is written; an existing
password-only install is (and stays) gated, with its effective mode
correctly defaulting to `password`; a first run that picks
`forward`/`authentik`/`none` gates immediately once `auth_mode` is
written. `TestConfigured_ExistingUsernameOnlyInstall_StillTrue` is the
regression test for this exact scenario.

**The Authentik introspection endpoint's verification status — stated
plainly, per this project's established honesty convention (see the
Jellyfin client's own package-doc caveat earlier this session).** The
introspection path (`/application/o/introspect/`) and the RFC 7662
form-body client-auth method were confirmed against Authentik's official
OAuth2-provider documentation (fetched 2026-07-10) — they are **NOT**
verified against a live Authentik instance. A wrong path or auth method
fails closed (the introspection request errors, the caller denies), so
this is a correctness-of-claim issue, not a live security gap — but it is
an unverified assumption and is documented as one in
`internal/authentik`'s package doc, not presented as confirmed fact.

Backend: `internal/auth/{auth,session,forward,authentik}.go`,
`internal/authentik/client.go` (new house HTTP client, mirrors
`internal/jellyfin`'s shape), `internal/api/{auth,authmode,forward,
authentik}.go`, `cmd/sakms/main.go` wiring (the single additive
`auth.New(settingsStore, secretStore, outboundHTTP)` signature change
slice 3 needed for Authentik's introspection client + decryptor, `auth.
Middleware`'s own signature is unchanged). Frontend:
`internal/web/static/index.html` (mode selector at setup, all-four-mode
Settings panel, mode-relative `boot()` gating, persistent `none`-mode
banner). Full design/decision detail, including the spec deviations
above, lives in `.omc/plans/autopilot-impl-auth-mode-switch.md`.

`go build/vet/test -race` across the whole module: all green. Combined
with slice 4's manual browser verification, every item in the spec's
nine "Missing Acceptance Criteria" and seven "Edge Cases" now has
coverage per the plan's §5.4 matrix — with two honest exceptions, stated
plainly rather than folded into a blanket "all covered" claim: Edge Case
#5 (SPA re-`boot()` on a mid-session mode switch) is UI behavior with no
Go test harness for `index.html` in this repo, verified manually in
slice 4, not by this slice's automated suite; and AC9's "secrets never
appear in logs" half is enforced by code review + the reveal-once/
not-in-response tests (secrets are never placed on a code path that
logs them), not by a test that scans log output for absence — the same
scope the plan's own G6 row describes. JWKS-local token validation,
considered for `authentik` mode,
was explicitly deferred (spec §4) and is not built; noted in
`docs/ROADMAP.md`.

## 2026-07-11 — Four-mode auth strategy switch: Phase 4 fix-up

Independent Phase 4 review — 12 reviewers (architect/security/code-reviewer
× dedicated passes for slices 1/2/3, consolidated for 4/5), unanimous
APPROVE across every group, zero blocking findings anywhere. Six small,
well-scoped issues surfaced across the reviews, applied before push:

1. **Raw internal error strings leaked to clients** (`internal/api/authmode.go`,
   slice 1) — the new `GET`/`PUT /api/auth/mode` handlers echoed
   `err.Error()` straight into 500 responses, inconsistent with the
   `apikey.go` house pattern (log server-side, return a generic
   `"internal error"` body) already established in this session's earlier
   API-key-auth Phase 4 fix-up. Fixed identically here.
2. **No minimum length on an operator-supplied forward secret**
   (`internal/api/auth.go`, slice 2, MEDIUM) — the auto-generated default
   is 32 bytes `crypto/rand`, but an operator-supplied secret at first-run
   setup only got `strings.TrimSpace`, silently accepting e.g. a
   one-character value that would trivially defeat forward mode's entire
   authorization gate. Now rejects anything under 16 characters (400),
   tested by `TestSetup_ForwardTooShortSecretRejected`.
3. **Missing operator-facing deployment guidance** (`internal/web/static/index.html`,
   slice 2, LOW) — neither the first-run setup screen nor the Settings
   panel stated the deployment requirement that the reverse proxy must
   **set** (overwrite), not append or pass through, the secret header on
   every forwarded request. Added to both UI surfaces.
4. **Missing wiring-protection regression test** (`internal/api/forward_test.go`,
   slice 2) — the forward mux's tests all exercised `NewForwardMux`
   unwrapped, so nothing regression-guarded "this must stay behind
   `auth.Middleware`" for the single highest-stakes route in the slice
   (`POST /api/auth/forward/secret`, which mints/reveals a fresh bypass
   credential) — a precedent (`TestAuthModeMux_ProtectedByMiddleware`)
   already existed for the parallel mode-mux and slice 2 simply didn't
   have its own copy. Added `TestForwardMux_ProtectedByMiddleware`,
   mirroring that precedent exactly. The wiring itself was never
   vulnerable (verified correct by the architect review); this only closes
   a future-regression gap.
5. **Case-sensitive "Bearer" scheme matching** (`internal/auth/session.go` /
   `internal/api/auth.go`, slice 3) — `strings.TrimPrefix(header, "Bearer ")`
   only matched an exact-case prefix; RFC 7235 §2.1 auth-scheme names are
   case-insensitive, so a client sending a lowercase `bearer` scheme would
   have its token silently treated as absent. Always failed closed (deny,
   never a security bug) but was a real interop nit. Extracted a shared
   `auth.BearerToken(r)` helper (case-insensitive scheme match) and pointed
   both `AuthentikAuth`'s real check and the status handler's presence-only
   check at the same function, so there's exactly one implementation of
   "what counts as a bearer token" rather than two copies that could drift.
6. **Two stale placeholder tests passing for the wrong reason**
   (`TestSetup_AuthentikPlaceholderRejected`, `TestPutMode_AuthentikNotAvailableYet_400`,
   slice 3, MEDIUM) — both dated from slice 1's "authentik mode is a 400
   placeholder" era and kept passing after slice 3 replaced that
   placeholder with real handling, but for an entirely different, unstated
   reason (missing required fields / no configured credentials, not "mode
   not selectable yet") — a misleading-test-intent hazard the "no dead
   code / no fake completion" convention specifically guards against.
   Removed; both scenarios are already covered by correctly-named slice-3
   tests (`TestSetup_AuthentikMissingFields_400`,
   `TestPutMode_AuthentikWithoutCreds_400`).

Verified via `go build/vet/test -race` across the whole module (all green,
fresh testcache). None of the six items were security-blocking on their
own — every reviewer's verdict was APPROVE or APPROVE WITH NOTES, and the
two amplification/instance-takeover properties the whole feature exists to
guarantee (the authentik status endpoint never introspects; an
unauthenticated request can never reconfigure an already-configured
instance) were independently re-verified clean by every security reviewer
across all four review groups.

---

## 2026-07-11 — First-run setup: proxy-header auto-detect, Skip, and forward-mode break-glass key

**Fix for a real lockout that happened today.** An operator selected forward
auth at first-run, but the reverse proxy was never configured to send the
secret header — an immediate, unrecoverable-without-server-access lockout
(the setup screen never reappears once configured, and forward mode has no
interactive login fallback). Two changes close the gap:

1. **Break-glass API key (the actual fix).** A forward-mode first-run now mints
   and reveals a one-time API key alongside the forward secret, same
   reveal-once discipline. A locked-out operator can present it as `X-Api-Key`
   to reach Settings and fix the proxy or switch modes. When `SAKMS_API_KEY`
   is set, no key is minted (env precedence would make it a no-op); the
   response instead points at the env value. Recovery guidance also added to
   the forward "Not authenticated" screen.
   - **Implementation note (deviation from the drafted spec):** the spec said
     call `EnsureAPIKey`, but boot (`cmd/sakms/main.go:92`) already persists a
     key on first start, so `EnsureAPIKey` would return `""` and reveal
     nothing. `Regenerate` is used instead — the only mechanism that can hand
     back a working raw key, since the boot key's raw value is never stored.
     This **invalidates the boot-logged key**; acceptable because forward
     first-run fires seconds after boot on a fresh instance and that key only
     ever hit stdout.
   - **Ordering is load-bearing:** the break-glass mint runs BEFORE
     `SetAuthMode` commits, not after. A mint failure after the mode commit
     would leave the instance `Configured()==true` with neither the forward
     secret nor a break-glass key ever revealed — the exact unrecoverable
     lockout this feature exists to prevent, reintroduced via a rare DB
     error. `TestSetup_ForwardMintFailure_LeavesUnconfigured` proves a forced
     mint failure still leaves `Configured()==false` for a clean retry.
2. **Proxy-header auto-detect (convenience, NOT the fix).** When the first-run
   request carries a recognized reverse-proxy identity header
   (`Remote-User`/`X-Remote-User`/`X-Forwarded-User`/`X-authentik-username`),
   the wizard PRE-SELECTS forward mode (dropdown default only — never
   auto-submits, freely changeable). Surfaced via a `!configured`-gated
   `proxyHeadersDetected` on `GET /api/auth/status`. A "Skip" button offers a
   one-click `none`-mode path reusing the exact existing `acknowledgeInsecure`
   guardrail and warning copy — no new endpoint.

   **Residual risk (honest):** detection only smooths mode *selection* — it
   cannot verify the proxy will actually send the secret header correctly.
   Detection is not a mitigation for the lockout; the break-glass key is. A
   spoofed identity header can at most flip a first-run dropdown default; no
   authorization path ever reads these headers.

## 2026-07-11 — Fix: "Copy" button in first-run setup submitted the form and wiped the reveal panel

**A real live incident, diagnosed via CDP against the deployed instance.** After the
break-glass-key fix above shipped and was deployed, an operator reported the "Copy"
button breaking the setup flow. Root cause: `internal/web/static/index.html`'s
forward-mode reveal panel (the forward secret, the break-glass API key, their two
"Copy" buttons, and the "I've copied it — continue" button) is appended as a child
of the setup `<form>` element. None of those four buttons had an explicit `type`
attribute — and a `<button>` with no `type` defaults to `type="submit"` **when it's
a descendant of a `<form>`**, per the HTML spec. Clicking "Copy" therefore also
submitted the form, re-running `submit()` a second time: its first line
(`reveal.innerHTML = ""`) wiped the entire reveal panel — the secret, the
break-glass key, and the continue button — the instant Copy was clicked, followed
by a 409 ("a login is already configured") since the mode had already committed on
the first successful submit. The operator was left with no visible way to finish
setup or recover the credentials they were trying to copy.

Confirmed via CDP against a local instance both ways: reverted the fix and
reproduced the exact reported symptom (`readonlyInputCountAfterCopy: 0`, all reveal
buttons gone, `"a login is already configured"` error) driving the actual browser
DOM through the real setup flow — then restored the fix and confirmed all reveal
elements survive a Copy click.

**Fix:** added `type: "button"` to all three previously-untyped buttons in the
reveal panel (both "Copy" buttons and "I've copied it — continue"). The existing
"Skip" button already had this correctly set (it was explicitly specced), which is
what made the omission on the other three read as an oversight rather than a
design choice — confirmed by reproducing the bug, not just inferring it from the
code.

**Scope note:** the two other "Copy" buttons in this codebase (`renderAPIAccess`'s
API-key panel, `renderAuthMode`'s Settings forward-secret panel) are NOT inside any
`<form>` element — verified `renderSettings` never wraps its fieldsets in a form —
so they were never affected and needed no change.

Verified via `go build/vet/test -race` (all green) and the live CDP
reproduce-then-fix cycle above, not just a syntax check.

---

## 2026-07-11 — Auth: replace `forward` + `authentik` modes with a real OIDC flow

Collapsed the four auth strategies down to three — **`password`, `oidc`,
`none`** — deleting both `forward` (reverse-proxy shared-secret) and
`authentik` (RFC 7662 bearer-token introspection) modes outright, not
deprecating them in place.

**Why both modes were wrong for this use case.** `forward` mode trusted
reverse-proxy-injected headers (`Remote-User` + a shared `X-Proxy-Secret`),
which forced a live secret to be smuggled into the reverse proxy's config —
in direct conflict with the deployment's "no plaintext secret in any proxy
config file" policy — and, worse, the proxy-secret model isn't even what
Authentik/Authelia themselves recommend: their real forward-auth model is
header-stripping plus network isolation, with no shared secret at all.
`authentik` mode was RFC 7662 introspection only: built for API/machine
clients that already hold a token, it was never a real browser login (its own
package doc said "SAK never becomes an OIDC client of Authentik — no
redirect/callback flow, no JWKS"). Neither was a genuine, provider-agnostic,
cryptographically-verified browser login.

**What replaced them.** A single real **OpenID Connect Authorization Code flow
with PKCE**, where SAK is the Relying Party (new `internal/oidcauth`, built on
`github.com/coreos/go-oidc/v3` for discovery + JWKS-backed ID-token
verification and `golang.org/x/oauth2` for the code exchange — no hand-rolled
JWT/JWKS validation). This is provider-agnostic (any OIDC IdP, not just
Authentik), and the ID token is verified by signature against the IdP's
published JWKS, with issuer/audience/expiry/nonce all checked — a real
cryptographic gate, not a trusted header. It needs **no** proxy-held secret.

**Single-operator model unchanged.** Successfully completing the IdP login
(valid ID token) IS the one operator authenticating — there is no
subject-allowlist step, exactly as `forward`/`authentik` never checked
identity either (a forward identity header and an Authentik `sub` were always
cosmetic, never the authorization gate). Restricting *who* may complete the
IdP's login screen is the IdP's own Application/Provider policy job, not SAK's.
After a successful callback, SAK issues the SAME signed session cookie password
mode uses, so every ongoing per-request check is identical to password mode's —
no new middleware path.

**Shape.** New public redirect legs `GET /api/auth/oidc/login` (mints
state + nonce + PKCE verifier into a short-lived, HttpOnly, Secure,
SameSite=Lax flow cookie scoped to `/api/auth/oidc`, then redirects to the
IdP) and `GET /api/auth/oidc/callback` (verifies state, exchanges the code with
the PKCE verifier, verifies the ID token + nonce, issues the session cookie,
redirects to `/`) — both public by necessity, since the whole point is to
establish a session where none exists. The redirect URL is an explicit,
operator-supplied setting (never derived from the spoofable request Host).
Post-setup config moves to a session-protected `GET/PUT /api/auth/oidc`
(replacing the deleted `/api/auth/forward*` and `/api/auth/authentik` routes).
The one-time break-glass API-key mechanism carries over unchanged (oidc
first-run has no interactive-login fallback at setup time either).

**Deleted, not kept as dead code:** `internal/authentik/` (the introspection
client), `internal/auth/forward.go` (forward-secret storage/verify),
`internal/auth/proxydetect.go` (its only consumer was the wizard's
now-gone forward pre-select), `internal/api/forward.go`,
`internal/api/authentik.go`, and the `proxyHeadersDetected` status field.
`internal/auth/session.go`'s `ForwardAuth`/`AuthentikAuth`/`BearerToken` are
gone; the mode switch now routes `oidc` through the same cookie check as
`password`. Frontend: the wizard's forward reveal-once panel and the
Settings-panel forward/authentik config groups are replaced with OIDC
issuer/client-id/client-secret/redirect-URL forms, and the old dead-end
"not authenticated" proxy notice becomes an actionable **"Log in with SSO"**
button.

Verified via `go build ./... && go vet ./... && go test ./...` — all green
except a pre-existing, unrelated `internal/grabs` ordering test
(`TestList_ScopedByModeAndOrderedNewestFirst`) that ties on sub-millisecond
`created_at` values and is untouched by this change. New OIDC tests stand up a
minimal in-process IdP (discovery + JWKS + token endpoint, RS256 ID tokens
signed by hand) and cover the full happy path plus state-mismatch,
nonce-mismatch, expired-token, bad-signature, wrong-audience, and
missing-flow-cookie rejections, and the mode-switch preconditions.

**Same-day follow-up: independent security review + 4 hardening fixes.**
A `security-reviewer` pass (separate from the implementing agent, per house
policy — auth code doesn't get self-approved) traced all 10 adversarial
checks (CSRF/state, PKCE, nonce, flow-cookie handling, ID-token verification,
session issuance, routing precedence, secret handling, open-redirect,
fail-closed) against the actual code, not the implementer's comments.
Verdict: 0 critical/high findings — the cryptographic and authorization core
was correct as shipped. Four lower-severity findings were fixed the same day:
1. **(Medium) Unauthenticated discovery-fetch DoS** — `OIDCClient` performed
   a live OIDC discovery (well-known + JWKS) fetch on every call, reachable
   from the public, unauthenticated `/api/auth/oidc/login` route — looping
   that endpoint could flood the configured IdP. Fixed by memoizing the
   discovered `*oidcauth.Client` on `auth.Store`, keyed by a fingerprint of
   the four config fields (`internal/auth/oidc.go`'s `oidcFingerprint`); a
   config change naturally invalidates the cache via a fingerprint mismatch,
   no separate invalidation call needed.
2. **(Low) Session cookie missing `Secure`** — `SetSessionCookie` gained a
   `secure bool` parameter. Password mode still passes `false` (preserves
   the documented plain-HTTP LAN use case); the OIDC callback passes `true`
   unconditionally, since a redirect URL an external IdP can reach is, in
   every real deployment, already HTTPS.
3. **(Low) Flow-cookie TTL was browser-enforced only** — `oidcFlowState`
   gained an `IssuedAt` field; the callback now rejects a flow older than
   `oidcFlowTTL` server-side, not just via the cookie's own `Expires`/`MaxAge`.
4. **(Low) Empty state/nonce/verifier not explicitly rejected** — the
   callback now rejects a degenerate (any-field-empty) flow cookie outright
   instead of relying on the state-compare or the IdP exchange to catch it
   incidentally.

Re-verified after the fixes: `gofmt -l` clean, `go build ./...` /
`go vet ./...` clean, full `go test ./...` green (including the previously-tied
`internal/grabs` test, which passed cleanly on this run).

## 2026-07-11 — Clearer mount-disconnect error messaging

Closed the "Cheap, independent wins" ROADMAP item confirmed safe on
2026-07-10 (no workflow deletes anything on a missing file — this was
purely an error-message clarity gap, not a safety one).

Previously, a network mount dropping mid-scan (a CIFS/NFS disconnect, an
unmounted drive) surfaced as a raw, unhelpful `WalkDir`/`os.Stat` error —
e.g. `scanning /mnt/Media-NAS/Movies: reading /mnt/Media-NAS/Movies: lstat
/mnt/Media-NAS/Movies: no such file or directory` — straight through to the
operator via the Scan API's `502` error body, with no indication of what
actually went wrong or what to do about it.

`library.ScanRootFolder` gained a single classification point
(`classifyScanErr`) at its one error-return site: when the underlying error
looks like the root itself became unreachable
(`fs.ErrNotExist`/`syscall.ENOTCONN`/`ESTALE`/`EIO`/`EHOSTUNREACH` — the
overwhelmingly common real causes of a scan aborting outright), the
returned error becomes `root folder unreadable — check that <path> is
still mounted and reachable`, still wrapping the original OS error via `%w`
so `errors.Is` and logs retain the raw detail. All four Scan call sites
(`rename.ScanLibrary`/`ScanLibrarySeries`, `dedup.ScanLibrary`/
`ScanLibrarySeries`) inherit the clearer message for free through their
existing `fmt.Errorf("scanning %s: %w", ...)` wraps around
`ScanRootFolder`'s return — no per-call-site changes needed.

New tests: `TestScanRootFolder_MissingRootGivesActionableMountMessage`
confirms both the actionable message text and that `errors.Is(err,
fs.ErrNotExist)` still holds through the wrap. A same-day `code-reviewer`
pass (separate from the authoring context, per house policy) flagged that
the four network/mount errnos the feature is actually named for
(`ENOTCONN`/`ESTALE`/`EIO`/`EHOSTUNREACH`) had no direct coverage — closed
with `TestClassifyScanErr_MountDisconnectErrnosGetActionableMessage`
(table-driven over synthetic `*fs.PathError` values) and
`TestClassifyScanErr_OtherErrorsStayGeneric` (confirms an unrelated errno
like `EACCES` does NOT get the mount-specific wording, while still
preserving `errors.Is`). Review verdict: 0 blocking issues, approved as-is
even before the added coverage — the extra tests are precision, not a fix.

Verified via `gofmt -l` (clean), `go build ./...` / `go vet ./...` (clean),
and full `go test ./...` (all green).
