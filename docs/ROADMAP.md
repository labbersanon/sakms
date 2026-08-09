# Roadmap / planned development

This is the living backlog — what's being considered, decided, in progress,
or deferred, and why. Unlike `CHANGELOG.md` (append-only history), this file
gets edited as priorities shift: move an item between sections, update its
status, refine its scope. Keep entries here in sync with reality — if a
decision here turns out stale, fix it here rather than letting this file
drift from what's actually true. For historical record of how a decision
was reached, put that in `CHANGELOG.md` instead and just link/reference it
briefly here.

---

## Known issues

### SeasonEpisodePicker grabs the wrong season — added 2026-08-01, queued
Reported by Wade: picking Season 4 in the season/episode picker grabs S1E1
instead. Not yet diagnosed — root cause unknown. **This is a bug-fix task,
not a feature — per CLAUDE.md's Execution Mode Rules it routes through
Ralph (troubleshooting), not deep-interview.**

**CORRECTED same day — the redesign is no longer folded into this fix.**
Wade initially asked for the picker redesign to be folded into this same
Ralph task, then explicitly asked for a separate deep-interview on the
redesign instead, superseding that. The redesign now has its own spec:
`.omc/specs/deep-interview-season-episode-picker-redesign.md` (spec ready,
~10% ambiguity, PASSED — scope grew substantially: real TMDB season/episode
enumeration with poster+still drill-down, a new general-purpose TMDB
response cache, and an amendment to the shipped bulk-grab feature's
per-item counting semantics). **This Ralph task is now scoped to the
season-mismatch bug diagnosis/fix only** — do not also redesign the UI as
part of it; that's the separate spec's job.

**Scope note**: `SeasonEpisodePicker` is shared by both the inline card grab
flow and `DetailPopup`'s Series gating step (see
`.omc/specs/deep-interview-discover-card-cleanup.md`, spec ready, same day)
— a fix here affects both call sites, no separate fix needed per
call site. Wade confirmed the bug fix, the cleanup spec, and the redesign
spec are three independent work items with no forced ordering between
them.

**UPDATED 2026-08-02 — the redesign shipped; THIS BUG DID NOT, and must not
be marked fixed on the strength of it.** Two things in the note above are now
stale. (1) The picker no longer lives in `Mainstream.tsx` — it is its own
`frontend/src/screens/discover/SeasonEpisodePicker.tsx`, and there are SIX
mount points, not the two "both call sites" implies (see the shipped entry
below). Anything planning against the old export must re-verify. (2) The
redesign's spec is no longer just "spec ready" — it is built and shipped.

**The season-mismatch bug remains open and still belongs to Ralph.** Replacing
free-text inputs with a grid may *mask* it (if the root cause was the
free-text parse) or leave it fully intact (if it is in release scoring or
check-import) — neither was diagnosed, and nothing in the redesign was allowed
to touch it. If it now appears fixed, that is an observation to hand to the
Ralph task, not a claim to make: silently absorbing a bug fix into a feature
is exactly what the correction above exists to prevent.

**FIXED 2026-08-03 — root cause was diagnosed and confirmed as TWO
independent bugs, both now closed.** This entry stays for historical
record per this file's own append-vs-edit convention; nothing above is
deleted.

1. **Prowlarr wire-format bug (the actual "grabs S1E1" trigger)**:
   `internal/prowlarr/client.go`'s `SearchByID` sent `tvdbid`/`season`/`ep`/
   `tmdbid`/`imdbid` as top-level HTTP query parameters. Prowlarr's
   `/api/v1/search` endpoint (confirmed against Prowlarr's own source,
   `QueryToParams`) never reads those top-level params at all for an
   ID-scoped search — it only recognizes brace tokens embedded inside the
   `query` parameter itself (e.g. `query={TvdbId:81189} {Season:4}`). So
   every "Season 4" request was, on the wire, an unscoped title-only search
   that could return releases from any season — S1E1 included. Fixed by
   rewriting `SearchByID` to build brace tokens (`{TmdbId:}`/`{ImdbId:}`/
   `{TvdbId:}`/`{Season:}`/`{Episode:}`) into `query` alongside the title
   text (keeping the existing single-word-title fix), and adding
   `SeasonSpecified bool` to `SearchByIDParams` so a deliberate Season 0/
   Specials pick can still be distinguished from "no season picked."
2. **No season-scope filter on returned releases**: even with the wire
   format fixed, Prowlarr aggregates many indexers and some fall back to
   loose title-only matching for a season-scoped search, so a wrong-season
   release could still come back mixed in with correct ones — and nothing
   downstream (title/language filtering, bitrate scoring) had any season
   awareness to catch it. Fixed by adding `FilterSeasonScope` (`internal/
   api/releasematch.go`), wired into both `RunAutoGrab`
   (`internal/api/autograb_shared.go`) and the Discover availability
   handler (`internal/api/discover_availability.go`) — drops any release
   whose title carries a recognizable, conflicting season/episode marker
   before it ever reaches scoring.

Regression coverage: `internal/prowlarr/client_test.go` (wire format),
`internal/api/releasematch_test.go` (`FilterSeasonScope` unit cases), and
`internal/api/autograb_handler_test.go`'s
`TestAutoGrabHandler_Series_WrongSeasonReleaseNeverWins` (full handler
end-to-end, fake Prowlarr returning both S01E01 and S04E05, proving S01E01
never wins). Known residual risk: Season 0/Specials pick-then-grab wasn't
part of the original bug report and has no dedicated end-to-end regression
test beyond `SeasonSpecified`'s unit-level plumbing — worth a follow-up
proof if Specials grabs are ever reported as still wrong.

**CORRECTED 2026-08-03 (same day) — batch path was initially missed.**
Architect review found `grabOneBatchItem` (`internal/api/autograb_batch.go`)
reimplements score-and-dispatch without calling `RunAutoGrab`, so the first
`FilterSeasonScope` wiring left Discover bulk select (`BulkBar` →
`autoGrabBatch`) unfiltered even though per-item `seasonSpecified: true` was
already plumbed. Closed by wiring `FilterSeasonScope` into `grabOneBatchItem`
and adding `TestAutoGrabBatch_WrongSeasonReleaseNeverWins`. The FIXED claim
above is now accurate for single-item auto-grab, Discover availability, AND
bulk batch.

## In progress

### Node CPU governor — backend + node-side enforcement shipped; slider, server-side reporting, and production verification still open
Real cgroup-v2 CPU ceiling for `cmd/sakms-node`'s worker daemon, so an
operator can cap how much of a node's total CPU the phash/videophash ffmpeg
fan-out is allowed to use — a genuine kernel-enforced hard limit, not a
soft dispatch-time throttle. Full design/research record:
`.omc/plans/node-resource-governor.md`.

**Scope decided (2026-07-22, operator-confirmed) — CPU-only, no GPU
governor of any kind. This is settled, not an open question; do not
re-research or re-litigate GPU throttling feasibility for this workload.**
GPU throttling was researched concretely against the actual hardware/
workload (NVDEC hardware video **frame decode** on a consumer RTX 4070,
proprietary NVIDIA driver — the node's *only* GPU use; the scale filter,
PNG encode, and DCT all run on CPU) and every candidate mechanism was
rejected:
- **NVDEC fixed-function decode has no throttle mechanism at all.** It's
  a fixed-function hardware block; the driver exposes no percentage,
  rate-limit, or quota control for decode throughput. There is no lever
  to turn.
- **`nvidia-smi -pl` power-limit throttling is whole-GPU, not
  per-process.** It requires root (a privilege regression on a daemon
  just hardened to run non-root) and would throttle every GPU consumer
  on the machine — the operator's own desktop compositor, browser,
  games — not just the hasher. wade-pc, the box with the RTX 4070, is
  the operator's own daily-driver desktop.
- **NVIDIA MPS thread-percentage partitioning governs CUDA *compute* SM
  allocation.** This workload has no CUDA compute kernel (decode-only),
  so MPS has essentially no effect on it, on top of requiring a
  system-wide architectural adoption to use at all.
- No Linux DRM-cgroup path exists either (NVIDIA's proprietary driver
  doesn't participate in the kernel DRM cgroup controller the way
  amdgpu/i915 do), and MIG hardware partitioning is datacenter-only
  (A100/H100) — not available on consumer GeForce.

Full mechanism-by-mechanism writeup, kept as the durable rationale:
`.omc/plans/node-resource-governor.md` § "GPU Feasibility Findings."

**Shipped (backend + node daemon):** the per-node settings column/DTO/SSE
wire path (`cpuCapPercent`, migration `0041_node_cpu_cap.sql`, mirrors the
existing `MaxJobs`/`pause_dispatch` pattern exactly) and the node-side
enforcement mechanism itself (`cmd/sakms-node/resourcegov.go`) — Option C
from the plan: `Delegate=yes` on `sakms-node.service` plus a self-managed
leaf cgroup whose `cpu.max` the **non-root** `sakms-node` daemon writes
directly (no polkit, no D-Bus). The daemon moves its own PID into the leaf
at startup, so every ffmpeg it forks inherits the cgroup automatically; a
cap change is one `cpu.max` write, live, with no daemon restart. `0%` means
unlimited, mirroring `MaxJobs`. An empirical spike on wade-pc confirmed an
unprivileged `cpu.max` write in the delegated subtree both succeeds and
actually throttles real CPU load — the mechanism is proven on this host,
not merely assumed to work from systemd's design.

**Still open (next slices) — none of this is operator-facing or deployed
yet:**
- **No slider exists.** The Nodes settings modal has only a one-line fix
  so an untouched Save no longer zeroes a stored `cpuCapPercent` — there
  is no `%` input in the UI. An operator cannot configure this today.
- **Enforcement/last-apply status isn't reported back to the server
  yet.** The node's own `GET /status` knows its enforcement state, but
  nothing carries it over the existing heartbeat, so
  `apidto.NodeInfo.Enforcement`/`CPUCapApply` are permanently zero-value
  server-side. A slider can't honestly render an "unavailable" or "not
  currently enforced: <reason>" state until this lands.
- **No production-load measurement yet.** The mechanism is proven on a
  spike load; the load-bearing E2E (cap at 50%/10%/0% against a real
  high-file-count Adult videophash scan — the only path that reaches the
  ~4-worker × 4-frame ≈16-way concurrent-ffmpeg steady state — measured
  via `systemd-cgtop`/`cpu.stat`, plus a blast-radius check and a
  live-adjust-mid-scan check) has not been run. That stage is also a
  deploy gate requiring explicit operator go-ahead before pushing/
  deploying.
- Nothing described above as "shipped" is committed or deployed as of
  this writing.

**Known, separate gap this plan does NOT fix — `MaxJobs` still isn't
enforced.** `cmd/sakms-node/main.go`'s dispatch loop spawns a goroutine
per job; `cfg.MaxJobs` is read only to log it, never to actually bound
concurrency. Real concurrency on a node is two hardcoded 4s an operator
can't see from the UI — 4 concurrent Adult-scan workers server-side
(`internal/rename/rename_adult_phash.go`'s `adultHashWorkers`) × 4
concurrent frame extractions per job node-side
(`internal/videophash/hwaccel.go`) — not whatever `MaxJobs` is set to.
The CPU governor caps that whole ~16-way fan-out in aggregate (a real
ceiling regardless of this gap), but it is not a substitute for fixing
`MaxJobs` itself, and the two remain independent, currently-unreconciled
limits on the same node. Fixing `MaxJobs` enforcement is its own,
not-yet-scheduled follow-up — out of scope for the CPU governor work.

### phash-based Dedup — refinement + phash-primary grouping shipped; PDQ migration open
<!-- Claude 2026-08-04: heading corrected. Reason: this heading said
     phash-primary grouping was "still open," but its own section body
     (below) documents it as shipped 2026-07-18 in 50dd970 — a stale heading
     contradicted by its own text. See
     .omc/plans/autopilot-impl-phash-grouping.md §0 for the full trace.
     Troubleshooting: n/a — docs-only staleness, no code defect.
     Review if: never — this heading now matches the body it introduces. -->
<!-- Original stale heading, kept per house rule (never delete, comment with
     a reason): "phash-based Dedup — Movies/Series/Adult refinement shipped;
     phash-primary grouping still open" -->
The other half of "phash as the defacto standard across all media." Unlike
Adult, there's no Stash instance for Movies/Series to lean on — SAK computes
perceptual hashes itself (real frame-decode work via ffmpeg).

**Shipped (2026-07-10):** the first slice — Movies-only, CPU-only, phash as a
**refinement WITHIN** the existing same-TMDB grouping. `internal/phash`
(injected-ffmpeg-runner `Hasher`, scheme-tagged composite over 5 sampled
frames), a file-identity-keyed cache (migration `0017`), a per-mode tunable
threshold (`GET`/`PUT /api/modes/{mode}/phash-threshold`, default 10), and
`dedup.ScanLibrary` dropping any same-TMDB candidate outside the threshold of
the group's reference. Validated with a build-tagged real-ffmpeg integration
test + a full-flow walkthrough (see the CHANGELOG entry of the same date for
the measured Hamming numbers). Ships imghash's released **PHash**, not PDQ —
see "PDQ is still pending" below.

**Shipped (2026-07-10): Series** — extended the same refine-within-identifier
approach to `dedup.ScanLibrarySeries` (group key `(show, season, episode)`):
migration `0018` adds the episode phash cache, `attachPHashesSeries` is an
Episode-typed sibling of `attachPHashes`, `refineByPHash` and the per-mode
threshold are reused verbatim, and the API handler is un-gated to pass
`hasher`+`threshold` for any library-backed mode. Season packs need no special
handling (flattened per-episode upstream of grouping).

**Shipped (2026-07-10): `internal/videophash`, the SAK-owned StashDB-compatible
hasher.** A fully independent sibling of `internal/phash` (zero shared code —
different algorithm, different consumers; `internal/phash` is unaffected and
stays exactly as shipped for Movies/Series). Computes the exact `PHASH`
algorithm StashDB/FansDB's stash-box network indexes: 25-frame 5x5 collage,
`goimagehash.PerceptionHash`, unpadded hex encoding — verified against
Stash's actual source, not assumed. **Live-cross-validated against a real
production Stash instance (`stash.zaena.us`) and a real library file: Hamming
distance 0/64 bits — byte-identical, on the first attempt.** See the
CHANGELOG entry of the same date for the full validation detail. This slice
is hasher-only — NOT yet wired into anything.

**Architecture clarified (2026-07-10): two hash systems, split by PURPOSE, not
by mode.** A unification pass was investigated (make `internal/videophash` the
single Dedup signal for all three modes, delete `internal/phash`) and
explicitly rejected: `internal/videophash` is mechanically coarser than
`internal/phash` (64 bits from one 25-frame collage vs. `internal/phash`'s 320
bits from 5 separately-hashed frames), and Stash's collage algorithm was tuned
for adult-scene content — using it as a Dedup deletion gate for arbitrary
movies/TV would be an unverified, destructive risk (see
`.omc/autopilot/spec-phash-unification.md` §1 for the full analysis; the doc
itself is superseded on its conclusion, not its risk analysis). The settled
split:
- **`internal/phash`** (higher-fidelity, SAK-only, never needs external
  compatibility) is the one **Deduplication** signal across all three modes.
  Movies/Series already have it; Adult Dedup gets it next (see below) — SAK
  computes its own hash for Adult files the same way it does for Movies/Series,
  NOT by reading Stash's live value.
- **`internal/videophash`** (StashDB-compatible, byte-identical to Stash) stays
  reserved for **identification** only — replacing Adult Rename's current
  Stash-read dependency, and any future direct StashDB/FansDB/TPDB fingerprint
  lookups. It is explicitly NOT a Dedup signal.

**Shipped (2026-07-10): Adult Dedup gets `internal/phash`.** `dedup.scanAdult`
(Servarr/Whisparr-backed, groups by `ForeignID`) gets the same
refine-within-identifier-grouping phash gate Movies/Series already have.
`internal/phash` itself is unchanged — reused verbatim, no new calibration,
same default threshold. New `attachPHashesAdult` is deliberately simpler than
its Movies/Series siblings: no cache (Adult has no SAK-owned row to cache a
hash against — `library_items`/`library_episodes` have no Adult equivalent),
every Scan recomputes fresh. Closed a real gap in `internal/api/dedup.go`
where Adult's Scan branch previously received neither a hasher nor a resolved
threshold at all. See the CHANGELOG entry of the same date for the full
safety trace and the new direct `refineByPHash` reference-selection test.

**Shipped (2026-07-10): Adult identify gets `internal/videophash`.**
`rename.scanAdultPhashFirst` now computes its own StashDB-compatible phash
directly instead of reading a live Stash instance's precomputed one. Deleted
the now-dead force-generate/poll machinery (SAK's compute is synchronous).
Fixed a real correctness gap along the way: `DurationSeconds` (required by
fingerprint give-back) used to ride in on the deleted Stash read —
`mediainfo.Probe` gained a `Duration` field to replace it, guarded by a
dedicated end-to-end test through `rename.Apply`. New
`GET|PUT /api/modes/adult/identify-enabled` toggle (default on) replaces the
old `sess.Stash != nil` gate. Per-file compute is bounded to 4 concurrent
workers; a hash error degrades only that one candidate to the legacy
AI/text path. See the CHANGELOG entry of the same date for the full
duration-regression trace and the honest performance note (N ffmpeg decodes
vs. one batched Stash read).

**Shipped (2026-07-10): `SubmitFingerprintRetry` retired — NOT a full
`sess.Stash` teardown.** A correctness fix first: `scanAdultPhashFirst` now
stamps the local phash/duration onto every hashed candidate's proposal,
cascade hit or legacy/text fallback alike (previously only cascade hits got
one), so give-back fires at Apply Stash-free for text matches too. That made
`SubmitFingerprintRetry` and its `/submit-fingerprint` API/UI surface
genuinely unreachable, so they're removed. Give-back at Apply now depends on
BOTH the local hash AND probe succeeding — not "always ready synchronously
at Scan time" as this section previously framed it; the small accepted gap
(a file SAK can't hash, or can't probe, that only text-matches loses
give-back) is documented in the CHANGELOG entry of the same date.
`internal/stashapi`, `sess.Stash`, `buildStashClient`, `mode.Session.Stash`,
and the `"stash"` connection type + `testStash` are RETAINED and
repurposed — not dead code — for the next item below.

**Shipped (2026-07-10): player-rescan-notify — all 5 slices landed.** SAK
now notifies the mode's configured downstream player (Jellyfin for
Movies/Series, Stash for Adult — hardcoded scoping, no toggle) with the
exact changed path(s) after every file-relocate event: Rename/Purge/Dedup's
Apply functions (9 call sites, Slices 3-4) and grab-import's `checkImportHandler`
(the 10th, added post-Critic as Slice 5). `internal/jellyfin` is a new
minimal client (`"jellyfin"` connection type); `sess.Stash` — retained from
the give-back retirement above — is finally read again via a new
phash-free `RescanPaths`. `Session.NotifyPlayers` is best-effort and
log-only: a player being down never fails SAK's own Apply/import, which has
already committed by the time notify runs. See the CHANGELOG entries dated
2026-07-10 (5 entries, one per slice) for the full design/test detail per
slice. Spec at `.omc/autopilot/spec-player-rescan-trigger.md`.

**Shipped (2026-07-12): Whisparr elimination for Adult.** Decided
2026-07-10 (`CLAUDE.md` Scope) — this entry previously listed it under
"Still open" as not-yet-designed, which went stale without the roadmap
being updated; corrected 2026-07-16 after an audit found the codebase and
`CLAUDE.md`'s own "Current state" section already described it as done.
Adult now owns its own library (`internal/library`'s `Scene` type +
`library_scenes` table, keyed on the stash-box `(box, scene_id)` identity
pair, not a Whisparr foreign id), its own library-backed Rename/Purge/
Dedup/Tag paths (`rename.ScanLibraryAdult`/`ApplyLibraryAdult` and the
matching Dedup/Purge siblings, plus scene-level tags via
`/api/modes/adult/scenes/...`), its own free-typed root-folder setting,
and its own fixed naming scheme (`naming.AdultFileName`:
`Studio - Title (Date) [phash-HASH]`). `mode.Build` constructs no Servarr
client for Adult anymore (`sess.Servarr` is nil, proven by
`TestBuild_Adult_ServarrAlwaysNil`) — same displacement already done to
Radarr/Sonarr. `internal/servarr`'s Whisparr support is retained as
generic capability, same precedent as Radarr/Sonarr, even though nothing
in `mode.Build` constructs one. The one-time `internal/whisparrimport`
migration tool was removed entirely (2026-07-12) — no Whisparr connection
type remains. Stash is unchanged and still used, but only as a downstream
player/identification source (phash-first Rename reads a phash Stash
already computed; player-rescan-notify still fires to it) — never as an
organizational authority.

**Shipped (2026-07-18): phash-PRIMARY grouping (TMDB-less).** All-pairs O(n²)
phash comparison across ALL files (tracked + orphans), union-find connected-
components grouping, TMDB used for display labels only. Catches three cases
the old scan missed: (1) orphan-vs-orphan — no shared identifier at all,
(2) cross-ID mis-assignment — both tracked but one resolved to the wrong TMDB
ID, (3) named-vs-unnamed — one file tracked, the other's filename too generic
for TMDB. `dedup_phash_primary.go` — `ScanLibraryPHash` (Movies) and
`ScanLibrarySeriesPHash` (Series); `orphan_phashes` scratch table (migration
`0034`) caches phash values for untracked orphan files. `DefaultMoviesThreshold`
= 25 (more permissive than the Series default of 10 — no shared-intro
false-positive risk for Movies). `PHashSimilarity float64` on
`proposals.Proposal` surfaces minimum pairwise similarity in the group card
header. Commit `50dd970`.

<!-- Claude 2026-08-04: the 25/10 numbers two paragraphs above, and the
     entire "Still open" PDQ bullet below, are now STALE — verified against
     internal/phash/algo.go and distance.go while executing
     .omc/plans/autopilot-impl-phash-grouping.md. Corrected inline below
     rather than rewritten in place, per house rule (never delete, comment
     with a reason): this paragraph's 25/10 were correct AS OF 50dd970
     (2026-07-18), on the PHash 64-bit-per-frame scale of that time.
     Troubleshooting: nobody updated this ROADMAP section after the PDQ
     migration below actually landed — three commits after the "Still open"
     bullet was written (3579a04 Stage 2, 60ece48 Stage 3, 1f1d4c5 Stage 4)
     completed exactly the migration that bullet was still describing as
     future work.
     Review if: never — this note documents a completed migration; nothing
     here is pending. -->
**CORRECTED 2026-08-04: PDQ migration is DONE, not open.** The "Still open"
bullet immediately below predates three commits that completed it:
`3579a04` (Stage 2 — width-agnostic distance.go), `60ece48` (Stage 3 — swap
PHash→PDQ via `internal/phash/algo.go`'s one-file seam, exactly as planned),
and `1f1d4c5` (Stage 4 — calibrate real defaults from measured PDQ harness
evidence). The scheme tag is `pdq256/5f` (256 bits/frame, up from PHash's 64),
so every old-scheme cached hash self-invalidated on upgrade — see
`internal/phash/algo.go`'s `Scheme` doc comment for why the tag had to change.
**Recalibrated thresholds (PDQ 256-bit-per-frame scale, replacing the 25/10
PHash-scale numbers above):** `DefaultMoviesThreshold = 64`,
`DefaultThreshold` (Series) `= 40` — see `internal/phash/distance.go`'s doc
comments for the measured perturbed-duplicate/distinct-content bounds (12 and
98 respectively) those two values were chosen against. The Movies-more-
permissive-than-Series posture the original 25/10 established is preserved.

**Still open (next slices):**
- **PDQ's upstream blocker is resolved — now a real migration, not a wait.**
  Corrected 2026-07-21: this entry previously said `imghash`'s latest tag
  (v1.1.0, what sakms currently depends on) had no PDQ, only on the
  unreleased `main` branch, and pinning to untagged upstream was rejected.
  That's now stale — `imghash` shipped PDQ in a proper tagged release,
  **v2.2.0** (2026-02-21), current latest tag v2.5.2 (MIT, same license
  sakms already consumes it under). But adopting it is a major-version
  upgrade (`github.com/ajdnik/imghash` → `github.com/ajdnik/imghash/v2`),
  not a flag flip: the changelog documents two explicit breaking-change
  releases getting there (v2.0.0 "Improved Library Interface," v2.1.0
  "Refactor Library For Maintainability"). Before swapping PHash→PDQ behind
  `internal/phash/algo.go`'s existing one-file seam, first verify the v2
  rewrite hasn't shifted PHash's own hash output — `DefaultMoviesThreshold`
  (25) and the Series default (10) were calibrated against the current
  library's actual values, and a "refactored for maintainability" major
  version is exactly the kind of change that could move them silently. (One
  dependency-weight concern did resolve cleanly on its own: DINOHash, a
  separate algorithm in the same library, originally bundled an 85MB
  model-weight download into the main module require, but v2.5.2 split
  that out specifically so PDQ-only consumers don't inherit it — no CGo or
  non-Go dependencies anywhere in the PDQ/core path.)
  **CORRECTED 2026-08-04: this bullet is done — see the note directly above
  the "Still open" heading. Kept verbatim below it for history, per house
  rule (never delete, comment with a reason).**

**Shipped 2026-07-19: Vendor-agnostic worker node (`cmd/sakms-node`).** Optional
installable binary that offloads phash/videophash computation to any machine with
better GPU hardware (immediate driver: wade-pc RTX 4070 supports AV1 NVDEC;
server1 Quadro K2200 does not — the entire media library is AV1). The node connects
over SSE, receives jobs, remaps paths via a configurable prefix table, runs
`internal/phash`/`internal/videophash` directly (byte-identical hashes), and POSTs
results back. Server transparently falls back to local execution when no node is
connected. New: `internal/nodes` (Registry + Dispatcher, circuit breaker, pending-
channel invariant), `internal/api/nodes.go` (SSE stream, heartbeat, result, list —
all behind existing X-Api-Key), `cmd/sakms-node` (CGo-free, linux/windows/darwin),
Settings → Nodes tab (read-only: name, status, capabilities, last heartbeat). The
`Dispatcher` implements both `PHasher` interfaces — zero downstream signature churn.
Commit `1843bca`.

**Shipped 2026-07-19: GPU frame decoding.** Concurrent frame extraction
(errgroup, limit 4) replaces the sequential N-subprocess loop in both
`internal/phash` and `internal/videophash`. Hardware acceleration (cuda >
vaapi) is probed once at `New()` time via `ffmpeg -hwaccels`; each decode
retries CPU transparently on driver error. The injected runner seam is
unchanged — unit tests are unaffected. Commit `29a56f3`.

---

## Recently shipped (outside this backlog)

<!-- Claude 2026-08-04: added for Stage 5 (configurable stash-box databases).
     Reason: the Stage 5 spec described a UI layered on an "approved backend
     design" that did not exist; this entry records that BOTH layers landed in
     one session, so a future reader doesn't re-derive the same discovery.
     Review if: OQ7 (moving the seeded keys inline) is ever decided. -->
### Configurable stash-box databases (Stage 5) — shipped 2026-08-04
`.omc/specs/deep-interview-stage5-stashboxdb-ui.md`, built per
`.omc/plans/autopilot-impl-stage5-stashboxdb-ui.md` on the backend design in
`.omc/plans/ralplan-adult-identify-configurable-databases.md`. StashDB and
FansDB are no longer hardcoded singleton connections with hardcoded endpoints:
they are two rows of an operator-managed registry (migration 0061,
`internal/stashboxdb`, `/api/stashbox-databases`) holding up to 5 stash-box
databases, each with its own name, GraphQL endpoint, key, cascade priority,
enabled flag and fansite-only gate. `internal/identify` iterates that registry
everywhere it used to write `["stashdb","fansdb"]`. The UI is a `RowEditor`
host in Settings → Library → Adult. See the 2026-08-04 CHANGELOG entry for the
full file list and the five recorded deviations.

Things a future session should not quietly undo:
- **There is no "built-in" tier, and adding one would be a regression.** The
  two seeded rows are fully renameable, re-pointable, reorderable and
  deletable, and the wire format carries no flag that would let the UI treat
  them differently (AC11). What constrains renames is not a protected tier but
  two guards: the reserved name `tpdb`, and the `library_scenes` name-reuse
  tombstone (`ErrNameHaunted`) that stops a name with tracked-scene history
  being rebound to a different database.
- **`secret_ref` is an internal handle, not a user-visible tier.** It exists so
  the two seeded rows keep reading their EXISTING `connections` keys (no
  re-entry, ever). It is never serialized, has zero matching effect, and
  `hasApiKey`/`keySuffix` always report the RESOLVED key so the UI cannot tell
  which table a secret lives in.
- **An `Identifier` with no `StashBoxes` snapshot means LEGACY, not "no
  databases".** It falls back to `stashdb` then `fansdb`. Changing that to an
  empty cascade would silently disable identification for every hand-built
  `Identifier` — which is all of them in tests.
- **`reSearchAfterGrounding` is ungated on purpose**, despite ralplan §2.3
  asking for the fansite gate there. That site never had one, and adding it
  changes the databases queried on a default install (AC15). It is probably
  the better behaviour once AC15 is retired — see the comment at the call site.
- **The registry panel is deliberately NOT inside Library's `SectionSave`
  batch.** Reorder-on-drop and Delete are immediate and irreversible; putting
  them behind a Save button that might never be pressed would make the shared
  dirty flag lie. Same call `RssFeedAdmin` made.
- **`stashdb`/`fansdb` are filtered out of `GET /api/connections` server-side**,
  not in the frontend. Removing that filter restores a double-listing bug where
  the old surface could overwrite a key the new one cannot see.

Still open (deliberately, not forgotten):
- The plan's two LIVE checks were not run: a real reachable-vs-unreachable
  endpoint test (AC12) and a manual AC15 spot-check against a known scene.
  Both need real stash-box credentials.
- OQ7 — whether to move the seeded keys inline into `stashbox_databases` and
  drop their `connections` rows — is still undecided. Several "inert, kept on
  purpose" comments (`fixedURLServices`, the legacy `TestConnection` case)
  point at it as their review trigger.

<!-- Claude 2026-08-08: added for the cross-mode move feature.
     Reason: the feature's plan went through 5 critic REVISE rounds finding
     two real, severe defects during planning (a cross-mode Dedup data-loss
     bug and an Adult-library exfiltration path) — a future session touching
     this endpoint needs that context before "simplifying" anything here.
     Review if: the copy+unlink relocation fallback below ever gets built,
     or if D-1a's Adult-gating design is ever revisited. -->
### Cross-mode "move to another section" (Rename + Dedup) — shipped 2026-08-08
`.omc/specs/deep-interview-sakms-cross-mode-move-rename-dedup.md`, built per
`.omc/plans/autopilot-impl.md` (5 critic REVISE rounds before APPROVE — the
most adversarially reviewed plan this project has produced). New
`POST /api/proposals/{id}/move-mode` lets an operator reassign a Proposal
scanned into the wrong Mode (Movies/Series/Adult); new
`GET /api/modes/adult/scene-search` gives Adult its first multi-result
correction surface. See the 2026-08-08 CHANGELOG entry for the full file
list, both defects found during planning, and the mutation-testing evidence
that the regression tests for them actually catch the bugs they're named
for.

Things a future session should not quietly undo:
- **Every candidate's `TrackedID` is zeroed on move, and the source mode's
  tracking rows are retired in the SAME transaction as the mode UPDATE.**
  Neither half alone is safe — zeroing without retirement leaves dangling/
  double-tracked library rows; retirement without the shared transaction
  means a routine 409 conflict silently destroys library rows while
  reporting the move as "failed". Don't split these into separate steps
  outside `Store.MoveMode`'s ownership.
- **Never call `DeleteSeries` from the retirement path.** A Series
  candidate's `TrackedID` is an EPISODE id, not a series id — `DeleteSeries`
  would cascade-delete an entire unrelated series' tracking history. Use
  `DeleteEpisodeTx`.
- **Any move touching Adult (source OR target) requires the Adult section
  unlocked — this deliberately deviates from the spec's literal AC7 text**,
  which said the PIN lock should only gate viewing, not moving. Confirmed
  with Wade during planning after the ungated design turned out to be an
  enumerable way to read Adult filenames out of the Movies queue without
  ever entering the PIN. Movies↔Series stays fully ungated.
- **Dedup moves never relocate files and preserve `root_folder_path`**
  (`internal/dedup` has no relocation call anywhere) — the opposite of the
  Rename-workflow behavior, which rewrites `root_folder_path` to the target
  root. Don't unify these two without re-deriving why they differ.

Still open (deliberately, not forgotten):
- **No cross-device (EXDEV) relocation fallback.** A move whose target
  library root is on a different filesystem than the source commits the
  mode change and then fails at Apply with `invalid cross-device link` — a
  recoverable, non-destructive outcome (Apply declines, the proposal stays
  Pending), but not a great one. The real fix is a copy+unlink fallback in
  `internal/rename`, which benefits every relocation path in the app, not
  just this feature — genuinely out of scope here, filed as a real backlog
  item, not a rejected idea.
- AC9's manual dev click-through and AC10's live server1 deploy verification
  are Wade's to run.


### Season/episode picker redesign (poster grid + TMDB response cache) — shipped 2026-08-02
`.omc/specs/deep-interview-season-episode-picker-redesign.md`, built per
`.omc/plans/autopilot-impl-season-episode-picker-redesign.md`. The two
free-text `S`/`E` inputs are replaced by a two-level poster grid: a season grid
(proxied poster, name, episode count, air year) drilling into an episode grid
(proxied still, number, name, air date, runtime), with a leading "Whole season"
tile preserving the shipped `episode: 0` / `SeasonSpecified: true` semantic that
protects Season 0 / Specials. Images reuse `/api/images/proxy` — zero backend
image work, never-hot-link rule unaffected. Data arrives through the existing
`GET /api/modes/{mode}/discover/detail`, extended with a response-scoping
`sections=seasons` param rather than a new endpoint, with the per-season episode
fetches fanned out server-side (bounded, soft-failing per season) so the browser
makes one request, not N. A general-purpose LRU+TTL TMDB response cache
(`internal/tmdb/cache.go`) sits behind `Client.do()`, so every TMDB call in the
codebase is cached by one change.

Things a future session should not quietly undo:
- **SIX mount points, not the three the spec names.** Five reach the picker
  through the shared `GrabButton` (Discover rows, "In your library",
  Trakt watchlist, `DetailPopup`'s "More like this" rail, `CalendarView`); the
  other two are `DetailPopup`'s own inline gating step and bulk-select.
  (`Library.tsx` has a same-named, unrelated `PosterCard` — not affected.)
- **Omitting the `seasons` prop is what enables self-fetching**, not its value.
  `DetailPopup` passes it (plus `loading`) because it already holds the bundle;
  passing it anywhere else — even as `undefined` — would hand loading control to
  that caller permanently.
- **Grab is suppressed on SERIES cards in `DetailPopup`'s recommendations rail
  only.** Its picker would open a second modal inside the popup's own, and both
  close on one backdrop click. Movies are deliberately untouched: their grab
  opens no picker and nests nothing.
- **The degraded free-text fallback is required and the spec never asked for
  it.** A failed/empty enumeration, or a `tmdbId <= 0` (reachable from the
  library and Trakt rows), renders the old two inputs. Without it a TMDB outage
  removes every Series grab in the app at once. It will look like dead code.
- **Grid, not carousel**, at both levels — deliberate, scoped to the picker.

### Browser (desktop) notifications for webhook events — shipped and deployed 2026-07-21
A human-directed addition, not a pre-existing backlog item (distinct from
"Webhooks + real API docs" below, which is the outbound-webhook CRUD
feature this builds on). Foreground-only (tab-must-be-open) desktop
notifications for the same four events sakms already tracks for outbound
webhooks (`rename.applied`, `purge.applied`, `dedup.applied`,
`grab.completed`). `webhooks.Store` gained a composed, `sync.RWMutex`-
guarded `broadcaster` that `Dispatch` publishes to unconditionally and
first — before the existing subscription-gated outbound-webhook delivery
— so live broadcast can never silently depend on whether any webhook is
configured (the exact defect the first design draft had, caught by
Architect+Critic review before implementation; see
`.omc/plans/browser-notifications.md`, Rev 3). New `GET
/api/notifications/stream` SSE endpoint; a shell-mounted
`BrowserNotifications` component subscribes via `EventSource` and calls
the native `Notification` API with a stable per-event-type `tag`,
collapsing cross-tab duplicates and same-type bursts (e.g. a bulk Apply)
into one visible notification instead of stacking. Settings toggle shares
a reactive signal with the shell component (flips take effect without a
reload) and distinguishes "off" from "on but blocked by the browser."

Verified: `go build`/`go vet` clean, `go test -race` zero race reports
(including the two correctness-critical tests the plan called out: a
zero-configured-webhooks store still broadcasts, and concurrent
subscribe/unsubscribe against a hot `Dispatch` loop), frontend `tsc`/
vitest clean (14 new tests). Pushed and auto-deployed same day
(`deployed_sha` = `d782861`, container `Up`, healthz 200 first attempt).

**Still outstanding**: the plan's own manual verification step was never
run — open two browser tabs, enable the toggle + grant permission in
both, trigger a Rename/Purge/Dedup Apply or a Grab, and confirm exactly
one visible desktop notification appears (not one per tab, not one per
item in a bulk-apply burst). This needs a human in a real browser: not
something that can be scripted/asserted automatically. Also unexercised:
the "blocked" UI state when the browser has denied permission, and the
toggle-off/on-without-reload `EventSource` open/close behavior — both
covered by frontend unit tests with mocks, but not against a real
browser.

### Node path mapping: library-path-driven + security hardening — shipped and deployed 2026-07-20
`cmd/sakms-node` worker-node path mappings (introduced 2026-07-19) are now
keyed off the fixed set of Library-settings paths instead of free text,
with a live remote-browse picker replacing the old freeform editor
(commits `037d03f`, `ba91f87`). A follow-on security hardening addendum
(commit `4212e3d`, its own `ralplan`-consensus design cycle + `ralph`
execution, 7 stories) adds two independent safeguards the operator
explicitly requested — a server-side directory-listing containment check
that hard-rejects a mismatched node-path mapping before it's ever
persisted, and a node-side `mediaRoots` allowlist that rejects any settings
push mapping outside it — plus packaging changed so `cmd/sakms-node` runs
as a dedicated non-root user. See `CHANGELOG.md`'s two 2026-07-20 entries
for full per-story detail, the 2 real bugs a THOROUGH-tier architect
review caught (an upgrade-path ownership regression; conflated
mismatch/unreachable error handling), and this deploy's own verification:
pushed + auto-deployed (`deployed_sha` = `4212e3d`, container `Up`, health
checks passed), and a real `sakms-node.service` restart on wade-pc
confirmed the durable node identity (the entire point of the underlying
US-0 work) actually survives a reconnect, not just in unit tests.
**Still outstanding, needs a dedicated pass**: mediaRoots enforcement
against a real crafted out-of-bounds push, a real wrong-folder mapping's
rejection evidence, and an RPM build/install proving the daemon starts
cleanly as the new non-root user.

### Tagging UI grid view — shipped 2026-07-19
Two-panel layout for the `/tag` screen (Movies/Series). Left: responsive
poster-card grid (2–4 cols, client-side title search, localStorage-persisted
grid/table toggle). Right: detail panel with read-only genres/cast chips and
the existing immediate-commit tag editor. Adult keeps the unchanged table
view. `frontend/src/screens/Tag.tsx` (609 lines); 6 new tests in
`Tag.test.tsx`. Commit `b470ca2`.

### Unified downloader — fully shipped (torrent engine + Usenet native support)

**Shipped 2026-07-18 (torrent only, commits `c3a3526`+`5eeae1f`):** SAK now
owns torrent downloads directly — no external qBittorrent required. An aria2c
static binary is bundled in the Go binary at build time (`//go:embed
assets/aria2c`, fetched by `cmd/download-aria2c` from abcfy2/aria2-static-build
v1.37.0). `internal/aria2` is a JSON-RPC client; `internal/downloader.Manager`
manages the subprocess lifecycle (spawn, restart-on-exit with exponential
backoff, log forwarding), polls aria2 every 750 ms, and fans out live
download-queue snapshots to an SSE hub (`GET /api/downloads/stream`). The
Downloads screen (`frontend/src/screens/Downloads.tsx`) shows per-download
filename, progress bar, speed, ETA, status badge, and Pause/Resume/Cancel
buttons. On GID completion, `DownloadCompleteImporter` runs the same
staging→library move as the old NZBGet/qBittorrent import path.

**Shipped ~2026-07-18: anacrolix/torrent in-process engine replaces the aria2c
subprocess.** `cmd/download-aria2c` and `internal/aria2` deleted; `internal/downloader`
now uses the anacrolix/torrent in-process engine (`github.com/anacrolix/torrent
v1.61.0` direct dep in `go.mod`). The subprocess spawn/restart/backoff/log-forwarding
machinery, the embedded aria2c binary, and the JSON-RPC polling loop are all gone.

**Shipped ~2026-07-18: Usenet/NZB native support.** `internal/usenet` provides
NNTP connection pooling, yEnc decoding, and NZB parsing (`pool.go`, `nzb.go`,
`manager.go`). `internal/api/search.go` wires `*usenet.Manager` into
`grabHandler` and `checkImportHandler`; "nzb-" prefixed GIDs route to the
native NNTP engine rather than returning a 400. Basic Usenet support is shipped;
par2 repair status is TBD.

### Collections — shipped (pre-2026-07-17; discovered complete during audit)
`library_collections` table (migration `0031`), `UpsertCollection` +
`SetItemCollection` on library.Store, `enrichMovieCollection` called
post-Apply in `internal/api/proposals.go` to fetch `belongs_to_collection`
from TMDB and record it on the newly-tracked movie row, `GET
/api/modes/movies/collections` endpoint (`internal/api/collections.go`),
`CollectionName` returned in the tracked-items API, and a `/collections`
route with `Collections.tsx` screen in the sidebar. All complete before
this session — entry was stale.

### Local .nfo preference for Movies/Series Rename — shipped 2026-07-17
`internal/nfo` reads Kodi/Jellyfin `.nfo` sidecar files and provides an
authoritative TMDB ID when present, skipping the fuzzy filename search and
confidence gate entirely. Both common XML shapes handled: flat `<tmdbid>`
and `<uniqueid type="tmdb">`. 

**Movies** (already wired before this session): `nfo.ReadSidecar` tries a
same-basename sidecar first, then `movie.nfo` in the same directory. Folder
entries (where `ScanRootFolder` yields the wrapping directory) look inside
the folder. Fast-path lives in `proposeOneLibrary`, before the TMDB search.

**Series** (added 2026-07-17): `nfo.ReadSeriesSidecar` tries, in order:
`{episodeDir}/../tvshow.nfo` (series root, the common season-subfolder
layout), `{episodeDir}/tvshow.nfo` (flat layout), then the episode's own
`.nfo` sidecar. Fast-path lives in `proposeOneEpisodeLibrary`, before the
TMDB search — season and episode numbers are still parsed from the filename,
and `SeasonDetails` is still called to verify the season exists. 7 new
tests added to `internal/nfo/nfo_test.go`.

Artwork reuse (local poster/fanart) remains open if it comes up.

### TVDB fallback for Movies/Series Rename — shipped 2026-07-17
When TMDB search returns zero results or a below-threshold confidence match
during Rename scan (Movies and Series), SAK now tries TheTVDB v4 as a
secondary source before returning Unmatched. The TVDB match is translated
back to a TMDB ID via TMDB's `/find?external_source=tvdb_id` endpoint, so
the library stays TMDB-keyed throughout — no schema changes, no dual-ID
tracking. TVDB is configured as a connection (Settings → Connections →
"TheTVDB") with an API key; when absent, the fallback silently skips and
the existing Unmatched behavior is unchanged.

Key files: `internal/tvdb/client.go` (new v4 client, bearer-token cached
29 days, `SearchSeries`/`SearchMovies`/`Ping`), `internal/tmdb/client.go`
(new `FindMovieByTVDBID`/`FindTVByTVDBID` methods), `internal/mode/mode.go`
(`Session.TVDB` field + `buildSearchPipeline` wiring), `internal/rename/rename.go`
(`tvdbFallbackMovie`/`tvdbFallbackSeries` helpers injected at both zero-result
and low-confidence sites in `proposeOneLibrary`/`proposeOneEpisodeLibrary`),
`internal/api/connections.go` (`testTVDB` + `"tvdb"` case).

### System dashboard — shipped 2026-07-17
Fourth item off the "least complex to most complex" backlog ordering.
New `internal/sysinfo` package reads five Linux pseudo-filesystem sources
to provide container-scoped and server-level resource metrics with no new
Go dependencies (pure stdlib + `runtime` + `syscall`):

- **CPU %** (container): `/sys/fs/cgroup/cpu.stat` `usage_usec` delta over
  elapsed time, normalized across all CPUs.
- **RAM** (container): `/sys/fs/cgroup/memory.current` + `memory.max`
  (unlimited when file reads "max").
- **Network rx/tx BPS** (container): `/proc/net/dev` — container-scoped
  via network namespace isolation; loopback excluded.
- **Container disk I/O** (BPS): `/sys/fs/cgroup/io.stat` `rbytes`/`wbytes`
  sum across all cgroup block devices.
- **Server disk I/O** (BPS per disk): `/proc/diskstats` filtered to whole
  physical devices only (`sd[a-z]+`, `nvme\d+n\d+`, etc. — partition
  entries with numeric/`p\d+` suffixes excluded by anchored regexp).
- **Storage usage** (data volume): `syscall.Statfs("/data")` —
  `Bavail`/`Blocks * Frsize` gives available and total bytes for the
  container's persistent data mount.

`GET /api/admin/sysinfo/stream` is a server-sent events endpoint (SSE —
no external dependency; pure HTTP `text/event-stream` via Go's stdlib
`http.Flusher`). It fires every 2 seconds. Transport errors use the
browser's native SSE reconnect; in-stream sample-read failures emit a
named `sampleError` SSE event (distinct from transport errors so the
frontend can surface them without closing the connection). The endpoint
inherits the same `auth.Middleware` session/`X-Api-Key` gate as all other
`/api/admin/*` routes.

Frontend: new `Dashboard` screen (`EventSource` + SolidJS signals), cards
for each metric group (fill bars for CPU/RAM/Storage, BPS labels for
network and disk), `formatGB` helper for storage. Dashboard nav item added
as the first entry in the sidebar. 10 new Go tests (9 sysinfo package, 3
SSE handler); 4 new frontend tests; 287 total passing. `pnpm build` clean.

One `UNVERIFIED ASSUMPTION` note: the storage path `/data` assumes the
container's data volume is mounted there — confirmed correct for the
current iSCSI bind-mount setup; will remain correct when the planned
TrueNAS NFS mount replaces it (same container path, different backing).

### Bulk apply — shipped 2026-07-17
Third item off the "least complex to most complex" backlog ordering — a
deliberate, documented reversal of the "one item at a time" rule (see
`CLAUDE.md`'s amended "Staged-for-approval" convention). Each of the three
workflow review queues (Rename, Dedup, Purge) now carries an opt-in,
same-screen multi-select: the operator checks one or more already-reviewed
Pending rows/groups on a single workflow+mode screen and clicks "Apply
Selected," which POSTs one `POST /api/proposals/apply-batch` request.

Backend (`internal/api/proposals.go` + `internal/api/apply_batch_test.go`):
skip-and-continue semantics — each item gets its own `applyByWorkflow` call,
one failure never blocks the rest, every id gets an individual `ok/error`
result in the response body (always 200). Sequential execution by design
(avoids concurrent filesystem races on overlapping paths). `applyByWorkflow`
refactored to return `([]PathChange, error)` so the caller accumulates
committed mutations for a single per-mode `NotifyPlayers` call after the
loop — grouping changes by mode so each mode's changes reach the correct
mode-scoped players, not the last-built session. New
`applyBatchRequest`/`applyBatchResponse`/`applyBatchResultItem` DTOs
(`internal/apidto`). `apply_batch_test.go` covers partial-failure
skip-and-continue, combined notify, and the committed-file/failed-DB-write
partial-success rule (via a `markAppliedFailStore` test seam that can't be
induced with a real store).

Frontend: `useBulkSelection` hook (`workflowHooks.ts`) — `selectedIds`
signal, toggle, toggleAll, clear (cleared on mode-switch/scan/act).
`BatchResultSummary` shared component (`ui.tsx`) renders "N applied, M
failed" with per-failed-item title + error. Rename/Purge gain a checkbox
column + Select All header + "Apply Selected (N)" button; Dedup gains a
per-card checkbox + "Apply Selected (N)" button that sends each card's
existing `keepSel` keepIndex (winner-fallback for unselected cards, exactly
matching single-item Apply). Purge's button is labeled "Delete Selected (N)"
with the same `window.confirm` guard as single-item Purge. Old "no bulk
affordance" tests updated to positive assertions; new tests cover selection,
the apply-batch endpoint call, and partial-failure summary display.

`CLAUDE.md` and `SEERR_SCOPE.md` record the principle change as an explicit,
dated reversal with a cross-reference, not a silent edit. `.gitignore` gained
an unanchored `.omc/` line so subdirectory OMC agent state is never swept
into a commit.

### Structured Genre/Actor tagging — shipped 2026-07-17
Fifth item off the "least complex to most complex" backlog ordering. Movies
and Series proposals and library records now carry structured `genres`
(`[]string`, TMDB genre names) and `cast` (`[]CastMember{Name, Character,
Order}`) fields populated at Scan time from TMDB's `/movie/{id}/credits` and
`/tv/{id}/credits` endpoints. Both are stored as JSON columns in
`library_items`, `library_series`, and `proposals` (`genres`, `cast` —
the latter column name required quoting in SQL expressions as `"cast"` since
it is a SQLite reserved word; plain `COALESCE(cast, '[]')` was parsed as a
broken `CAST()` invocation and produced `SQL logic error: near ",": syntax
error`). Enrichment runs per-match after each TMDB search result resolves,
with a soft 404-on-error policy — a missing credits endpoint never fails
the whole Scan. Frontend test mock servers for all four rename/series test
files were updated to return `http.NotFound` (instead of `t.Fatalf`) for
enrichment paths that carry no `query` parameter.

### Watch folders (inotify) — shipped 2026-07-17
Sixth item off the "least complex to most complex" backlog ordering.
`internal/api/watchfolders.go` (new, ~300 lines): a background goroutine
(`RunWatchFolders`) launched from `main.go` that monitors each mode's
configured library root folder via `fsnotify` (v1.8.0, the only new
dependency). Design decisions kept:

- **Scan-only, never auto-Apply** — proposals land in the Rename queue and
  still require a human Apply click, preserving the staged-for-approval
  invariant.
- **10-second debounce per mode** — absorbs burst events from a download
  client dropping a full directory tree into the root folder; a single
  `time.AfterFunc` is reset on every `Create`/`Rename` event and fires once
  after 10 s of quiet.
- **30-second settings poll** — the outer loop re-reads `watch_folders_enabled`
  and root paths every 30 s, so enabling/disabling or changing a root folder
  takes effect without a restart.
- **Gated off by default** (`watch_folders_enabled = false`). Settings toggle
  in the Advanced tab (`GET /api/admin/watch-folders`,
  `PUT /api/admin/watch-folders/enabled`).

`scanFromWatcher` reuses the same `mode.Build`/`resolveNamingPreset`/
`resolveConfidenceThreshold`/`rename.Scan*`/`propStore.ReplacePending`
chain as the manual Scan button — same proposals, same queue, same Apply
path. Errors are logged and dropped; the manual Scan button always remains
the fallback. The feature inherits the same `ctx`-cancellation path as
`recheck.Run` and `adultnewest.Run`, so shutdown cancels it cleanly.

### Clearer mount-disconnect error messaging — shipped 2026-07-11
`library.ScanRootFolder`'s single error-return point (all four Rename/Dedup
Scan call sites share it) now classifies the underlying OS error: a missing
path, a dropped network mount, or an I/O error against it
(`fs.ErrNotExist`/`syscall.ENOTCONN`/`ESTALE`/`EIO`/`EHOSTUNREACH`) gets
wrapped as "root folder unreadable — check that `<path>` is still mounted
and reachable", instead of a bare `lstat ...: no such file or directory`
surfacing straight to the operator. The original error is still wrapped via
`%w` either way, so `errors.Is`/logs keep the raw OS error underneath.
One classification point, not four — every caller (`rename.ScanLibrary`/
`ScanLibrarySeries`, `dedup.ScanLibrary`/`ScanLibrarySeries`) inherits it for
free through their existing `fmt.Errorf("scanning %s: %w", ...)` wraps.

### Confidence scoring for Rename matches — shipped 2026-07-11
Closed the "Matching quality" backlog item above for Movies/Series (that
entry originally noted a deliberate Adult/`lookupFirst` scope deferral —
see its 2026-07-16 correction below, since Whisparr elimination for Adult
made the deferred code path disappear entirely). `internal/
rename/confidence.go` (new): `matchConfidence` scores TMDB's best
(`items[0]`) search result against the cleaned search term, 0-100, combining
a Dice-coefficient word-token similarity (`titleSimilarity`) with a year-
corroboration check (`extractYear`, preferring a parenthesized year, falling
back to an unambiguous bare one) that halves the score on a >1-year mismatch
against TMDB's release year — but only when both sides have a known year, so
a search term with no year signal at all isn't penalized. `ScanLibrary`/
`ScanLibrarySeries` and their per-item `proposeOneLibrary`/
`proposeOneEpisodeLibrary` helpers gained a `confidenceThreshold int`
parameter; a below-threshold `items[0]` now routes to `Unmatched` (reason
names the search term, the rejected title, the score, and the threshold)
instead of being silently accepted — the exact gap the backlog item
described. New per-mode setting (`GET/PUT /api/modes/{mode}/match-
confidence-threshold`, 0-100, defaults to `rename.DefaultConfidenceThreshold`
= 40), mirroring `phash-threshold`'s existing storage/validation shape
exactly. No frontend control yet — same precedent as `phash-threshold`,
which also shipped API-only.

Same-day `code-reviewer` pass (separate context, per house policy): 0
blocking issues. Verdict COMMENT, not APPROVE, specifically to surface the
Adult/`lookupFirst` scope question as a conscious decision rather than a
silent skip (see above) — everything else was polish (a stale doc-comment
symbol reference, fixed; a missing Series-specific weak-match test
symmetric to the Movies one, added). Reviewer independently reran the
scorer against real fixture data and confirmed the default threshold (40)
clears every genuine match with a wide margin (e.g. 86, 80) while an
unrelated result scores 0.

Verified via `gofmt -l` (clean), `go build ./...` / `go vet ./...` (clean),
and full `go test ./...` (all green) — both before and after the
reviewer-prompted fixes.

### Manual override / re-pick for Rename matches — shipped 2026-07-11
Closed the "Matching quality" backlog item above for Movies/Series. Today
Dismiss only removed something from the queue — it couldn't correct a
match that Scan got wrong, or that confidence scoring (see above) routed
to Unmatched for being too weak to auto-accept.

New `proposals.Store.Repick(ctx, id, title string, tmdbID, year int) error`
overwrites a proposal's title/tmdbId/year, unconditionally promotes it to
Pending, and clears any stale `Reason` — no status guard in the SQL itself
by design, since its one caller (`repickProposalHandler`) already enforces
the eligible-status precondition (Pending or Unmatched only; Applied/
Dismissed proposals are refused, so a re-pick can never silently rewrite
the queue's record of something that already happened). New `POST /api/
proposals/{id}/repick` (`{tmdbId, title, year}`, all but year required) and
`GET /api/modes/{mode}/tmdb-search?q=...` (a thin `SearchMovies`/`SearchTV`
proxy, mirroring `discoverHandler`'s session pattern — the search box's
backend) — both Movies/Series only, `tmdb-search` via an explicit mode
check rather than relying on `sess.TMDB` being nil for Adult (it isn't;
`mode.Build`'s `buildSearchPipeline` populates TMDB for every mode from the
one global connection, Adult included). Frontend: `renderRename` gained a
"Re-pick" button (Pending/Unmatched, Movies/Series) opening a shared inline
search panel with a pre-filled query, results, and "Use this" per result.

The repick request trusts the client-supplied `{tmdbId, title, year}`
triple directly (from a prior tmdb-search response) rather than the server
re-fetching authoritative values by id — same tradeoff Scan's own
`proposeOneLibrary`/`proposeOneEpisodeLibrary` already make from a TMDB
search response, consistent with the single-operator trust model (no
permissions surface to protect against the operator's own client).

Same-day `code-reviewer` pass (separate context, per house policy): 0
blocking issues (5 LOW). Two were fixed before committing: `tmdb-search`
gained the explicit Movies/Series mode check described above (the original
comment's claim that Adult naturally 400s was false — fixed the invariant,
not just the comment), and a missing Series-specific end-to-end test was
added (`TestRepickWorkflow_Series_WeakMatchSearchRepickApply_EndToEnd`) —
the same category of gap confidence scoring's review caught, now checked
for on both features. Three LOW items left as documented, non-blocking
tradeoffs matching existing codebase conventions: a `Get`-then-`Repick`
TOCTOU (two round trips, not one atomic `UPDATE ... WHERE`— real but low,
same shape as the existing dismiss/apply handlers), a repick failure's
error message getting wiped by the immediately-following queue refresh
(matches the pre-existing Apply/Give-back/Dismiss convention, not a
regression), and the client-trust tradeoff above.

Verified via `gofmt -l` (clean), `go build ./...` / `go vet ./...` (clean),
full `go test ./...` (all green), and `node --check` on the extracted
`<script>` block (frontend syntax valid) — both before and after the
reviewer-prompted fixes.

### First-run break-glass recovery — shipped 2026-07-11
OIDC-mode first-run mints a one-time recovery API key (see CHANGELOG) —
there's no interactive-login fallback at setup time (the browser hasn't
completed the IdP redirect dance yet), so the key is the operator's way back
in if SSO login is ever unavailable.

### Auth strategy switch — shipped 2026-07-11 (superseded same day)
A human-directed addition, not a pre-existing backlog item. Auth is chosen at
first-run and switchable later from Settings. Originally shipped with four
strategies (`password`, `forward`, `authentik`, `none`); later the same day,
`forward` (reverse-proxy shared-secret) and `authentik` (RFC 7662 bearer-token
introspection) were **both deleted and replaced by a single `oidc` mode** — a
real, provider-agnostic OpenID Connect Authorization Code flow with PKCE where
SAK is the Relying Party (JWKS-verified ID token, no proxy-held secret). The
supported set is now exactly `password`, `oidc`, `none`. All three share one
mode-aware `Middleware` that fails closed on any mode-read error, and the
`X-Api-Key` header works in all three modes. See `CHANGELOG.md`'s two
2026-07-11 entries (the original switch, then the OIDC replacement) for the
full design/decision detail.

### API-key auth (X-Api-Key) — shipped 2026-07-10
A human-directed addition, not a pre-existing item anywhere in this
backlog. Any `/api/...` route now accepts either the session cookie or an
`X-Api-Key: <key>` header, so an out-of-process client (a script, a test
harness) can call SAK without a browser session. Boot resolves the key
from `SAKMS_API_KEY` (in-memory, stable across restarts, never persisted)
or auto-generates and persists a SHA-256 hash on first boot, reusing it on
every later boot; the raw key is shown in full exactly once, from Settings
→ API Access (`GET /api/apikey` status, `POST /api/apikey/regenerate`,
refused with 409 while env-managed). `/healthz` and `/api/auth/*` are
unchanged and still fully public. See `CHANGELOG.md`'s entry of the same
date for the full design/honesty-framing detail.

### Frontend redesign (shell) — shipped 2026-07-13
The "Frontend redesign" backlog item below previously described this as
not-yet-started, which went stale without the roadmap being updated;
corrected 2026-07-16 after an audit found the shell already shipped. The
old 2,284-line hand-written vanilla-JS `static/index.html` is gone
entirely — the frontend is now a SolidJS + Vite SPA (`frontend/`),
compiled at build time into the Go binary's embedded `static/` tree, same
as before (`internal/web`, `//go:embed static`; no Node.js runs in
production). A collapsible left sidebar (`AppShell.tsx`) replaced the old
horizontal top nav, and a generic `useScreenTabs`/`ScreenTabBar` mechanism
(`components/ui.tsx`) lets any screen register its own tab set with the
shell's one consistent tab-bar slot — used by both Settings (Connections/
Library/UI/Auth/Advanced) and Discover (Mainstream/Adult). This shipped
the *shell* only; the mockup-driven content it was meant to eventually
host (bulk-apply tables, the system dashboard, Collections/tagging UI)
remains genuinely unbuilt — see the trimmed "Frontend redesign" backlog
entry below, which now only describes that remaining work.

### Adult Discover "newest releases" background scan — shipped 2026-07-15
A human-directed addition, not a pre-existing backlog item. New
`internal/adultnewest` package: an opt-in (off by default, same
convention as `internal/recheck`) periodic job that scans Prowlarr's
newest Adult releases and matches each one to a TPDB/StashDB/FansDB
entity via the existing identify pipeline, caching matched results
(migrations `0024`-`0027`) for Adult Discover's "newest releases" rows to
read at request time — Discover itself never queries Prowlarr directly,
preserving the existing "Discover never queries Prowlarr" rule. Rows are
admin-configurable (Movie/Scene/Performer/Studio, optionally genre-
narrowed) via a Settings admin UI (`AdultRowAdmin.tsx`), the same
CRUD+reorder shape as the existing TMDB-backed Discover sliders. See
`CHANGELOG.md` for full per-slice detail (not yet backfilled there as of
2026-07-16 — flagged as a gap during the same audit).

### RSS-sourced Discover rows — shipped 2026-07-15
A human-directed addition, not a pre-existing backlog item. New
`internal/rssfeeds` package (migration `0028`): admin-defined raw RSS 2.0
feed rows (NZBGeek saved-search style) — a per-row feed URL fetched and
parsed server-side at resolve time, distinct from the TMDB-backed
Discover sliders and the Prowlarr-backed Adult-newest rows above (three
separate row-config systems now, deliberately not unified — see CLAUDE.md's
"no premature abstraction" convention). Admin UI mirrors the existing
slider/Adult-row editors' CRUD+reorder shape.

**Follow-up shipped, undocumented until now (discovered 2026-07-31 during
planning-session reconciliation): Adult RSS feed admin relocated into
Settings, with Edit + protocol auto-detection added.** Commit `e043082`.
The consensus-approved `.omc/specs/deep-interview-adult-rss-feed-settings-relocation.md`
(9 rounds, ~13% ambiguity, PASSED 2026-07-27) sat marked "pending approval"
for 4 days while the work was actually built and merged — its tracking
status was simply never updated to reflect reality. `frontend/src/screens/settings/RssFeedAdmin.tsx`
now lives in Settings → UI → Discover → Adult, with a genuine Edit
capability that didn't exist before, drag-and-drop reorder (reusing
`RowEditor.tsx`'s pattern), and server-side protocol auto-detection on Add
+ a per-feed Re-scan action, with a one-time manual-pick fallback dialog
when detection is inconclusive. `protocol` is no longer a manually-set
field anywhere in the normal flow. See the spec file for full acceptance
criteria (all met) — this entry exists so ROADMAP.md stops contradicting
the actual codebase.

### DB-first Adult filename parsing; bundled-Ollama image removed — shipped 2026-07-16
A human-directed addition, not a pre-existing backlog item. New
`internal/parseentity` package (migration `0029`): a local SQLite cache of
normalized studio/performer names sourced from Stash/TPDB/StashDB/FansDB,
letting Adult filename parsing resolve studio/performer/title
deterministically from this DB-first lookup instead of relying on an AI
model for every file. AI (`ParseFilename`) is now an explicit, off-by-
default *fallback* only, gated by a new toggle — it runs when DB-first
parsing can't resolve a field, not unconditionally. New Settings UI
(Connections → AI tab): entity-cache counts, per-source "Sync now"
buttons, and (added same day as a follow-up, see `CHANGELOG.md`) a shared
opt-in background sync interval plus a manual on-demand trigger. The
previously-documented opt-in Ollama-bundled Docker image (`ai` build
target, see the 2026-07-11 CHANGELOG entry) was removed as part of this
same change, superseding that entry — DB-first parsing needing no AI
backend at all removed the motivation for shipping one bundled. See
`CHANGELOG.md` for full detail (not yet backfilled there as of
2026-07-16 — flagged as a gap during the same audit, along with the two
entries above).

### Mainstream Discover: trailer link + hide not-yet-released movies — shipped 2026-07-16
First item off the "least complex to most complex" backlog ordering. Two
additions. (1) A "Watch Trailer" link in the detail popup (Movies/Series
only, not Adult), opening the title's YouTube trailer in a new tab —
`internal/tmdb.TrailerURL(ctx, mt, tmdbID)` (`/movie|tv/{id}/videos`,
prefers `official==true` YouTube Trailer, falls back to any YouTube
Trailer then any YouTube video at all), a `TrailerResponse` DTO, and
`GET /api/modes/{mode}/discover/trailer?tmdbId=N` (`internal/api/
discover_trailer.go`, same one-shot-per-popup-open trigger shape as
`discoverAvailabilityHandler`; 400 for Adult and for `tmdbId<=0`). Renders
next to the existing "More on TMDB →" link in `DetailPopup.tsx`. (2) Hides
movies from Trending Movies and Popular Movies (not Upcoming Movies, not
Series) with no US digital/physical release yet —
`internal/tmdb.HasUSRelease(ctx, tmdbID)` (`/movie/{id}/release_dates`,
type 4/Digital or 5/Physical dated today-or-earlier counts as released),
wired into `discoverHandler`'s trending/popular dispatch via
`filterReleasedMovies`/`filterByUSRelease` (bounded-concurrent,
`golang.org/x/sync/errgroup` `SetLimit(5)`, now promoted from an indirect
to a direct `go.mod` dependency). Two real edge cases handled, not just
noted: (a) if an entire fetched TMDB page filters to empty, the handler
retries up to 3 more consecutive TMDB pages before giving up — otherwise
`Mainstream.tsx`'s `PaginatedRow` would mark the row falsely exhausted on
its `batch.length === 0` check; (b) `filterByUSRelease` fails OPEN on a
per-item `HasUSRelease` error (logs and keeps the item) rather than
blanking the whole row over one transient TMDB hiccup — found and fixed
during this change's own pre-merge code review, matching the
never-an-error posture `fetchTitlePoster`/`posterHandler` already use.
Accepted, documented limitation: since the frontend's own page counter
doesn't track which raw TMDB page a retry burst actually consumed, a
retry that skips past a PARTIALLY-filtered page can make its survivors
render twice on a later "Show more" click (cosmetic only — Solid's
`<For>` keys by object reference, no crash) in the narrow case where a
partial-filter page sits immediately next to a fully-empty one being
retried past; a full fix would need a bigger wire-contract change
(returning which raw page was consumed), judged out of scope for this
pass. Both new TMDB methods are flagged "UNVERIFIED ASSUMPTION" per this
project's honesty convention — neither endpoint had been called live by
this codebase before. Independently code-reviewed pre-merge (0 CRITICAL,
0 HIGH; the 2 MEDIUM findings — fail-open filtering and an error-path
test gap — were fixed before merge; 3 LOW findings addressed or accepted).

### Logical episode-splitting — shipped 2026-07-16
Second item off the "least complex to most complex" backlog ordering — but
turned out more complex than its one-line ROADMAP description suggested,
per a design pass done before implementation (see the "Load-bearing
decisions" section this entry summarizes). One video file that's actually
two (or more) bundled Series episodes (e.g. `Show.S01E01-E02.mkv`) now
records one `library.Episode` row per bundled number, all pointing at the
SAME `FilePath` — no re-encoding, no physical splitting (that stays
explicitly out of scope).

New `library.ParseEpisodeNumbers(name) (season int, episodes []int, ok bool)`
extracts ALL bundled episode numbers — concatenated (`S01E01E02E03`), dash
range (`S01E01-E02`/`S01E01-02`, inclusive expansion capped at 26 to reject
a pathological `S01E01-E99` misparse), and the alt `01x01-02` format.
`ParseEpisodeFilename` is now a thin wrapper returning just the first
number — every existing single-episode caller's behavior is unchanged
(verified: its own pre-existing test still passes verbatim). New
`proposals.Proposal.ExtraEpisodeNumbers []int` (migration `0030`,
JSON-encoded column, empty string = none) carries the bundled numbers
through Scan → Apply. `rename.ApplyLibrarySeries` relocates the file
exactly ONCE via a new `RelocateEpisodeRange`/`naming.EpisodeRangeFileName`
(renders `S03E05-E06`), then upserts one `Episode` row per number —
including the SAME existing-metadata-preserve dance (`GetEpisode` before
`UpsertEpisode`) the primary episode already got, so a bundled episode's
prior TMDB-seeded title/air-date isn't blanked. `naming/schema.go`'s
conformance regexes recognize the range shape too, so a correctly-split,
already-renamed file isn't endlessly re-proposed. Search's check-import
(`internal/api/search.go`) got the same fix for a directly-grabbed
multi-episode file — a confirmed pre-existing bug where every episode past
the first was silently dropped forever is now fixed.

**The real complexity, found during a research pass before any code was
written**: Dedup's `ApplyLibrarySeries` (`internal/dedup/dedup.go`) used to
delete a losing duplicate candidate's file unconditionally per
`(series, season, episode)` key, with no awareness that the SAME file could
be a DIFFERENT episode's tracked `FilePath` (the split scenario) — a live,
reachable violation of this project's core "no drift" mission (CLAUDE.md's
Mission section), not a hypothetical. Fixed via a new
`library.Store.CountEpisodesByFilePath(ctx, filePath) (int, error)`: before
deleting any losing candidate's file, Dedup now checks whether any OTHER
episode row still references that exact path (count > 1) and skips the
physical delete if so (logging why), while still letting this proposal's
own key advance to its winner. Purge's `ApplyLibrarySeries` needed no
equivalent fix — it deletes an entire series' episodes in one atomic call,
so split siblings always die together — but did get a smaller fix found in
the same review: it was double-counting a shared file's deletion in its
returned `PathChange` list (cosmetic, not data-loss, but corrected).

Independently code-reviewed pre-merge (`oh-my-claudecode:code-reviewer`,
fresh context, own advisor consultation): 0 CRITICAL, 0 HIGH at HIGH
confidence — APPROVE. The reviewer traced the Dedup fix's ordering
(refCount check reads the OLD DB state, before the winner's own
`UpsertEpisode`) and confirmed the critical regression test
(`TestApplyLibrarySeries_SharedFileLosesItsOwnKey_NotDeleted_SiblingIntact`)
is genuine, not vacuous. One Open Question was raised (the guard's
correctness depends on exact `file_path` string equality between sibling
rows) and closed before merge: confirmed every writer of split-sibling rows
upserts all numbers with the identical already-relocated path string in
one call (never re-derived per row), and — separately — that
`ScanLibrarySeries`'s own `known`-path masking means a shared file can
never surface as a scan-discovered orphan with a differently-formatted
path in the first place; documented directly on
`CountEpisodesByFilePath`'s doc comment. A second, path-based (not
candidate-label-based) regression test was added to demonstrate the guard
generalizes correctly. Purge's duplicate-PathChange fix also got its own
regression test.

Verified via `go build`/`go vet`/`go test -race` across every touched
package (`library`, `proposals`, `naming`, `rename`, `dedup`, `purge`,
`api`) plus full repo `go build ./...`/`go test ./...`, and frontend
`pnpm typecheck`/`pnpm test` (273 tests, up from 272)/`pnpm build`, all
clean. Merged, pushed, auto-deployed, health checks passed.

**Follow-up (same day):** the review's one remaining LOW finding (the
multi-episode upsert loop wasn't transactional) was closed — see
CHANGELOG.md's "transactional multi-episode upserts" entry.

---

## Backlog (not yet started, roughly in discussion order)

### Multi-idea planning session (2026-07-31) — active, 11 items, specs banked incrementally
Wade brought 11 feature ideas in one session, each getting its own full
deep-interview per `CLAUDE.md`'s mandatory pipeline. This entry is updated
after every spec lands and reconciled fully once the whole session
completes (originally planned as a one-time end-of-session reconciliation;
changed to update-as-you-go 2026-07-31 at Wade's request). None of these
are authorized to build yet — every spec below is `pending approval`,
**except items 3 and 12, which shipped 2026-08-01, and item 1, which shipped
2026-08-02** (see each item's own "Shipped" block below, plus
`CHANGELOG.md`). That blanket claim is
corrected here rather than left to go stale silently — it is the same class
of "was true when written" statement this file and `CLAUDE.md` both make a
habit of superseding in place.

**Confirmed interview order:** (1) Connections tab elimination + Usenet
settings → (2) Grabs/Requests/Downloads side-tab grouping → (3) Discover
scheduled refresh → (4) Torrent behavior settings → (5) Download progress
bar/seeds → (6) Calendar → (7) Adult pop-up enrichment → (8) Series
monitoring/auto-grab (last, highest architectural weight). Items are
numbered below by original idea order, not interview order.

1. **Adult rows configurable in 2 places, no duplication** — spec ready:
   `.omc/specs/deep-interview-adult-rows-config-unification.md` (16%
   ambiguity, PASSED). Unifies Settings' `AdultRowAdmin` and Discover's
   Adult edit mode onto one canonical `sort_order` store; deletes the
   StashDB/FansDB structural-rows feature entirely.

   **Shipped 2026-08-02** — see `CHANGELOG.md` and `CLAUDE.md`'s CORRECTED
   2026-08-02 note under the adult-popup-enrichment clarification. Built via a
   3-wave autopilot split per
   `.omc/plans/autopilot-impl-adult-rows-config-unification.md`. What actually
   landed, and the parts a future session needs before touching it:

   - **One order, two surfaces, one column.** Both Settings' Adult Rows tab
     and Discover's Adult edit mode now read `adultnewest`'s `List` (ORDER BY
     `sort_order` ASC) and write `POST /api/modes/adult/newest-rows/reorder`,
     through a new shared hook `frontend/src/screens/discover/useAdultRowOrder.ts`.
     Adult's old per-screen KV order (`useRowOrder("adult", …)`) is retired.
     There is **no migration and no backfill** — an order previously set only
     via Discover's drag-and-drop is reset to `sort_order`, a deliberate,
     accepted one-time reset (single operator), not a bug.
   - **`useRowOrder` and both `/api/discover/{row-order,row-hidden}/{screen}`
     routes deliberately SURVIVE** — Mainstream is still a live consumer, so
     deleting them was never on the table. That means nothing structural stops
     Adult from quietly re-acquiring a second, divergent order source; a test
     is what stops it (`Discover.test.tsx`, "fires ZERO requests to
     /api/discover/row-order/adult or /row-hidden/adult"), following the
     `TestAdultDescriptionMakesNoProwlarrCall` precedent `CLAUDE.md` names for
     guarantees of this shape.
   - **AdultRowAdmin renders Discover's `<RowEditor>` verbatim**, not a second
     drag implementation and not a shared extracted primitive — its ▲▼ buttons
     and hand-rolled list are gone. RowEditor gained four additive, optional
     props to make that possible: `actions?: RowAction[]` (an injected per-row
     control slot), `title?`, `description?`, `footer?`, plus `onToggleHidden`
     becoming optional. **`footer` renders as a SIBLING after the empty-state
     `<Show>`, never inside it** — inside, a fresh install shows "No rows yet."
     with no way to create a first row. Discover passes none of the four, so
     its own surface is unchanged, which is the AC5 deliverable.
   - **The Edit affordance had to be carried explicitly.** RowEditor's built-in
     controls are enabled-toggle + Delete only, so adopting it as-is would have
     deleted the app's ONLY place to edit a row's title/rowType/genreFilter.
     It survives as the one injected `RowAction`. `RowAction.icon` is typed
     `JSX.Element` (not `string`) so item 2's real-icon-library swap is a
     call-site-only change.
   - **The StashDB/FansDB structural rows are gone end to end** — twelve
     backend routes, their handlers, the five frontend fetchers in
     `api/discover.ts`, and the `AdultDrill` box variant. **`internal/stashbox`
     is untouched** (same precedent as `internal/availability`: the Go package
     keeps its still-live callers in `internal/parseentity` and
     `internal/identify`; only the Discover-facing HTTP surface was removed).
     Two files were GUTTED rather than deleted, because their names lied about
     their contents: `adultdiscover_stashbox.go` also held the live
     `adultDiscoverMergedRecentHandler`, and `adultdiscover_stashbox_test.go`
     is the `internal/api` package's shared test-helper file (`overrideFixedURL`
     alone has 19 external users). Both were renamed to `*_merged*` to match
     what they actually contain.
   - **Known temporary inconsistency, by design:** `AdultRowAdmin`'s header
     used to say reordering was button-based "same as SliderAdmin." SliderAdmin
     and RssFeedAdmin still use ▲▼ — converting them is item 2's job — so that
     comparison is annotated as temporarily false rather than silently dropped.
     - **RESOLVED 2026-08-02 by item 2.** The comparison is true again, in the
       opposite direction: all three screens drag, via one `RowEditor`. The
       in-file annotation was flipped rather than deleted. Note the bullet
       above was **wrong about RssFeedAdmin even when written** — it never had
       ▲▼ buttons; it had its own full `@thisbeyond/solid-dnd` wiring, a
       parallel duplicate of RowEditor's. Correct history: SliderAdmin ▲▼ →
       RowEditor drag; RssFeedAdmin duplicate solid-dnd → the same shared
       RowEditor.
2. **Live drag-and-drop row reordering** — spec ready:
   `.omc/specs/deep-interview-row-dnd-consolidation-and-pagination.md`
   (15% ambiguity, PASSED). Converts SliderAdmin off button-based reorder,
   consolidates RssFeedAdmin's independent drag-and-drop into a shared
   `RowEditor.tsx` with a new icon-based extra-actions slot; grew mid-interview
   to also cover a Discover row-pagination redesign (arrow overlay on the
   last poster, replacing the edge chevrons; native scrollbar hidden).

   **Shipped 2026-08-02** — see `CHANGELOG.md` and `CLAUDE.md`'s frontend
   note of the same date. Built via a 6-wave autopilot split per
   `.omc/plans/autopilot-impl-row-dnd-consolidation-and-pagination.md`. What
   actually landed, and the parts a future session needs before touching it:

   - **There is now exactly ONE `RowEditor` and zero solid-dnd wiring anywhere
     else in the frontend.** SliderAdmin, RssFeedAdmin, AdultRowAdmin,
     Mainstream and Adult Discover all render it. SliderAdmin's ▲▼ pair,
     `SliderRow`, `move()`, and RssFeedAdmin's whole parallel solid-dnd block
     and `FeedRow` are gone.
   - **`reorderAdultFeedIds` (`settings/RssFeedAdmin.tsx`) SURVIVES and is NOT
     drag wiring.** It sat immediately below the deleted solid-dnd imports and
     looks like part of them; it is the adult-subset→full-id-list mapping, and
     without it a reorder 422s as `ErrReorderMismatch` on any install that also
     has a movie/tv feed. **Do not delete it in a future dedup sweep.**
   - **First icon library.** `lucide-solid` (3 runtime deps → 4), deep
     per-icon imports only. See `CLAUDE.md` for the resolution hazard.
   - **Rows on both Settings screens are title-only now.** SliderAdmin's
     filter summary moved into `SliderForm`; RssFeedAdmin's protocol moved into
     a new `ProtocolStatusDialog` (status-only, never collects a protocol —
     `ProtocolFallbackDialog` is still the sole manual-pick path).
   - **Discover's rows deliberately gained NO actions**, enforced by test
     (`Discover.test.tsx`, "the row editor carries NO per-entity actions"),
     following the `TestAdultDescriptionMakesNoProwlarrCall` precedent.
   - **Accepted accessibility trade-off:** SliderAdmin's ▲▼ removal drops
     keyboard-only reorder access on that one screen (single-operator,
     low-stakes; row order is cosmetic).
   - Bundle: 105.0 KB → 106.1 KB gzipped JS, against a 200 KB soft ceiling.

   **Two deferred follow-ups, neither built:**
   - **(a) Optimistic reorder override for the two Settings surfaces.** Both
     `reorderSliders` and `reorderRssFeeds` return 204 with no body, so the new
     order only appears after the follow-up `refetch()` — between drop and
     refetch the dragged row snaps back to its original slot, then corrects.
     Accepted as-is because RssFeedAdmin already shipped exactly this behaviour
     and it went unremarked; SliderAdmin simply joins it (a button press hid it
     before, a drag does not). Revisit only if it proves annoying in practice.
     The shape would be generalising `useAdultRowOrder`'s override signal,
     which is today typed to `AdultNewestRow[]`/`reorderAdultNewestRows`.
   - **(b) Unify `PaginatedStrip`'s scrollbar treatment with Carousel's.** The
     `no-scrollbar` utility is deliberately scoped to Carousel only, so
     Mainstream/Trakt/slider/RSS rows have no visible scrollbar while Adult
     Discover's rows (which use `PaginatedStrip`,
     `screens/discover/shared.tsx`) still do. That asymmetry is spec
     scope-boundary compliance, not an oversight. If it proves confusing it is
     a one-class change on `PaginatedStrip`'s own `overflow-x-auto` track.
3. **Eliminate Connections tab; Usenet multi-subscription settings** — spec
   ready: `.omc/specs/deep-interview-connections-elimination-usenet-multisub.md`
   (12% ambiguity, PASSED). Every one of the 9 services + Trakt + AI
   individually placed (not bulk-grouped); new Advanced → API section adds
   real Emby/Plex clients alongside Jellyfin, with per-connection library-mode
   assignment; Usenet gets a dedicated multi-subscription page with a
   parallel-query-and-score download model. **Introduces a genuine, explicit
   staged-for-approval exception (auto-grab, off by default via a global
   toggle) — same documentation obligation as the existing bulk-apply/
   bulk-grab amendments in `CLAUDE.md` once this is actually built.** This
   auto-grab mechanism is explicitly designed to be shared with item 10
   below (series monitoring), not rebuilt twice.
   <!-- Stale cross-reference, corrected 2026-08-01 rather than rewritten:
        series monitoring is item 9, not item 10 (item 10 is the Downloads
        progress bar). The number drifted during this session's renumbering. -->


   **Shipped 2026-08-01** — see `CHANGELOG.md` and `CLAUDE.md`'s AMENDED
   2026-08-01 staged-for-approval note. Built via a 4-wave, 12-task autopilot
   split per `.omc/plans/autopilot-impl-connections-elimination.md` (plan
   Critic-reviewed and revised 4 times before any executor ran). What
   actually landed, and the parts a future session needs before touching it:

   - **The registry is a SPLIT, not a replacement.** Migration `0053` adds a
     new `service_connections` table (+ `service_connection_modes`) and moves
     exactly TWO rows out of `connections` — `nntp` and `jellyfin`. The
     `connections` table and `internal/connections` survive unchanged for the
     ~7 genuinely-singleton services (TMDB, TVDB, StashDB, FansDB, TPDB,
     Trakt, AI), which is what the spec's own constraint required. New
     package `internal/serviceconn`, deliberately not named `connections`.
     Migration `0054` adds the retry columns to `grabs`. The registry's Down
     migration is deliberately lossy (the old `PRIMARY KEY(service)` shape
     cannot represent multiple rows per provider) and says so in-file.
   - **Real Emby and Plex clients** (`internal/emby`, `internal/plex`) now sit
     alongside Jellyfin under Settings → Advanced → API Connections, each with
     per-connection mode assignment (many-to-many, no exclusivity) replacing
     the old hardcoded `Jellyfin = Movies/Series` scoping. **Stash was
     deliberately NOT folded into the player registry** — its notify path
     splits Created/Modified → `RescanPaths` vs Deleted → `CleanMetadata`,
     which `mode.go` calls the single most important correctness guardrail in
     that feature; a uniform `PlayerNotifier` interface would dilute or hide
     it. Stash stays a singleton `connections` row.
   - **Usenet is genuinely multi-subscription now**, with its own Settings
     tab (`frontend/src/screens/settings/Usenet.tsx`), and the boot-time
     singleton engine became runtime-reconfigurable
     (`usenet.Manager.SetSubscriptions`) — before this, adding a subscription
     in the UI did nothing until process restart. **The page must never grow
     a priority/ordering field or a retry-interval input**; both are
     documented in its header as permanent non-goals.
   - **The shared auto-grab mechanism item 9 (series monitoring) is meant to
     reuse is `RunAutoGrab` in `internal/api/autograb_shared.go`** — one
     gated scoring-and-dispatch path, called with an `AutoGrabRequest` and an
     `AutoGrabTrigger`. **`TriggerAirDate` already exists as an
     unimplemented const**, exactly so that adding series monitoring is one
     new caller and nothing else. Do not build a second scoring, dispatch or
     toggle-gating path — the toggle gate lives inside `RunAutoGrab` and
     nowhere else, which is what makes that promise real rather than
     aspirational. The same applies to item 7's stale-torrent requeue and
     item 8's pre-release requests: they are additional *callers*, not
     additional retry systems.
   - **The retry system items 7/8/9 forward-reference is
     `internal/api/usenetretry.go` (`RunUsenetRetry`)** — one loop, now **four**
     passes per cycle (**CORRECTED 2026-08-02, was "two jobs per cycle"**): the
     authoritative sweep converting asynchronous usenet retrieval failures into
     `pending_retry`/`failed`; the re-search of every due row; series air-date
     monitoring (`monitorAirDates`); and pre-release request promotion
     (`releaseDueGrabs`, shipped by `calendar-grabs-requests`). Off by default;
     its interval is written server-side as a coupled side effect of the
     auto-grab toggle so the two can never disagree.
   - **Two things worth knowing before extending this, both discovered during
     implementation and neither predicted by the plan:** (a) the toggle-ON
     Search hook can never actually auto-grab — a query-only search carries
     no runtime, and `GradeCandidate` short-circuits on missing size/runtime
     *before* the Lossless bypass the plan expected to save it, so every
     toggle-ON search parks a retry row rather than grabbing; (b) a
     successful retry must RE-ARM its existing row
     (`AutoGrabRequest.ExistingGrabID` → `grabs.Relaunch`), because creating
     a fresh row would leave the original due-in-the-past and the next cycle
     would grab the same release again — and the existing GID dedup guard
     cannot catch that, since `AddNZB` mints a fresh GID per call.
   - **`RunUsenetRetry` is the first scheduler in this codebase that
     dispatches downloads rather than proposing work for review**, which
     falsifies the Scan-only claim in `CLAUDE.md`'s AMENDED 2026-07-23 note.
     That note is corrected there, not silently extended.
   - **This entry's own "parallel-query-and-score download model" wording
     above describes the spec's literal reading, which was deviated from —
     deliberately and with the deviation documented, not silently.** NNTP has
     no search verb (only retrieval by message-ID), so "query all
     subscriptions in parallel and score the results" is not implementable as
     written. What ships is a two-stage pipeline: score Prowlarr candidates
     first, then fan out *retrieval* across subscriptions. Nothing in the
     spec is lost under that reading — the full per-criterion mapping is in
     `CLAUDE.md`'s AMENDED 2026-08-01 note and the `CHANGELOG.md` entry.
4. **Grabs/Requests/Downloads grouped under a side tab** — **shipped 2026-08-01**:
   `.omc/specs/deep-interview-queue-navigation-grouping.md` (~9.5%
   ambiguity, PASSED). New sidebar entry "Queue" (Downloads → Requests →
   Grabs sub-tabs, Downloads default), reusing `Organize.tsx`'s exact
   existing client-side-tabs pattern — no new nav mechanism, no
   URL-addressable sub-routes. Pure navigation restructuring; built
   directly on the companion spec below, which shipped earlier the same
   session.
   - **Shipped with the ORIGINAL Grabs order, not item 8's amended Calendar
     order — deliberately, not by oversight.** Item 8's Calendar spec is
     still unbuilt as of this ship date, so building against it directly
     would have meant designing against a screen that doesn't exist yet
     (see `.omc/plans/roadmap-implementation-sequencing.md:104-106`, which
     explicitly adjudicated this exact tradeoff and rejected pulling
     Calendar forward). **The Grabs → Calendar swap described below remains
     a real, pending amendment** — whoever implements item 8's Calendar
     spec must still perform it as that spec's own scope, not assume it
     already happened.
   - **Directly builds on an already-passed, still-unbuilt companion
     spec**: `.omc/specs/deep-interview-requests-downloads-remove.md` (9.5%
     ambiguity, PASSED, generated same day 2026-07-31, predates this
     planning session). Adds Remove/exclude to Requests, Cancel-with-
     file-deletion + bulk multi-select + a global download-pause to
     Downloads; confirms Grabs stays read-only, untouched.
   - **AMENDED 2026-08-02 — the pending swap has been PERFORMED, by item 8's
     Calendar work, exactly as this entry said it would have to be.** Two
     things above are now superseded, quoted verbatim rather than rewritten in
     place. First, this entry's own summary line: *"(Downloads → Requests →
     Grabs sub-tabs, Downloads default)"* — **corrected to Downloads →
     Requests → Calendar, Downloads still the default.** Second, the bullet
     that deferred it: *"**The Grabs → Calendar swap described below remains a
     real, pending amendment** — whoever implements item 8's Calendar spec
     must still perform it as that spec's own scope, not assume it already
     happened."* — **performed 2026-08-02.** The standalone Grabs screen
     (`frontend/src/screens/Grabs.tsx` + its test) was **deleted outright**
     rather than kept alongside Calendar, per the "no dead code left behind"
     convention; Calendar's History view carries the read-only
     completed/imported grab log forward. `APP_ROUTES` and the sidebar counts
     in this entry are unaffected — Calendar never had a route of its own. See
     item 8 below, `CLAUDE.md`'s CORRECTED 2026-08-02 note on the Queue
     sidebar grouping entry, and `CHANGELOG.md`.
5. **Discover scheduled background refresh** — spec ready:
   `.omc/specs/deep-interview-discover-scheduled-refresh.md` (~11%
   ambiguity, PASSED). Misdiagnosis corrected during interview: Adult
   newest-releases rows already background-refresh via
   `internal/adultnewest` — the real gap is 4 other row categories making
   a live external API call on every page load with zero caching
   (Mainstream TMDB Trending/Popular/Upcoming, custom Discover sliders,
   Trakt watchlist, Adult StashDB/FansDB catalog rows). One shared new
   scheduler, on by default, 24h global interval, immediate one-off
   populate on slider-create/Trakt-connect, plus an operator-triggered
   manual refresh for all 4 sources as a troubleshooting backup.

   **CORRECTED 2026-08-03 — the "4 other row categories" / "all 4 sources"
   claims above are stale, retracted here rather than silently rewritten.**
   The superseded clause, quoted verbatim: *"the real gap is 4 other row
   categories making a live external API call on every page load with zero
   caching (Mainstream TMDB Trending/Popular/Upcoming, custom Discover
   sliders, Trakt watchlist, Adult StashDB/FansDB catalog rows)."* Commit
   `e25274f` (Adult row ordering unification, shipped 2026-08-02 — see that
   entry below) deleted every StashDB/FansDB Adult Discover browse
   route/handler before this feature's implementation started, so there was
   nothing left for a fourth source to cache. **This feature caches THREE
   sources, not four: Mainstream TMDB rows, custom sliders, and the Trakt
   watchlist.** Stash-box Adult catalog rows are unaffected functionally —
   they still work exactly as before, live, via the same cold-miss
   fall-through this feature's other three sources use — they are simply
   not warmed by a background scheduler, because the read path they would
   have warmed no longer exists.

   **Shipped 2026-08-03** — see `CHANGELOG.md` and `CLAUDE.md`'s AMENDED
   2026-08-03 note under **Automation** (the scheduler count) and under
   **TMDB response cache** (the consumer-list correction). Four corrections
   a future reader most needs, found during planning and verified against
   source rather than inferred:
   - **§0.1 — there is no single `adultDiscoverStashBoxHandler`.** The
     spec's Technical Context named one function that never existed; the
     real surface was five constructors registering six route templates ×
     2 boxes = 12 routes (moot now that the routes themselves are deleted,
     but recorded so a future reader doesn't go looking for the wrong
     symbol in old specs/plans).
   - **§0.2 — three of the (now-deleted) stash-box routes had zero frontend
     call sites** (`stashdb/recent`, `stashdb/studios`, `stashdb/performers`)
     even before `e25274f` removed them — the live-rendered set was always
     five of twelve, not eight, and warming the other seven would have been
     pure waste.
   - **§0.3 — the cached TMDB key space is exactly the six Mainstream rows,
     nothing else.** `discoverHandler` serves seven categories
     (`trending`/`popular`/`upcoming`/`genre`/`studio`/`network`/`filter`);
     the last four are operator-driven browse actions with an unbounded or
     large cross-product key space and stay live, uncached, same carve-out
     shape as the Adult drill-downs.
   - **§0.4 — caching had to move onto the write path, not just wrap the
     read path.** Movies trending/popular additionally run a
     post-list US-release filter (up to 20 live TMDB calls per page,
     `internal/api/discover.go`). Caching pre-filter items would have left
     the read path still making those calls on every render, so the
     scheduler filters as it accumulates and the read path serves an
     already-filtered list with zero calls — this also structurally
     prevents a pre-existing, documented duplicate-item edge case from ever
     surviving inside the cached window.

   **AC1 is a steady-state property, stated so out loud a future reader
   doesn't read it as a gap.** "Discover hits zero live external APIs"
   holds from the first completed refresh cycle onward, not on a genuinely
   cold install with no cache row ever written — a cold miss falls through
   to today's live handler and behaves exactly as it does now, the same
   in-repo precedent migration `0045`'s (now-superseded) cache table set.
   Because the scheduler boot-polls anything due and is on by default, the
   cold window in practice is one cycle's duration at first start, not 24h.
6. **Adult pop-up enrichment** (ratings/description/tags; performer
   bio/tags) — spec ready: `.omc/specs/deep-interview-adult-popup-enrichment.md`
   (~15% ambiguity, PASSED). **Real finding: description/bio are
   genuinely absent from every currently-queried catalog** (TPDB REST,
   StashDB, FansDB, and the local Stash instance) — not a wiring gap.
   Direction corrected mid-interview away from an initially-proposed
   AI-synthesis fallback (explicitly rejected) toward extending all three
   catalogs' own queries for a `details`-style field (mirroring how
   Stash's own built-in scraper pulls this data — TPDB's GraphQL API,
   untested in this codebase today, flagged for live verification during
   implementation). StashDB/FansDB tags get a real, low-risk fix (already
   in the API, browse query just never requests them); their rating stays
   genuinely absent (no such field exists) rather than faked. Performer
   bio/tags render as a header above the existing scene-grid drill-down —
   no new popup UI. Explicitly flags the same open item as item 5's spec:
   needs a design pass before/during implementation.

   **CORRECTED 2026-08-02 — this item's own "Real finding" above was itself
   wrong, retracted here rather than silently rewritten** (same convention
   item 8's "zero new backend needed" retraction uses). The superseded
   sentence, quoted verbatim: *"Real finding: description/bio are genuinely
   absent from every currently-queried catalog (TPDB REST, StashDB, FansDB,
   and the local Stash instance) — not a wiring gap."* Live-verified
   2026-08-02 (TPDB's OpenAPI schema plus GraphQL introspection against both
   stash-box instances, not inference): TPDB REST's `SceneResource` already
   carries `description`, and `PerformerResource.bio` / `SiteResource.description`
   exist too — so the TPDB-GraphQL workstream this item pointed toward was
   closed as unnecessary, not deferred; the REST client needed three
   additive decode fields, nothing else. The following sentence above is
   also retracted: *"Performer bio/tags render as a header above the
   existing scene-grid drill-down — no new popup UI."* Half right: the bio
   banner does render as a header above the drill-down, but StashDB/FansDB
   expose neither a performer bio nor a studio description field at all
   (proven by complete GraphQL field enumeration, not inferred), and no
   catalog — TPDB included — exposes performer/studio tags, so the "tags"
   half of this item does not ship. The scene-tags half above is unaffected
   and shipped as described: StashDB/FansDB's browse query genuinely never
   requested `tags`, that was a real, low-risk fix.

   **Shipped 2026-08-02** — see `CHANGELOG.md` and `CLAUDE.md`'s 2026-08-02
   clarification under "Discover never queries Prowlarr, full stop." What
   shipped, against the corrected finding above:
   - **Scene tags (AC1)**: StashDB/FansDB's browse query now requests
     `tags`, populating `Genres` on stash-box scene cards. The drill-down
     enrichment path (`identify.fetchCatalog`) shares the same query and
     gains tags as a documented side effect — a scene already cached in
     `adult_newest_scene_matches` before this change keeps an empty
     `Genres` indefinitely (that table has no TTL, prune, or invalidation);
     only freshly-matched scenes gain tags.
   - **Scene description (AC3/AC4)**: TPDB REST's `SceneResource.description`
     and StashDB/FansDB's `Scene.details`, surfaced through a new dedicated
     endpoint (`GET /api/modes/adult/discover/description`), fired once per
     popup or drill-down open, strictly following the scene's own
     `entity_source`.
   - **Performer/studio bio (AC6, narrowed)**: a bio banner above the
     performer/studio drill-down, sourced from TPDB's `PerformerResource.bio`/
     `SiteResource.description` — always resolved via TPDB regardless of the
     drill's own catalog source (Wade's Option A decision, 2026-08-02),
     specifically for banner coverage on all four drill-down entry points.
     Costs two upstream TPDB calls (name resolve + detail fetch) for a
     stash-box-sourced entity; still one call per explicit operator action,
     still zero Prowlarr.
   - **Placeholder removal (AC5), cross-mode**: the "No description
     available." fallback in `DetailPopup.tsx` is gone in all three modes,
     one consistent rule (Wade's locked decision #3) — a TMDB title with an
     empty `overview` now renders no line at all instead of the placeholder
     sentence. Required its own PR callout so this doesn't read as Adult
     scope leaking into Movies/Series.
   - **Dropped (mandatory, not a judgment call)**: performer/studio tags. No
     source exists in TPDB, StashDB, or FansDB (complete field enumeration,
     live-verified 2026-08-02) — do not add a tags pill row here; there is
     nothing in any configured catalog to bind it to.
7. **Torrent behavior settings + seeding + stale torrent handling** — **SHIPPED
   2026-08-01** (full wave: BE-0 gate closed 2026-08-01; BE-1–BE-6 + FE-1–FE-2
   + QA-1 executed same day; all tests passing, `go build/vet/test -race` green,
   frontend `pnpm typecheck/test/build` clean).
   Spec: `.omc/specs/deep-interview-torrent-behavior-and-stale-handling.md`
   (~12% ambiguity, PASSED). Grew mid-interview at Wade's request to fold
   in "stale torrent handling" (thematically adjacent). New Settings
   section exposes staging dir/max-connections (backend already existed,
   UI was missing) plus download rate limit, DHT/PEX/listen port, and
   obfuscation mode (previously hardcoded to library defaults). **Note: Local
   peer discovery was confirmed absent in anacrolix/torrent v1.61.0 and is not
   exposed** (documented AC1 deviation, disclosed in CHANGELOG).
   **Real architectural finding**: seeding (`Seed=true`) can't just be flipped
   on — completed files are `os.Rename`'d out of staging immediately, so
   there'd be nothing left to seed. Resolved via copy-then-move: a
   staging-side seeding copy under `<staging>/.import/<gid>/…` persists until
   a ratio or duration limit is hit (whichever first), then gets deleted; the
   existing library-relocation flow is unchanged (works on the copy paths, not
   the originals). Stale torrents (zero progress, no peers, default 4h
   threshold, off by default) auto-cancel + clean up (reusing item 4's
   companion Cancel-with-file-deletion spec) and then **requeue into Requests
   via the same shared retry mechanism item 3's Usenet spec already built** —
   one retry system, three trigger sources now (Usenet no-match, stalled
   torrents, and pending for series-monitoring when it ships, item 9).
8. **Calendar for Grabs/Requests** (grid + list, upcoming + history) —
   **SHIPPED 2026-08-02** (BE-0 gate closed 2026-08-02 with both blocking
   questions decided by Wade; Waves 0–3 executed the same day). Plan:
   `.omc/plans/autopilot-impl-calendar-grabs-requests.md`. Spec:
   `.omc/specs/deep-interview-calendar-grabs-requests.md` (~12%
   ambiguity, PASSED). **Replaces Grabs as Queue's 3rd sub-tab** (amends
   item 4's spec — see its note above), not an addition. History =
   completed/imported grabs only, calendar-organized (drops the
   in-progress/failed detail Grabs shows today, already visible
   elsewhere). Upcoming is deliberately **asymmetric**: Series stays
   bounded to already-tracked shows (`MissingEpisodes`' real air dates,
   zero new backend needed); Movies are **open-ended** — any TMDB movie
   with a digital/streaming-or-planned release date (region-aware, same
   pattern as the existing `HasUSRelease` check), clickable to create a
   real Request. Pre-release requests stay dormant (no search attempts)
   until the actual release date passes, then join the normal
   Missing-request pipeline — including item 3's shared retry/auto-grab
   mechanism as a third trigger source. Loosely overlaps this file's
   pre-existing "Request-status view" backlog item below — worth
   reconciling before both get built, not yet done.
   - **RETRACTED 2026-08-02 — this entry's "zero new backend needed" claim
     about Series' Upcoming was FALSE, and finding out why was this feature's
     headline blocking discovery.** The superseded clause, quoted verbatim from
     the paragraph above rather than deleted: *"Series stays bounded to
     already-tracked shows (`MissingEpisodes`' real air dates, zero new backend
     needed)"*. It restates the spec's own **"No new backend tracking needed;
     this data already exists."** Both are wrong on **any install where
     unattended auto-grab is switched off, which is every install by default**,
     and the call graph was verified end to end: `MissingEpisodes` needs
     `library_episodes` rows with an empty `file_path` and a populated
     `air_date`; the only thing in the codebase that writes either is
     `syncSeriesCatalog`; its only caller was `monitorAirDates`; that runs only
     inside `runUsenetRetryCycle`; that loop returns immediately without
     starting when `usenet_retry_interval_seconds` is 0, which is what
     `usenet_autograb_enabled` (default **false**) writes server-side. So
     Series' Upcoming would have rendered **permanently empty**, gated on an
     unrelated toggle. **Not a defect this spec introduced** — an inherited
     property of where item 9's `series-monitoring-autograb` put the sync,
     which was correct for a dispatch feature and wrong for a read-only
     browsing surface. **Resolved by real backend work** (BE-0.1, decided by
     Wade 2026-08-02): `syncSeriesCatalog` was extracted verbatim into a new
     file, `internal/api/seriescatalog.go`, as a caller-driven, TTL-cached,
     per-request-capped function serving both `monitorAirDates` (unchanged when
     auto-grab is on) and Calendar's read path — **metadata becomes
     always-available, dispatch stays gated exactly as before.** A new file
     rather than an export because
     `internal/api/airdatemonitor_static_test.go` fails on any const in
     `airdatemonitor.go` whose name merely CONTAINS `"Interval"`; that test
     passes unmodified. **This adds no seventh scheduler** — see `CLAUDE.md`'s
     AMENDED 2026-08-02 note under **Automation**.
   - **AMENDED 2026-08-02 — the "worth reconciling, not yet done" closing
     sentence is stale.** It was reconciled 2026-08-01, before this shipped:
     see the **Request-status view** entry below, which records the decision
     that the two views deliberately do **not** merge — same underlying
     missing-episode data, different lens (date-organized vs.
     status-organized), both kept as independent views.
   - **What shipped, and the load-bearing decisions behind it.** The
     pre-release hold is a real schema-level origin marker — a new
     `grabs.hold_until` column plus a partial unique index (migration `0057`)
     — chosen by Wade over a cheaper `retry_after` reuse specifically because
     inferring origin from row shape and a reason string caused a HIGH-severity
     bug in item 9's work days earlier. Dispatch is a **fifth**
     `AutoGrabTrigger`, `TriggerPreRelease`, through the one existing
     `RunAutoGrab` gate; it introduces a new unattended-dispatch category
     ("deferred operator approval": an operator's click, dispatching
     days-to-months later with nobody present), capped at
     `maxPreReleaseGrabsPerCycle = 20`. Full detail, including three
     documented spec deviations, is in `CLAUDE.md`'s two AMENDED 2026-08-02
     notes and in `CHANGELOG.md`.
9. **Series monitoring / true auto-grab** — spec ready:
   `.omc/specs/deep-interview-series-monitoring-autograb.md` (~14%
   ambiguity, PASSED). Turned out remarkably small given the shared
   infrastructure items 3 and 7 already built: **zero new schedulers,
   goroutines, or toggles beyond what's specified** — detection is just
   an added filter (`MissingEpisodes()` + `AirDate<=today` + per-season
   monitored flag) inside item 3's existing auto-grab tick, this
   feature's fourth trigger source (after Usenet no-match, stale-torrent
   requeue). No new persistence either — recomputed live each cycle,
   matching Requests' existing pattern, a deliberate asymmetry from item
   8's Movies-side `PendingPreReleaseRequest`. New scope, added
   mid-interview: **per-season monitored granularity** (not a single
   per-series flag) and a **new-season-discovery toggle** (off by
   default) that checks TMDB for entirely new seasons — when on,
   discovered seasons auto-monitor rather than requiring a second manual
   opt-in step (explicit operator override of the more conservative
   default). Every actual grab this feature causes still requires item
   3's existing "Enable auto-grab" toggle — no new bypass path.

   **Shipped 2026-08-01** — see `CHANGELOG.md` and `CLAUDE.md`'s AMENDED
   2026-08-01 (later same day) scheduler/trigger-count note plus the
   CORRECTED honest-scope note under the auto-grab amendment. Built per
   `.omc/plans/autopilot-impl-series-monitoring-autograb.md` (Critic-reviewed
   twice before any executor ran). Two claims in the entry above are corrected
   rather than rewritten away, per this file's supersede-in-place habit:

   - **"Turned out remarkably small" is WRONG, and an executor told that will
     under-plan the largest piece.** The Pattern B trigger itself genuinely
     was one const-doc edit plus one caller — that part held exactly. But
     `library.MissingEpisodes()` returned the EMPTY SET on every install
     (nothing in the codebase wrote a fileless `library_episodes` row after
     `internal/sonarrimport` was deleted 2026-07-12) and `air_date` was
     structurally always empty for the same reason, so the feature needed a
     full TMDB→`library_episodes` **episode catalog sync** to have any input
     at all — plus migration `0056` + a `library_season_monitored` table, a
     column-scoped `UpsertEpisodeCatalog` writer, an additive
     `tmdb.SeasonDetails` field, a settings toggle with routes, and a
     per-season UI surface. Comparable to or somewhat larger than item 12
     (Library sidebar tab), clearly smaller than item 7.
   - **The trigger-source parenthetical is corrected.** It read *"this
     feature's fourth trigger source (after Usenet no-match, stale-torrent
     requeue)"* — which names two for a "fourth" and miscounts the
     stale-torrent requeue, which is a Pattern A **parker** (it re-arms an
     existing row), not an `AutoGrabTrigger` at all. The accurate statement:
     `TriggerAirDate` is the **fourth `AutoGrabTrigger` const**, after
     `TriggerOperator` (Discover's one-click Grab), `TriggerRequest` (the
     toggle-ON Search hook) and `TriggerRetry` (the due re-search).
   - **Scope additions beyond the spec's AC list, approved rather than
     assumed** (same treatment `torrent_seeding_enabled` got in item 7): an
     **all-seasons-at-once** monitor endpoint
     (`PUT /api/modes/series/library/{seriesID}/seasons/monitored`), because
     absent-means-unmonitored would otherwise make adopting this feature on a
     200-series library a per-season click; and the per-season **UI surface**
     itself, since a monitored flag with no operator surface is inert.
   - **Zero new schedulers held, and is now enforced.** Detection is the
     third pass inside `runUsenetRetryCycle` — a plain function call, no
     goroutine, no ticker, no interval key, no `main.go` line — asserted by a
     static AST test, `internal/api/airdatemonitor_static_test.go`.

**This completes the originally-confirmed 8-item interview order.** Three
items remain queued from mid-session additions (Adult section security,
Library sidebar tab, Streaming media player — items 11-13 below), all
explicitly deferred.
10. **Downloads progress bar with speed + seed count** — **SHIPPED
    2026-08-04** (plan `.omc/plans/autopilot-impl-downloads-progress-speed-seeds.md`,
    all 5 waves executed same day; `go build/vet/test` and frontend
    `pnpm typecheck/test/build` green). Spec:
    `.omc/specs/deep-interview-downloads-progress-speed-seeds.md` (~13%
    ambiguity, PASSED). Fixed the misnomer found during interview: the
    unified downloader's "Connections" field (actually
    `Stats().ConnectedSeeders` for torrents, always-zero-by-omission for
    usenet, no protocol field existed to explain why) is now a
    correctly-labeled, protocol-scoped `seedCount` (torrents only, hidden
    entirely for usenet — not shown as 0/N/A) plus a new `uploadSpeed`
    field and an explicit `protocol: "torrent" | "usenet"` field on
    `apidto.Download`. Real architectural finding during implementation
    (Architect review, not foreseen by the spec): upload speed and the
    live seed-count refresh both had to be computed in their own pass,
    structurally outside `pollSnapshot`'s `active||waiting` guard — a
    seeding entry's status is `"complete"`, so nesting either there would
    have shipped an upload-speed number that reads 0 forever. Deliberate
    deviation from the spec's wording: upload accounting uses
    `BytesWrittenData` (payload-only), not `BytesWritten` (wire bytes),
    matching every other upload-counter site in `internal/downloader` and
    the ratio accounting that decides when seeding stops. Frontend: two
    Unicode arrow glyphs (`↓`/`↑`, no new icon dependency), upload speed
    always shows on torrent rows including a true `0 KB/s` (a new
    `formatUpBps` sibling to the existing `formatBps`, which is left
    untouched since download speed still relies on its `"—"` case), seed
    count always-shows on torrent rows too (including while seeding) per
    an Architect-locked decision resolving an AC gap the spec left open.
11. **Security for Adult sections** — spec ready:
    `.omc/specs/deep-interview-section-pin-lock.md` (~11% ambiguity,
    PASSED). **Reframed mid-interview from an Adult-specific ask into a
    general-purpose configurable PIN lock** — Settings → Advanced gets a
    new panel: set a PIN, check which sidebar tabs it protects, plus a
    dedicated "Adult content" checkbox (app-wide — Discover's Adult
    sub-tab, Settings' Adult sections, Organize/Tag's Adult views, RSS
    feeds — since Adult isn't its own sidebar tab), plus a one-click
    "lock everything" shortcut. **Real finding: today's `adult_mode_enabled`
    setting is explicitly documented as UI-visibility-only, trivially
    bypassed by a direct API call** — this spec's enforcement is
    genuinely server-side instead, including closing the same bypass via
    `X-Api-Key` (which currently inherits full access with no scoping):
    a new `X-Section-Pin`-style header is required on API-key requests to
    locked sections. **Explicit, deliberate divergence from CLAUDE.md's
    stated single-operator/no-permissions-system architecture** — flagged
    for the same documentation discipline as this session's staged-for-
    approval exceptions, though not made a hard acceptance criterion by
    Wade this round.

    **Shipped 2026-08-03** — see `CHANGELOG.md` and `CLAUDE.md`'s
    "Single-operator auth" AMENDED 2026-08-03 sub-bullet, which quotes and
    corrects the three sentences this feature falsifies. Built via a
    multi-wave autopilot split per
    `.omc/plans/autopilot-impl-section-pin-lock.md`: a new
    `internal/sectionlock` package, a gate on `auth.Middleware`'s one gated
    success exit (Layer 1, path-classified — `r.PathValue` is empty inside
    the middleware and reading it would fail open on every Adult route),
    `mode.Build` for row/body-addressed Adult (Layer 2), explicit checks
    where `Build` is not reached (Layer 3), and SSE re-check tickers with a
    revocation epoch so an already-open stream terminates on a re-lock.
    **The `X-Api-Key` scoping divergence WAS documented** rather than left
    at "not a hard acceptance criterion" — see the CLAUDE.md sub-bullet.
    Two things worth flagging here. **GATE-1 was decided Option A**: the
    lock is inert in auth mode `none`, since that mode's banner already
    declares the instance fully open and there is no authenticated caller
    to bound a config write or the brute-force counter. And a gap-closure
    pass found a real enforcement hole the original plan missed — seven
    `/api/nodes*` operator routes classified as `settings` but structurally
    unreachable by the gate, because `NewNodesMux` builds its own
    `auth.Middleware` instead of inheriting `cmd/sakms`'s single wrap. **So
    locking `settings` now also gates node management**, a breaking change
    for any out-of-process script the first time an operator locks it.
12. **Library sidebar tab** — spec ready:
    `.omc/specs/deep-interview-library-sidebar-tab.md` (~10% ambiguity,
    PASSED). **Real finding: no dedicated library-browsing screen exists
    today — `Tag.tsx` accidentally already serves that role** for
    Movies/Series (full tracked-catalog grid, client-side title search,
    poster cards) alongside its actual purpose (tag CRUD). This spec
    formally separates the two: a new Library tab takes over browsing
    entirely (title search relocated, plus two new capabilities — genre
    filter and added-date sort, the latter requiring `createdAt` to be
    exposed to the frontend for the first time); Tag narrows back to
    pure tag-editing, reached by drilling into an item from Library.
    Conceptual split confirmed directly by Wade: "Library is what I have
    and Discover is for finding content." Adult mode is explicitly out
    of scope — keeps its existing separate table view untouched, matching
    today's split. Quality-tier filtering was considered and consciously
    declined (would need new data exposure), not silently dropped.

    **Shipped 2026-08-01** — see `CHANGELOG.md` and `CLAUDE.md`'s "Sidebar +
    section-tab redesign" section. Built via a 4-task autopilot split (backend
    `createdAt` exposure, shell/sidebar wiring, Library+Tag move-refactor,
    docs) per `.omc/plans/autopilot-impl-library-sidebar-tab.md`. One
    resolved wrinkle worth flagging here since it's this item's own spec
    contradicting itself: AC5 ("Tag no longer a standalone browsing entry
    point") and AC7 ("Adult's table view completely unchanged") can't both
    be fully true, because Adult's tag-CRUD table has always lived inside
    `Tag.tsx` and was never given a Library-equivalent screen (Adult is
    out of scope for Library, above). Resolution: Tag keeps a direct
    sidebar entry for all three modes rather than either breaking Adult's
    only path to its tag CRUD or building Adult a redundant new table
    screen the spec never asked for — Movies/Series still lost their
    standalone-browsing use of Tag, which is what AC5 was actually aimed
    at. 499/499 tests passing, `go build`/`vet`/`test` and `pnpm
    typecheck`/`test`/`build` all clean.
13. **Streaming media player** — spec ready:
    `.omc/specs/deep-interview-streaming-media-player.md` (~10%
    ambiguity, PASSED). **Confirmed as the same feature** as this file's
    "Native TV-app player enabling real transcoding" entry (below, under
    "Native TV-app player enabling real transcoding — future, DIFFERENT
    and higher legal-risk profile"). Wade was shown both the legal-risk
    flag and CLAUDE.md's explicit "SAK does not become a media player"
    Mission statement directly, before confirming he wants the full
    transcoding scope — a knowing, deliberate choice. **This spec's own
    Acceptance Criteria are gated on a dedicated legal/licensing review
    that has NOT been performed** — a deep-interview cannot do codec-
    royalty/patent research, so implementation must not start from this
    spec alone. Scope, once the legal gate clears: shared backend
    (direct-play via HTTP range-requests when compatible, else
    hardware-accelerated adaptive HLS transcoding reusing the existing
    `internal/nodes` worker-node dispatch) + a web player as the
    reference implementation. Native TV/mobile apps (Android TV, tvOS,
    Roku, Fire TV) are confirmed future work sharing this same backend,
    each getting its own dedicated future deep-interview — not built
    here.

### Dedup review UX refinement (list/card toggle, multi-keep, Skip, click-to-play) — deferred, plan approved, not built
Consensus-approved plan at `.omc/plans/dedup-ux-refine.md` (6 review rounds,
ambiguity 15%, status `pending approval`) to refine the Dedup review queue: a
list/card view toggle, checkbox multi-keep selection, a per-group Skip
action, and click-to-play video tiles for visual side-by-side comparison.
Explicitly **sequenced after** the VMAF-backend work
(`.omc/plans/vmaf-backend.md`), which lands the perceptual-quality score
these cards will surface — this UI is where a computed VMAF score becomes
operator-visible. Documentation only: not yet built, and this entry does not
authorize building it — it's the approved-but-pending follow-up the VMAF
backend clears the way for.

### Native TV-app player enabling real transcoding — future, DIFFERENT and higher legal-risk profile, needs its own scoping
A Jellyfin/Stash-like native TV app player for SAK's library, which would
enable **real transcoding** (actual H.264/HEVC/etc. re-encoding, not just
decoding). Flagged explicitly as a **materially different and higher
legal-risk profile than the VMAF work**: VMAF is decode-only and never
encodes, so it stays outside the codec patent-royalty territory FFmpeg's own
legal page warns commercial encoders about (see `NOTICE.md`). A transcoding
player crosses into real encoding — real codec-royalty exposure — a
genuinely different risk analysis that `NOTICE.md`'s current decode-only
rationale does NOT cover and would have to be extended for. Captured here as
a future item per the VMAF spec's explicit instruction
(`.omc/specs/deep-interview-vmaf-backend.md` Follow-ups); **not designed or
scoped here** — it needs its own future scoping effort before any work
starts. Cross-reference: this is the deliberate future reconsideration of the
"Hardware acceleration for transcoding/thumbnails" item in "Dropped from
scope" below (dropped because SAK doesn't transcode *today*) — as a player
feature, with the legal caveat above attached, not a silent reversal of that
drop.

### Frontend redesign — fully shipped 2026-07-19
Shell shipped 2026-07-13; bulk-apply tables + system dashboard shipped
2026-07-17; Collections/structured tagging UI (the last open content
surface) shipped 2026-07-19 — see "Recently shipped" below.

### Cheap, independent wins
- **Clearer mount-disconnect error messaging** — shipped 2026-07-11, see
  "Recently shipped" below.
### Matching quality
- **Confidence scoring** — shipped 2026-07-11 for the TMDB-backed Movies/
  Series paths (`proposeOneLibrary`/`proposeOneEpisodeLibrary`), see
  "Recently shipped" below. **This entry's original deferral note is now
  MOOT, corrected 2026-07-16 (same audit as the Whisparr-elimination
  fix above):** it said this was "deliberately NOT extended to Adult's
  Whisparr-lookup path (`lookupFirst`/`lookupWithAIFallback`)," to be
  revisited once Adult got its own library-owned Rename path.
  `lookupFirst`/`lookupWithAIFallback` no longer exist anywhere in
  `internal/rename` — Whisparr elimination for Adult (see "In progress"
  above) replaced that whole path with `rename.ScanLibraryAdult`/
  `ApplyLibraryAdult`'s own phash-first identification pipeline (see
  CLAUDE.md's Adult section), which was never a candidate for this
  TMDB-search confidence-scoring mechanism to begin with — there's no
  live gap here anymore to revisit, the code this note was about is gone.
- **Manual override / re-pick** — shipped 2026-07-11 for Movies/Series
  (TMDB-backed), see "Recently shipped" below. Adult's community-scene
  correction (a different id space, foreignId via Whisparr) already has its
  own separate mechanism (Give back) and wasn't extended here.
- **Logical episode-splitting** — shipped 2026-07-16, see "Recently shipped"
  below.
- **Descriptor-as-co-credit pollution in TPDB performer catalogs** — not yet
  started; logged 2026-07-27 out of
  `.omc/plans/ralplan-adult-discover-performers-availability-gate.md`'s scope
  (its "Out of Scope" Decision DC). Live-verified 2026-07-26: certain
  low-curation/amateur-published TPDB sites credit scenes with body-part/act
  descriptor tags (e.g. "Huge Tits") alongside real performer stage names, in
  the same scene credit list — TPDB conflates the two at the data-entry
  level. The merged Performers row's Option B availability hard filter
  (shipped 2026-07-27, `internal/api/adultdiscover_merge.go`'s
  `filterAvailablePerformers`) deliberately does NOT and CANNOT fix this —
  it only checks catalog/grabbable presence, so a descriptor co-credited on
  an available scene is retained exactly like a real performer sharing that
  scene (see `TestFilterAvailablePerformers_DescriptorCoCreditHonestyWitness`
  for the executable proof this stays true). Every structural signal tested
  during that investigation (`is_parent`, `extras.gender` presence,
  `site_performers` count) failed to discriminate a descriptor from a real
  person. A curated credit-role/denylist approach was considered and
  rejected as too fragile (risks hiding a real performer with a provocative
  stage name) — a separate effort would need a genuinely different signal,
  not yet identified.

### Metadata expansion
- **TVDB as fallback metadata source** — shipped 2026-07-17, see "Recently
  shipped" below. IMDB deferred: no official public API (would need a paid
  third-party mirror or scraping), judged not worth the complexity.
  **Ops note (2026-08-06):** live sakms now has `connections.tvdb` populated
  (encrypted at rest); BW SM backup UUID `sakms/tvdb-api-key`. Existing
  `tvdbFallbackSeries`/`tvdbFallbackMovie` paths are therefore active.
- **Sonarr-like TVDB proxy (Skyhook-style)** — queued 2026-08-06.
  Sonarr does **not** embed a TVDB key in the client; it calls
  `https://skyhook.sonarr.tv/…`, and the TVDB credential lives only on that
  private proxy. For multi-user / open-source distribution of sakms, mirror
  that pattern: a small metadata proxy (self-hosted or operated) that holds
  the TVDB key and serves sanitized search/show payloads, so installs need
  no TheTVDB account. Homelab single-operator interim remains encrypted
  `connections.tvdb` (already working). Scope when built: auth to the proxy,
  cache, rate limits, and whether Movies/Series rename should prefer proxy
  over direct TVDB. Do **not** hardcode the key in the sakms git tree.
- **Local `.nfo` preference** — shipped 2026-07-17, see "Recently shipped"
  below. Artwork reuse (local poster/fanart) remains open if it comes up.
  **UPDATED 2026-08-06:** Series NFO fast-path no longer hard-unmatches when
  `SeasonDetails` 404s for the sidecar TMDB id — it falls through to filename
  TMDB search → TVDB → web-authority (same pipeline as no-NFO orphans).
- **Collections** — shipped (date unclear; already complete when audited
  2026-07-17). See "Recently shipped" below.
- **Structured Genre/Actor tagging** — shipped 2026-07-17, see "Recently shipped" below.

### Automation
- **Watch folders (inotify)** — shipped 2026-07-17, see "Recently shipped" below.
- **Background task queue** — not needed. Watch folders run `RunWatchFolders`
  as a goroutine from `main.go` (confirmed 2026-07-17 during audit) — Scan
  never blocks an HTTP handler. No current operation needs a queue. Revisit
  only if a genuinely slow, user-triggered operation appears.
- **Webhooks + real API docs** — shipped (pre-2026-07-17; discovered
  complete during audit). `internal/webhooks` + `internal/api/webhooks_api.go`
  implement full CRUD + test-fire; `internal/api/openapi.go` embeds
  `openapi.yaml` and serves it at `GET /api/openapi.yaml`. GraphQL remains
  out of scope (rejected — no clear win over the existing REST surface).

### System dashboard — shipped 2026-07-17, see "Recently shipped" below.

### Discovery & requests UX (single-operator scope)
Identified 2026-07-24 comparing sakms against an external concept doc for a
hypothetical multi-user "unified media platform" (working name "OmniMedia")
that assumed Overseerr/Jellyseerr-style multi-user requests, per-user quotas,
and role-based auto-approval. **Scoped down, not adopted wholesale** —
CLAUDE.md's single-operator, no-permissions-surface decision stands; nothing
below introduces per-user roles, quotas, or a second user. See "Dropped from
scope" for the multi-user piece that was explicitly rejected. What's still
worth building, staying single-operator:
- **Bulk/multi-select request-and-grab** — **already shipped, 2026-07-24**
  (this entry was stale; no interview or build needed). See `CLAUDE.md`'s
  "bounded Discover bulk-grab exception" note for the decision record.
  Confirmed live in code, not just documented: an opt-in Select-mode toggle
  on Discover (`frontend/src/screens/discover/selection.tsx`,
  `BulkBar.tsx`), Movies select via a per-card checkbox, Series select
  per-season **or per-episode** via chips (`SeriesSeasonSelect`, no
  whole-series checkbox — episode granularity added 2026-08-02 by the picker
  redesign; a whole season and any episode of that season are mutually
  exclusive, since dispatching both means a season pack plus a duplicate
  single episode), capped at 20 flattened items
  (`internal/api/autograb_batch.go`'s `MaxBatchGrabItems`, where a flattened
  item is a whole season **or** a single episode — so a series contributing
  three checked episodes contributes three; the cap value and the guard are
  both unchanged), submitted via `POST /api/autograb-batch` and executed
  sequentially server-side with a three-state per-item result
  (grabbed/needs-manual-pick/error) — never a queue-wide "grab everything."
  A live registry drops any selected-but-no-longer-rendered card before
  building the batch, so a stale selection can't fire a grab of the wrong
  title. Covered by `autograb_batch_test.go` and
  `Discover.select.test.tsx`.
- **Request-status view** — **reconciled 2026-08-01, shipped 2026-08-02**:
  `.omc/specs/deep-interview-request-status-missing-filter.md` (~9%
  ambiguity, PASSED). **Turned out ~90% already built before this
  interview happened**: `Requests.tsx` already is the single
  cross-mode worklist screen this item asked for, with live status/mode
  filter chips and bulk-exclude. Discover per-card badges — the thing
  this entry said to move away from — were never actually built, so
  there was nothing to replace. "Requested" as a status is intentionally
  absent by design (`internal/api/requests.go`'s own comment: "a grab IS
  the request... no approval queue"). The one real gap: "Missing" existed
  only as a numeric annotation (`· N missing`) on In-Library series rows,
  not a separate filterable state — shipped: a "Has Missing Episodes"
  filter chip (Series-only, frontend-only, no backend changes) closes
  exactly that gap. Deliberately does not overlap/merge with the
  Calendar spec's Upcoming view (item 8, above) — same underlying missing-
  episode data, different lens (date-organized vs. status-organized),
  both kept as independent views.

### Storage & health analytics
Identified 2026-07-24 during the same OmniMedia comparison above. Extends
the existing System dashboard (`internal/sysinfo`, shipped 2026-07-17)
rather than replacing it.
- **Storage allocation breakdown by media type and quality tier** —
  **shipped 2026-08-01** (frontend + backend, fully integrated with Library
  sidebar tab's drill-down). Dashboard.tsx gained a new Storage Allocation
  Breakdown section (a table with Movies/Series/Adult rows and quality-tier
  columns including "Unknown"), backed by real per-item size and quality-tier
  data — both now captured at Apply/Grab time going forward via migration
  `0055_library_size_tier.sql`.

  **CORRECTED 2026-08-01 — this entry's original claim "using data already
  tracked in `internal/library`/`internal/quality`, no new collection
  needed" was false and has been retracted.** Verified against live code that
  no per-file size and no per-item quality tier exist anywhere prior to this
  work. This was real new instrumentation, not a query-only feature. The
  actual scope:
  - **Size capture**: `os.Stat` on the file once relocated/imported at
    Apply/Grab time, stored in new `size` column on `library_items`/
    `library_episodes`/`library_scenes`.
  - **Quality tier capture**: at grab/relocate time, stored in new
    `quality_tier` column, populated from **the mode's currently-configured
    quality tier setting** (the same value `autoGrabTier` already reads for
    search scoring) — **not** `grabs.quality_profile_id`. That field was
    investigated and found dead: it's written only from a frontend request
    field zero callers ever send, so every row has it at `0` with no
    meaningful mapping onto a tier. Stored semantics: "the tier preference in
    force when this file entered the library," not a measured property of
    the file.
  - **Backfill for pre-existing items**: a one-time boot-time goroutine
    (launched after the HTTP server starts listening, so a stale/disconnected
    mount can't hang boot) — size via `os.Stat` on existing `FilePath`s; tier
    via a **2-step** fallback chain (the spec's original 3-step design
    assumed a grab-record match step that turned out to be dead for the same
    `quality_profile_id` reason above, and was dropped): (1) filename
    inference — `release.Parse()` against the file's path **relative to its
    root folder** (not just the basename, since SAK's renamer strips quality
    tokens from filenames but a season-pack parent directory may still carry
    them) + a new `quality.InferTier` reverse-lookup against
    `internal/quality`'s tier definitions, (2) explicit "Unknown" tier bucket.
  - **Dashboard endpoint**: `GET /api/admin/storage-allocation` — one grouped
    SQL query (no per-request disk I/O or external calls) aggregating
    size/tier from the stored columns into a dense 3×5 grid (movies/series/adult
    × low/medium/high/lossless/unknown).
  - **Drill-down**: Movies/Series cells are clickable and drill into a
    filtered item list in the Library sidebar tab (item 12, above) — a
    cross-spec dependency that now ships together. Adult cells are not
    clickable (see limitation 2 below).

  **Two accepted limitations** (non-blocking, documented):
  1. **Well-Renamed libraries backfill mostly to "Unknown" tier** — SAK's
     renamer strips quality tokens from filenames during relocation, so only
     un-Renamed/freshly-grabbed files (or season-pack parent directories)
     carry inferable quality tokens for the filename-inference fallback
     step. This is the spec's own explicitly-accepted outcome, not a bug —
     "Unknown" is a first-class, honestly-labelled bucket, not a blank.
  2. **Adult mode's Dashboard cells are informational only** — the Library
     screen was deliberately scoped to Movies/Series only (per its own spec),
     so the Adult row's drill-down cells cannot open a filtered item view (no
     Adult Library exists to open). Cells render with disabled styling and
     no click handler, providing operator visibility into Adult storage
     allocation without a broken navigation target.
- **Propose-only pruning rules** — **spec ready, dependency satisfied,
  planned but not yet built**: `.omc/specs/deep-interview-pruning-rules.md`
  (~9% ambiguity, PASSED). Its hard dependency ("Storage allocation
  breakdown," above) shipped 2026-08-01 — the per-item `size`/`quality_tier`
  columns this spec's rule conditions read now exist. An implementation
  plan exists at `.omc/plans/autopilot-impl-pruning-rules.md`
  (Architect+Critic reviewed) and is ready to resume when scheduled — see
  that file for a required safety addition (a match-count preview before
  a rule is saved, so a broad rule like an age-only threshold can't stage
  an unbounded, uncapped portion of the library for deletion in one click)
  identified during review and not yet resolved. Scope grew beyond this
  entry's original framing during interview: multiple named,
  operator-authored rules (not one fixed per-mode rule — a real
  rule-authoring system, a deliberate, explicitly-confirmed scope increase
  after being shown the no-premature-abstraction tradeoff), within-rule AND
  / across-rules OR, wired into `internal/scanschedule`'s Purge interval
  (**correction**: Purge's scheduler wiring is already fully built at
  HEAD — the spec's premise that it was "currently-unwired" was wrong;
  the only real gap is a frontend interval-settings UI, which doesn't
  exist for Rename/Purge/Dedup at all yet) as opt-in scheduling AND the
  existing manual Purge Scan button — still explicitly NOT auto-delete,
  same staged-for-approval invariant. Matches surface in the existing
  Purge review queue via the existing `Proposal.Reason` field (rule name +
  matched values), no new UI queue.

  **SHIPPED 2026-08-03 — Critic CRITICAL-risk deferral (2026-08-01) resolved
  via `.omc/plans/autopilot-impl-pruning-rules.md` §13 (Architect+Critic
  reviewed); migration `0059_pruning_rules.sql`.** Still explicitly
  **propose-only**: rule evaluation runs only inside Purge's existing
  Scan/propose phase (the manual Scan button and the pre-existing, opt-in,
  default-off `purge-scan-interval` scheduler — see the correction above;
  that interval already existed on the backend, this feature only added the
  missing operator-facing control for it), and every rule match still lands
  as an ordinary Pending proposal requiring an explicit human Apply — no
  Apply-family code path was touched. Two safety amendments closed the
  deferral:
  1. **Match-count preview is a SOFT warning, not a hard block.** `POST
     /api/pruning-rules/preview` returns how many library items a
     draft/saved rule would currently match; the Settings → Pruning
     create/edit form shows it as a non-blocking banner ("this rule would
     currently match N items"). Save/Update stays enabled regardless of how
     large N is — there is no cap on the preview count, by design (§13.1).
  2. **Purge's apply-batch is capped at 20 items per request**
     (`MaxBatchPurgeItems`, `internal/api/proposals.go` — parity with the
     existing `MaxBatchGrabItems`), Purge-workflow-only: Rename and Dedup
     are unchanged at the existing shared 200-item cap (`maxBatchItems`). A
     Purge `apply-batch` request over 20 items returns 400 before any Apply
     runs, so a rule that floods the queue can't wipe the library in one
     click (§13.2).

  Neither amendment is a substitute for actually reviewing the queue before
  applying: the preview warns but never blocks, and the batch cap only
  bounds how much damage one click can do, not whether the operator looked
  first.

### Discover card UI cleanup (grab buttons, poster size) — shipped 2026-08-02
`.omc/specs/deep-interview-discover-card-cleanup.md` (~9% ambiguity,
PASSED). Both open questions resolved: "remove grab buttons" means the
always-visible inline card button on Mainstream/Library/Adult cards is
removed entirely, and card-click opens `DetailPopup` as the sole grab
path — verified via code research to be a clean relocation, not new
infrastructure (`DetailPopup` already fully supports Series
season/episode selection and Adult's scene fields, and Adult's card
already wires to it). Poster size: modest increase (Mainstream/Library
180→220-240px, Adult 200→230-260px proportionally, same 16:9), explicit
priority on preserving the current visible-card-count in the
horizontal-scroll carousel over maximizing size. Select-mode's
checkbox/season-chip states and Search's release-picker suppression are
explicitly untouched — separate, pre-existing states.

**SHIPPED 2026-08-02** (three sequential waves in one lane: `Mainstream.tsx` +
`CalendarView.tsx`, then `Adult.tsx`, then tests + docs). `tsc --noEmit` clean,
`vitest run` 644/644 passing (was 641 with 30 failing mid-wave), `vite build`
clean. Plan: `.omc/plans/autopilot-impl-discover-card-cleanup.md`; full detail
in `CHANGELOG.md` and `CLAUDE.md`'s CORRECTED 2026-08-02 (later, same day) note
under Staged-for-approval.

**Two claims in the paragraph above turned out to be wrong and are corrected
here rather than left standing, since this entry is the record of what was
asked for versus what was actually true.**

1. ***"verified via code research to be a clean relocation, not new
   infrastructure"* — false.** It is a **mechanism substitution**: the removed
   button called `autoGrab` (`internal/autograb`'s scorer picks the release,
   one click); `DetailPopup` calls `manualGrab` with an operator-picked
   candidate. Discover has no per-card one-click auto-grab any more. The
   scorer is still reached via select-mode bulk grab and via
   `TraktWatchlistRow`, which was out of scope and keeps its inline
   `GrabButton` — making the Trakt row the only card-level one-click auto-grab
   left in Discover.
2. ***"Adult's card already wires to it"* is true, but incomplete in a way that
   mattered.** `AdultCard`'s body→popup wiring did pre-exist. What the spec
   missed is that `AdultCard` also carried `downloadUrl`/`downloadProtocol` —
   a direct-enclosure path that dispatches a fresh-feed scene straight to the
   download client and **skips Prowlarr**, which `DetailPopup` cannot do. That
   is the one real capability loss; it survives only through select-mode bulk
   grab, and that mitigation is now covered by a real test asserting the
   `POST /api/autograb-batch` payload rather than by a code-trace claim.
   `LibraryCard`, by contrast, had **no** popup wiring at all and gained it
   here (guarded against `tmdbId <= 0` and against select-mode).

Final sizes: Mainstream/Library **220px**, Adult **240px**; `EntityCard`
deliberately stays 200px. `CalendarView`'s grid needed a matching
`min-w-[92rem]` → `min-w-[106rem]` widening the spec never anticipated. The
picker redesign's `insideModal` nested-modal carve-out became unreachable once
`PosterCard` lost its Grab button and was removed in the same change.

**Open, non-blocking sign-off gates:** GATE-A (accept Adult's one-click
direct-enclosure loss), GATE-B (Trakt keeps its inline auto-grab, so "sole grab
path" is scoped to the three named card types), GATE-C (`DetailPopup` shows a
bare but correct-and-actionable error where `GrabDialog` showed an inline
setup form). AC8 (visible-card-count + CalendarView layout) and AC9's popup leg
are manual browser checks.

### Dropped from scope
- **Token/regex-based custom renaming engine** — considered, then
  explicitly dropped (2026-07-10): would have reopened `internal/naming`'s
  deliberate fixed-preset design (Jellyfin/Legacy) from Stage 2c. User will
  revisit later if needed; `internal/naming` stays as-is for now.
- **Hardware acceleration for transcoding/thumbnails** — dropped as a scope
  mismatch: SAK doesn't transcode or generate thumbnails, so there was
  nothing for it to accelerate. (GPU accel is back in scope, but narrowly,
  for phash frame-decoding — see the "phash-based Dedup" in-progress entry
  above, a different and more concrete driver.)
- **Full OIDC client** — **built after all (2026-07-11)**, reversing the
  earlier "dropped in favor of forward-auth" decision: `oidc` mode is now a
  real OpenID Connect Relying Party (Authorization Code flow with PKCE,
  JWKS-verified ID token), replacing both the forward-auth and Authentik-
  introspection modes. See "Recently shipped" above and the CHANGELOG. A full
  **SAML** client remains out of scope — OIDC covers the same need for this
  single-operator tool with far less surface.
- **GraphQL API** — dropped; the existing REST surface has no problem a
  GraphQL rewrite would actually solve.
- **Multi-user request workflows / RBAC / per-user quotas** — considered
  2026-07-24 (comparing sakms against an external "OmniMedia" concept doc
  that assumed Overseerr/Jellyseerr-style multi-user requests) and rejected:
  directly contradicts CLAUDE.md's settled single-operator, no-permissions-
  surface decision, restated multiple times in that file. Not planned; see
  "Discovery & requests UX" above for the single-operator-scoped version of
  what's actually worth building from that comparison.

---

## UI mockup reference

Five AI-generated concept images shared 2026-07-10, depicting a
dashboard-style redesign (garbled placeholder text throughout —
"Tagnis"/"Papeles"/"Compines"/"Sctive" — confirming these are AI-generated
mockups, not a literal spec, hence "inspiration only" per the scope decision
above). All five share a left sidebar: Dashboard, Series, Movies, Tagnis
[sic], Media Management (expandable: Queue, Deduplication, Renaming,
Tagging, Import), Movies, Series, Papeles [sic], Compines [sic], Settings.

1. **"Renaming" / Mass Rename Utility** — a table (Original Filename /
   Current Path / Predicted Result with Path Nesting), row checkboxes, a
   "Rename Selected (2 Files)" button with a dropdown of preset-style
   options (Collection Folders / Season Folders / Add Quality Tags / Date
   Suffix). This is the bulk-apply mockup — see "Bulk apply" above.

2. **"Import Content"** — an "Add Content Wizard": step 1 is a file-browser
   panel (breadcrumb path navigation, e.g. `/mnt/downloads/completed/`);
   step 2 is "Configure Import" (Import Type dropdown defaulting to
   "Automatic Detect," "Assign to Collection" dropdown, an "Auto-tag
   Content" toggle, a "Start Scan" button); below, a "Scan History" table
   (Name / Status / Failed / Timestamp columns).

3. **"Tagging"** — a poster grid ("Library Tagging," with a search/filter
   box) with select-checkboxes on each poster, and a right-side "Edit Tags"
   panel showing structured **Genres** (chip list, e.g. Sci-Fi/Action/
   Thriller), **Actors** (chip list, e.g. named performers), and a
   **Collection** dropdown (e.g. "Nolan Collection"), plus a "Save Tags"
   button. This is the structured Genre/Actor tagging + Collections mockup
   — see "Metadata expansion" above.

4. **"Deduplication Queue"** — a table (Title / Format / File Size /
   Status columns) showing multiple detected-duplicate rows per title
   (e.g. two copies of one movie, three of another, each row's Status
   showing "Duplicate"), row checkboxes, a "Resolve Duplicates" button, and
   a "Merge & Delete Lower Quality" dropdown action (with sibling options
   like "Merge & Delete" / "Merge & Keep"). Another bulk-apply mockup.

5. **"Library Dashboard"** — the true home/system-dashboard view (a
   simpler top icon-bar instead of the shared sidebar, suggesting this may
   be a distinct top-level landing page): a "System Overview" tile (status
   + pending-task count), a "Current Downloads" tile (per-download title,
   progress percentage, transfer rate, ETA), a "Network & Disk Usage" tile
   (a small throughput chart plus disk read/write figures), a "Library
   Health" tile (a donut/ring chart — matched/unmatched/error counts), and
   a "Library Content Summary" tile (title counts per mode, a bar chart,
   total storage used/available). This is the "System dashboard" backlog
   item above — note the Network & Disk Usage piece specifically has no
   existing data source in SAK today.
