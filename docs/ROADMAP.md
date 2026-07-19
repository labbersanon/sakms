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

## In progress

### phash-based Dedup — Movies/Series/Adult refinement shipped; phash-primary grouping still open
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

**Still open (next slices):**
- **GPU frame decoding.** CPU baseline shipped; GPU (QuickSync/NVENC) as an
  opt-in speedup for frame decoding is still just a decided-in-principle idea.
- **PDQ is still pending an imghash tagged release.** The algorithm is isolated
  behind `internal/phash/algo.go` as a one-file swap point, but imghash's
  latest tag (v1.1.0) has no PDQ — it lives only on the unreleased `main`
  branch, and pinning a deletion-gating signal to untagged upstream was
  rejected. Swap PHash→PDQ once imghash tags a release containing it.

---

## Recently shipped (outside this backlog)

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

### Frontend redesign — content surfaces still needed
The sidebar/SPA *shell* already shipped 2026-07-13 (see "Recently shipped"
above) — this entry previously described the whole redesign, shell
included, as not-yet-started, which was stale; corrected 2026-07-16 (same
audit as the Whisparr-elimination fix above). Bulk apply's multi-select
tables shipped 2026-07-17 (see "Recently shipped" below). The system
dashboard shipped 2026-07-17 (see "Recently shipped" below). What's
genuinely still backlog is: Collections/structured tagging UI — see "UI
mockup reference" below for the visual direction, and its own backlog entry
further down for scope detail. Scope decision (2026-07-10, still holds):
build each wrapping SAK's *existing* data and workflows — do not treat the
mockups as a literal feature spec.

### Unified downloader — torrent shipped; torrent engine migration + Usenet still open

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
staging→library move as the old NZBGet/qBittorrent import path. NZB/Usenet
grabs return an explicit `400` ("usenet/NZB downloads aren't supported by the
aria2 backend yet") rather than silently failing.

**Still open — two independent follow-ons:**

**1. Replace aria2c subprocess with anacrolix/torrent (in-process engine)**

The aria2c approach works but carries real costs compared to a pure-Go
in-process library:

| Dimension | aria2c subprocess (current) | anacrolix/torrent |
|---|---|---|
| Binary bundling | `cmd/download-aria2c` + `//go:embed` + Docker fetch step | Just `go get` — none of that |
| Process management | ~200 lines spawn/restart/backoff/log-forwarding | In-process — zero |
| Progress model | 750 ms polling over JSON-RPC | `t.SubscribePieceStateChanges()` — event-driven |
| Platform support | Linux/amd64 only (hard error on other arches) | Any Go platform |
| RPC secret | Auto-generated, stored in `internal/secrets` | None — no auth surface |
| Per-file progress | Not native; aria2 reports file list at completion | `t.Files()[i].BytesCompleted()` real-time |

anacrolix/torrent (github.com/anacrolix/torrent): pure Go, MPL-2.0,
production-grade since 2014 (used by Gopeed, bitmagnet, Erigon). 6k stars.
Supports DHT, PEX, uTP, WebTorrent, WebSeeds, BitTorrent v2, rate limiting,
multiple storage backends. The refactor scope: delete `cmd/download-aria2c`,
`internal/downloader/embed.go`, `internal/aria2`; replace
`internal/downloader`'s subprocess+poll loop with an in-process client +
piece-change-driven SSE hub; `internal/api/downloads.go` minor updates (GID
becomes `*torrent.Torrent` handle). The SSE fan-out shape stays identical.

**2. Usenet/NZB native support**

The NZB path still returns 400. A real Usenet implementation needs: NNTP
connection pooling (multi-server, TLS), yEnc segment decoding, NZB parsing,
and par2 repair (optional but expected by users). Bigger lift than torrent —
par2 repair is the real cost.

Evaluated and rejected: **gonzbee** (github.com/DanielMorsing/gonzbee) — has
the right building blocks but is a CLI tool, not a library. No programmatic
API surface. 21 stars, 2 forks, essentially unmaintained.

No maintained Go NZB library exists. Every candidate evaluated is abandoned
(2015–2016 vintage, single-digit stars): `strider-/go-usenet` (last commit
2015), `gjrtimmer/nzb` (2016), `matthiassb/go-usenet` (2016), gonzbee (dead
CLI). `andrewstuart/yenc` has a 2024 touch but is a single-file yEnc decoder,
not a downloader stack.

Real options:

1. **Keep NZBGet as an external process** — not the consolidation goal, but
   NZBGet is actively maintained and works today. Lowest effort.
2. **Build natively** — NNTP client (small; `net/textproto` is stdlib), yEnc
   decoder (small; simple spec), NZB XML parser (trivial), par2 repair (hard,
   or embed `par2cmdline` binary same pattern as aria2c). A few weeks of work
   with no library to lean on.
3. **Defer** — Usenet usage is declining; torrent-first is likely the right
   sequencing.

No design started. Decision needed before any implementation begins.

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

### Metadata expansion
- **TVDB as fallback metadata source** — shipped 2026-07-17, see "Recently
  shipped" below. IMDB deferred: no official public API (would need a paid
  third-party mirror or scraping), judged not worth the complexity.
- **Local `.nfo` preference** — shipped 2026-07-17, see "Recently shipped"
  below. Artwork reuse (local poster/fanart) remains open if it comes up.
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
