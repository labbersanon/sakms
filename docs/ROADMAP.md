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

**Still open (next slices):**
- **Adult phash refinement.** Extend the same
  refine-within-identifier-grouping approach to Adult's Servarr-backed
  `scanAdult` (foreignID grouping). Deferred, not designed at the
  file/function level yet.
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

### Cheap, independent wins
- **PUID/PGID Docker support** — confirmed gap: the image hardcodes uid
  1000, `docker-entrypoint.sh` chowns to that fixed uid via `gosu`, no
  PUID/PGID env vars exist anywhere. Small, well-understood, standard
  container-native fix.
- **Clearer mount-disconnect error messaging** — confirmed already SAFE
  (no workflow deletes anything on a missing file; see CHANGELOG's
  2026-07-10 redesign-discussion entry for the verification). Remaining
  work is just turning a raw `WalkDir`/`os.Stat` error into a clear "root
  folder unreadable — check your mount" message in Rename/Dedup's Scan
  error path.
- **Forward-auth header support** — trust a header (e.g. `Remote-User`)
  set by a reverse proxy the user already runs (Authelia, Authentik,
  Tailscale, etc.), instead of a full OIDC/SAML client. Keeps SAK
  single-operator; the proxy owns real identity federation.

### Matching quality
- **Confidence scoring** — today `items[0]` from TMDB/community-DB search
  is always taken unconditionally as the match; the only thing that routes
  to Unmatched is *zero* results, never "found something, but it's a weak
  match." Add a similarity/year-match score with a configurable threshold.
- **Manual override / re-pick** — a search box to manually assign a
  different TMDB id / community scene when Rename matched wrong. Today
  Dismiss only removes something from the queue, it can't correct it.
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
- **Full OIDC/SAML client** — dropped in favor of forward-auth header
  support (see Cheap wins above) — a proxy in front of SAK already solves
  this for most people in this situation, and a full client is a bigger
  lift in tension with SAK's single-operator design.
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
