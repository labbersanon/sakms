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

### phash-based Dedup — Movies + Series refinement shipped; Adult and phash-primary grouping still open
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

**Still open (next slices):**
- **Whisparr elimination for Adult.** Adult gets its own library-owned
  Rename/Purge/Dedup/Tag path, same pattern as Movies/Sonarr. Decided
  2026-07-10 (`CLAUDE.md` Scope), no design yet — this is a substantial
  slice (Adult's own `library.Item`-equivalent schema, its own Search/grab,
  migrating off `internal/servarr`'s Whisparr client for the app-level path
  while keeping `internal/servarr` itself, same precedent as Radarr/Sonarr).
- **phash-PRIMARY grouping (TMDB-less).** The larger ambition from the original
  entry: making phash the *primary* duplicate signal that groups files with no
  shared identifier at all — replacing identifier-based grouping rather than
  refining it. This needs a full-library comparison strategy (the current slice
  is scoped to same-identifier groups, which comes for free; primary grouping
  is not). Not started.
- **GPU frame decoding.** CPU baseline shipped; GPU (QuickSync/NVENC) as an
  opt-in speedup for frame decoding is still just a decided-in-principle idea.
- **PDQ is still pending an imghash tagged release.** The algorithm is isolated
  behind `internal/phash/algo.go` as a one-file swap point, but imghash's
  latest tag (v1.1.0) has no PDQ — it lives only on the unreleased `main`
  branch, and pinning a deletion-gating signal to untagged upstream was
  rejected. Swap PHash→PDQ once imghash tags a release containing it.

---

## Recently shipped (outside this backlog)

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
Closed the "Matching quality" backlog item above for Movies/Series (see that
entry for the deliberate Adult/`lookupFirst` scope deferral). `internal/
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

---

## Backlog (not yet started, roughly in discussion order)

### Frontend redesign
Sidebar nav + dashboard-style layout, dark theme, replacing today's
lightweight single-page tab UI. See "UI mockup reference" below for the
visual direction. Scope decision (2026-07-10): build the redesign wrapping
SAK's *existing* data and workflows — do not treat the mockups as a literal
feature spec. Needed as the home for several other backlog items below
(bulk apply's multi-select tables, the system dashboard, Collections/tagging
UI) — likely the natural first thing to build once current work lands.

### Bulk apply
A deliberate, considered reversal of "no apply-everything path anywhere, by
design." Needs its own design pass: partial-failure handling per workflow
(Rename/Dedup/Purge already have different single-item failure shapes —
see `applyByWorkflow`'s doc comment in `internal/api/proposals.go` — a
batch version needs to decide per-workflow whether one failure blocks the
rest or skip-and-continue, and how that's surfaced in the UI), and an
explicit update to `CLAUDE.md`'s stated principle once built (not a silent
reversal).

### Unified downloader
Consolidate qBittorrent and NZBGet the same way Radarr/Sonarr/Whisparr were
already displaced (see `CLAUDE.md`'s Mission) — SAK owns the actual
torrent/usenet download itself instead of depending on an external client
for that one step. Today `internal/qbittorrent`/`internal/nzbget` are thin
clients against a separately-run app the operator must install, configure
(URL + username/password), and keep online; `dispatchToDownloadClient`
(`internal/api/search.go`) picks one based on the release's protocol
(torrent vs. Usenet) purely because two different external apps speak two
different protocols — not because SAK actually needs two of anything.
A real design needs: a torrent engine (embedding one, e.g. a Go BitTorrent
library, vs. shelling out) and a Usenet/NZB downloader (connection pooling,
`yenc` decode, par2 repair — a bigger lift than torrent), a unified
queue/status model replacing today's per-client polling
(`internal/qbittorrent`/`internal/nzbget`'s own status calls), and a
decision on whether both protocols are worth owning at once or whether one
ships first. Flagged 2026-07-14 while building auto-grab's "service isn't
configured" setup prompts (Discover's `GrabError` — see CHANGELOG) made the
two-separate-external-apps friction concrete: an operator hits this dance
twice (Prowlarr for search, then qBittorrent *or* NZBGet for the actual
download) before a single one-click grab can complete end-to-end. Not
started — no design, no client package, no schema.

### Mainstream Discover: trailer link + hide not-yet-released movies
Two additions, deferred 2026-07-15 in favor of live Adult Discover bugs.
(1) A "Watch Trailer" link in the detail popup (Movies/Series only, not
Adult), opening the title's YouTube trailer in a new tab — needs a new
`internal/tmdb.TrailerURL(ctx, mt, tmdbID)` method (`/movie|tv/{id}/videos`,
prefer `official==true`, filter `site=="YouTube" && type=="Trailer"`), a
narrow `TrailerResponse` DTO, and a one-off popup-scoped
`GET /api/modes/{mode}/discover/trailer?tmdbId=N` handler (same trigger
shape as the existing `discoverAvailabilityHandler` — fires once per popup
open, not a bulk fetch). Renders next to the existing "More on TMDB →" link
in `DetailPopup.tsx`. (2) Hide movies from Trending Movies and Popular
Movies (not Upcoming Movies, not Series) that have no US digital/physical
release yet — needs `internal/tmdb.HasUSRelease(ctx, tmdbID)`
(`/movie/{id}/release_dates`, type 4=Digital/5=Physical dated today or
earlier; type 3-only or no US entry = still theater-only, filter it out),
wired into `discoverHandler`'s trending/popular dispatch. Real design
constraint already scoped: no bulk release-dates endpoint exists, so this
is one extra TMDB call per item — needs bounded-concurrent fetching
(`golang.org/x/sync/errgroup`, already an indirect `go.mod` dependency,
`SetLimit(5)`), and a real edge case to handle (not just note): if an
entire TMDB page's movies all filter out, `Mainstream.tsx`'s `PaginatedRow`
would mark the row falsely exhausted (it exhausts on the first empty
batch) — the handler needs to retry the next TMDB page (bounded, e.g. 3
extra attempts) before returning empty. Neither TMDB videos nor
release-date-type data exists anywhere in this codebase today — confirmed
by direct code research, not assumed. No caching for either, consistent
with the rest of Discover (`discoverHandler`/sliders resolve live already).
Not started — full implementation plan was written and researched
end-to-end this session but not yet built.

### Cheap, independent wins
- **Clearer mount-disconnect error messaging** — shipped 2026-07-11, see
  "Recently shipped" below.
### Matching quality
- **Confidence scoring** — shipped 2026-07-11 for the TMDB-backed Movies/
  Series paths (`proposeOneLibrary`/`proposeOneEpisodeLibrary`), see
  "Recently shipped" below. **Deliberately NOT extended to Adult's
  Whisparr-lookup path** (`lookupFirst`/`lookupWithAIFallback` in
  `internal/rename/rename.go`, called from `Scan`) — a different mechanism
  entirely (`servarr.LookupResult` from Whisparr's own `/lookup` proxy to
  TPDB, not a raw `tmdb.Item`), and Adult/Whisparr elimination (see
  `CLAUDE.md` Scope) hasn't started. `lookupFirst` still takes
  `results[0]` unconditionally today — a conscious deferral, not an
  oversight, flagged by the same-day code-reviewer pass and left for
  whoever designs Adult's own library-owned Rename path to pick up (the
  natural point to revisit Adult's matching logic anyway, since that
  slice replaces `lookupFirst`'s Whisparr dependency entirely rather than
  patching it in place).
- **Manual override / re-pick** — shipped 2026-07-11 for Movies/Series
  (TMDB-backed), see "Recently shipped" below. Adult's community-scene
  correction (a different id space, foreignId via Whisparr) already has its
  own separate mechanism (Give back) and wasn't extended here.
- **Logical episode-splitting** — one video file that's actually two
  episodes bundled together: record two `Episode` rows pointing at the same
  `FilePath`, no re-encoding. (Explicitly NOT physical file-splitting —
  that was considered and rejected as out of scope for this item.)

### Metadata expansion
- **TVDB/IMDB as fallback metadata sources** — today Movies/Series
  identification is TMDB-only; TVDB is only ever a *derived* id via TMDB's
  `/find` endpoint, never a primary search source. Real, substantial
  feature: new client package(s) + a per-mode source-priority order. Note:
  IMDB has no official public API — would need a paid third-party mirror
  or scraping, worth deciding on going in.
- **Local `.nfo`/artwork preference** — confirmed zero support today:
  `.nfo` is purely in `config.SidecarExts` (skip-only, contents never
  read), and there is no local poster/fanart-reuse logic anywhere. Would
  mean writing a parser for Kodi/Jellyfin's de facto `.nfo` XML schema and
  preferring it over a fresh TMDB search when present.
- **Collections** — TMDB has a native `belongs_to_collection` field on
  movie details, the natural seed. Movies-only (Series has no TMDB
  equivalent — same asymmetry pattern as Kids-root-path). Needs a new
  `collections` table + item→collection FK + whatever UI surfaces it.
- **Structured Genre/Actor tagging** — richer than today's flat per-mode
  tag vocabulary. Needs its own schema (genres, cast), sourced from TMDB's
  `/movie/{id}/credits` + genre list (new TMDB client methods, a new
  per-item fetch). Decide whether this replaces free-form tags or sits
  alongside them.

### Automation
- **Watch folders (inotify)** — real tension with "manual by default," but
  CLAUDE.md explicitly allows earned automation once a manual workflow is
  proven, and Rename/Dedup/Purge all qualify by now. Firm design
  constraint: a watch-folder trigger may only ever auto-run *Scan* (new
  proposals appear, still need a human Apply click) — never auto-Apply.
  Auto-Apply would break the one invariant this whole project is built on.
- **Background task queue** — the exact "scheduler infrastructure" CLAUDE.md
  says doesn't exist, by design. Only build this if/when watch-folders
  actually need it (so Scan doesn't block an HTTP handler) — no current
  operation is slow enough to need it independently as of 2026-07-10.
- **Webhooks + real API docs** — the REST API already *is* the
  extensibility surface (the frontend uses the same endpoints a script
  would). Missing pieces: formal API docs (OpenAPI) and outbound webhooks
  (notify an external URL on Apply/import completion). GraphQL was
  explicitly considered and rejected — no clear win over the existing REST
  surface, would be a rewrite for no benefit.

### System dashboard
Live download/library-health widgets (see "Library Dashboard" mockup
below). Download progress can reuse the existing Grabs list/status — just
needs a live-refresh view. Library Health (matched/unmatched/error counts)
is cheap — aggregating what `library.Store`/`proposals.Store` already
track. Network/disk I/O has **no existing data source at all** — would mean
reading `/proc/net/dev`/`/proc/diskstats` or similar, new capability with no
current use case driving it. Least connected to the rest of the backlog;
lowest priority.

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
