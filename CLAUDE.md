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
including Series Dedup (done); **Whisparr is eliminated for Adult too**
(decided 2026-07-10, shipped 2026-07-12 — see Current State below) — same
pattern, Adult owns its own library-owned Rename/Purge/Dedup/Tag path
instead of depending on Whisparr. Beyond the three `*arr` apps, there is
still **no committed target list**: whether Bazarr, Tdarr, or anything else ever
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
a person approves, Apply commits exactly that item — no background pollers,
no scheduler infrastructure exists anywhere in this codebase today. That's
the default for anything new, too — **don't build speculative scheduling
ahead of proven manual usage.**

**AMENDED 2026-07-23 — the "no scheduler infrastructure exists anywhere in
this codebase today" claim above is stale, corrected here (a deliberate,
documented reversal, not a silent one — same class as the bulk-apply
amendment below; see `.omc/plans/vmaf-backend.md` and `CHANGELOG.md`).** The
original text in this section read "no background pollers, no scheduler
infrastructure exists anywhere in this codebase today." That blanket claim
was already inaccurate when written and is more so now: three background
schedulers predate this note — `internal/recheck` (availability re-check
ticker), `internal/adultnewest` (Adult "newest releases" background scan),
and `internal/api`'s `RunWatchFolders` (inotify watch-folder Scan trigger) —
each an interval-driven goroutine launched from `main.go`, exactly the
"scheduled second, once manual is proven" upgrade this section's closing
paragraph explicitly permits. The VMAF-backend work adds a fourth,
`internal/scanschedule` — a general Rename/Purge/Dedup scan scheduler (all 3
modes, per-workflow/mode configurable, 0/off by default) built as a
compile-time-enforced **Scan-only safety boundary**: it may only ever
trigger Scan (propose), never Apply, Dismiss, Repick, fingerprint-submit, or
any other mutating, operator-approval-gated action, on any workflow, under
any condition. So the corrected statement is: scheduler infrastructure DOES
exist, but every scheduler in it is Scan/propose/passive-flag only — the
staged-for-approval invariant above is fully preserved, nothing is
auto-Applied. The "manual by default, don't build speculative scheduling
ahead of proven manual usage" principle stands unchanged for anything new;
what's corrected is only the factual "none exists" claim, not the policy.

**CORRECTED 2026-08-01 — the Scan-only claim in the AMENDED 2026-07-23 note
above is now FALSE, and the scheduler enumeration in it was already
incomplete. Both are corrected here rather than quietly extended, the same
way that note itself corrected the one before it (see
`.omc/plans/autopilot-impl-connections-elimination.md`, `docs/ROADMAP.md`
item 3, and `CHANGELOG.md`).** The superseded sentence, quoted verbatim so a
future session can find it: *"So the corrected statement is: scheduler
infrastructure DOES exist, but every scheduler in it is Scan/propose/
passive-flag only — the staged-for-approval invariant above is fully
preserved, nothing is auto-Applied."* Two things in it are wrong now:

- **It is no longer Scan/propose-only.** The Usenet multi-subscription work
  adds `internal/api`'s `RunUsenetRetry` (`internal/api/usenetretry.go`,
  launched from `cmd/sakms/main.go`), the **first scheduler in this codebase
  that actually DISPATCHES DOWNLOADS** rather than proposing something for a
  human to approve. Each cycle sweeps in-flight usenet grabs for
  asynchronous retrieval failures and re-runs the full auto-grab pipeline
  for every `pending_retry` row that is due — and a re-search that finds a
  qualifying release dispatches it to the download client with no human in
  the loop. This is the deliberate, bounded auto-grab exception documented
  in full under **Staged-for-approval** below (AMENDED 2026-08-01), not an
  oversight and not a general loosening: every OTHER scheduler here is still
  Scan/propose/passive-flag only, and this one is bounded by an
  off-by-default toggle it re-checks on every single cycle. So the corrected
  statement is: **scheduler infrastructure exists, and all of it is
  Scan/propose-only EXCEPT `RunUsenetRetry`, whose dispatch authority is
  itself gated by `usenet_autograb_enabled` (default off).**
- **The count was stale before this work and is stale twice over now.**
  There are **six** interval-driven schedulers live today, five of which
  predate this work: `internal/recheck`, `internal/adultnewest`,
  **`internal/parseentity`** (entity-cache sync — omitted from the 2026-07-23
  enumeration entirely, which is why that note says "three predate … adds a
  fourth"), `internal/api`'s `RunWatchFolders`, and `internal/scanschedule`.
  `RunUsenetRetry` is the sixth. **The ordinals in `main.go`'s own launch
  comments ("the fourth", "the fifth") inherit the same omission and are not
  authoritative** — `internal/api/usenetretry.go`'s file doc deliberately
  declines to pick a number for exactly this reason. Count the launch block
  in `main.go` rather than trusting any prose ordinal, this one included —
  and note that `scanschedule.Run` is not a bare `go` statement (it starts
  one goroutine per workflow internally), which is part of why it is easy to
  miss when counting.

**AMENDED 2026-08-01 (later, same day) — series air-date monitoring adds a
FOURTH trigger source to the bounded auto-grab exception below, and changes NO
COUNT in this section.** Recorded explicitly because the natural reading of
"a new unattended thing that runs on a cycle" is that a seventh scheduler
appeared. It did not. The superseded enumeration, quoted from the
staged-for-approval amendment's bound 3 below: *"`RunAutoGrab` is the single
scoring-and-dispatch path for every trigger (`TriggerOperator` /
`TriggerRequest` / `TriggerRetry`, plus the reserved-but-unimplemented
`TriggerAirDate`)."* `TriggerAirDate` is no longer reserved — it is
implemented by `monitorAirDates` (`internal/api/airdatemonitor.go`), which
searches for every aired-but-missing episode of a monitored season. Corrected
enumeration: **four live triggers — `TriggerOperator` / `TriggerRequest` /
`TriggerRetry` / `TriggerAirDate` — all through the one `RunAutoGrab` path,
with the gate still enforced in exactly one place.**

**There are still SIX interval-driven schedulers, not seven.** Air-date
monitoring adds **no goroutine, no ticker, no interval key, and no line in
`cmd/sakms/main.go`** — it is a plain function call, the third pass inside the
existing `runUsenetRetryCycle` (`internal/api/usenetretry.go`), running after
the GID sweep and the due re-search. Count the launch block in `main.go` as
the CORRECTED note above instructs and it still yields the same six.
**This one is proven rather than asserted**, which is the difference worth
recording: `internal/api/airdatemonitor_static_test.go` is a static AST test
(modelled on `internal/scanschedule`'s `allowlist_test.go`, whose *test* shape
was adopted while its *scheduler* shape was rejected) asserting that
`airdatemonitor.go` itself contains no goroutine launch, no
`time.NewTicker`/`Tick`/`NewTimer`/`AfterFunc`/`After`/`Sleep` construction,
no interval-setting access and no declared `Interval` const, and that
`cmd/sakms/main.go` references nothing declared in it. **Read that guarantee
as narrowly as the test's own doc states it:** it inspects ONE file for a
fixed list of syntactic indicators, so a scheduler introduced any other way
would pass it. It is a backstop against the specific likely regression — a
future session "helpfully" giving this pass its own ticker and `main.go`
launch, which is exactly what the sibling torrent-behavior plan's §5
originally recommended and this feature's spec overrode — not a general proof
about the whole codebase.

**AMENDED 2026-08-02 — Calendar pre-release requests add a FIFTH trigger source
to the bounded auto-grab exception below, and change NO COUNT in this section.**
Same shape as the air-date amendment directly above, recorded for the same
reason: *"held dormant until the release date"* is the most schedule-suggesting
phrase in this feature's spec and it adds no scheduler at all. The superseded
enumeration, quoted verbatim from that amendment so a future session can find
it: *"Corrected enumeration: **four live triggers — `TriggerOperator` /
`TriggerRequest` / `TriggerRetry` / `TriggerAirDate` — all through the one
`RunAutoGrab` path, with the gate still enforced in exactly one place.**"*
Corrected enumeration: **five live triggers — `TriggerOperator` /
`TriggerRequest` / `TriggerRetry` / `TriggerAirDate` / `TriggerPreRelease` —
still all through the one `RunAutoGrab` path, still with the gate enforced in
exactly one place.** `TriggerPreRelease` is declared in
`internal/api/autograb_shared.go` and dispatched by `releaseDueGrabs`
(`internal/api/prerelease.go`); the category it introduces is documented in full
under **Staged-for-approval** below (AMENDED 2026-08-02).

**`TriggerRetry`'s population is UNCHANGED by this feature, and that is a direct
benefit of the design rather than a coincidence.** A held pre-release row is
born with an EMPTY `retry_after`, which makes it invisible to
`grabs.DueForRetry` unconditionally; it reaches dispatch through
`releaseDueGrabs`, never through `retryDueGrabs`. So "retry" keeps meaning
exactly what it means everywhere else in this document. The rejected design
(reusing `retry_after` as the hold) would have silently widened `TriggerRetry`
instead — the full Option A / Option B audit trail is in
`.omc/plans/autopilot-impl-calendar-grabs-requests.md` §5.4.

**There are still SIX interval-driven schedulers, not seven.** This feature adds
no goroutine, no ticker, no interval key, and **no line in `cmd/sakms/main.go`**
— that file is untouched by this feature. Two separate pieces of it look like
they should each have added one, and neither did:

- **`releaseDueGrabs` is a plain function call — the FOURTH pass inside the
  existing `runUsenetRetryCycle`** (`internal/api/usenetretry.go`), exactly as
  `monitorAirDates` is the third. The 24h cycle notices a release date has
  arrived with a `WHERE` clause (`grabs.DueForRelease`), not a timer.
- **The series episode-catalog sync is caller-driven, not timed.**
  `syncSeriesCatalog` moved VERBATIM out of `airdatemonitor.go` into a new file,
  `internal/api/seriescatalog.go`, so that METADATA becomes always-available
  while DISPATCH stays gated exactly as before (decided by Wade, 2026-08-02, in
  response to the finding below). It is called from `monitorAirDates` (unchanged
  behaviour when auto-grab is on) and from Calendar's read path, with its TTL
  and per-request cap declared in that new file — no ticker, no `main.go`
  launch. **Why it had to move:** that sync is the only thing that ever writes a
  `library_episodes` row with an empty `file_path` or seeds `air_date` at all,
  and its sole caller ran inside the toggle-gated retry cycle, which does not
  start while unattended auto-grab is off — the default on every install. So
  Calendar's Upcoming/Series view would have rendered permanently empty
  everywhere. **Why a NEW FILE rather than an export:**
  `internal/api/airdatemonitor_static_test.go` constrains `airdatemonitor.go` by
  name, and one of its assertions fires on any const whose NAME CONTAINS
  `"Interval"`, whatever that const is actually used for — so the TTL this read
  path needs would have failed that test from inside that file. The test passes
  **unmodified**. Never declare an `"Interval"`-named const or a settings-backed
  TTL in `airdatemonitor.go`.

**This count is verified, not proven — a weaker claim than the note above it
makes, stated honestly.** The air-date note could say *"This one is proven
rather than asserted"* because a static AST test pins it. There is no equivalent
test for `prerelease.go` or `seriescatalog.go`. The six was confirmed by reading
`cmd/sakms/main.go`'s launch block and confirming the file is untouched — the
method the CORRECTED note above prescribes — not by anything that would catch a
future session giving either of these its own ticker.

**The cycle now does FOUR things, not three.** The air-date amendment's *"it is
a plain function call, the third pass inside the existing `runUsenetRetryCycle`
(`internal/api/usenetretry.go`), running after the GID sweep and the due
re-search"* remains true **of `monitorAirDates`** and no longer describes the
whole cycle. In order: (1) the GID sweep converting asynchronous usenet
retrieval failures into `pending_retry`/`failed`, (2) the due re-search,
(3) `monitorAirDates`, (4) `releaseDueGrabs`. `usenetretry.go`'s own file doc
was corrected in lockstep and is the authoritative description of the ordering
and of why pass 4 runs last.

**AMENDED 2026-08-03 — the SIX count above is now stale; the discover-scheduled-refresh
feature is a real SEVENTH interval-driven scheduler, and `cmd/sakms/main.go` IS touched.**
The superseded sentence, quoted verbatim from the CORRECTED 2026-08-02 note above so a
future session can find it: *"There are still SIX interval-driven schedulers, not seven.
This feature adds no goroutine, no ticker, no interval key, and **no line in
`cmd/sakms/main.go`** — that file is untouched by this feature."* That sentence described
`releaseDueGrabs`/`monitorAirDates`/`syncSeriesCatalog`, not this feature, and does not
carry over to it. `internal/discoverrefresh` (plan
`.omc/plans/autopilot-impl-discover-scheduled-refresh.md`) adds a real `go
discoverrefresh.Run(...)` goroutine, a real `time.NewTicker`, a real interval key
(`discover_refresh_interval_seconds`), and a real line in `cmd/sakms/main.go` — so **there
are now SEVEN.**

Count the launch block in `main.go`, not any prose ordinal — same instruction the
CORRECTED 2026-08-02 note above gives, repeated here because it is what makes this count
reproducible rather than asserted: `internal/recheck`, `internal/adultnewest`,
`internal/parseentity`, `internal/api`'s `RunWatchFolders`, `internal/scanschedule`
(still not a bare `go` statement — it starts one goroutine per workflow internally),
`internal/api`'s `RunUsenetRetry`, and now `internal/discoverrefresh`'s `Run`. Air-date
monitoring, `releaseDueGrabs`, and series-catalog sync are still plain function calls
inside `runUsenetRetryCycle`, not separate entries — unchanged by this feature.

**This seven is verified by reading the launch block, not proven by a test — the same
honesty qualifier the CORRECTED 2026-08-02 note above states for its own six.** There is
no static AST test for `internal/discoverrefresh`, and none is warranted: the
`scanschedule`/`airdatemonitor` static tests exist to bound *dispatch* authority (proving a
scheduler cannot reach Apply/AutoGrab), and `internal/discoverrefresh` has no dispatch
authority at all to bound — it is read-only content caching (see the staged-for-approval
note below).

**On-by-default deviation, recorded explicitly so a future session does not "align" it to
the off-by-default majority and silently kill the feature.** `internal/discoverrefresh` is
the **second** scheduler in this codebase that is on by default (after `internal/adultnewest`'s
browse pass) and the **first Mainstream-affecting one** — every other scheduler in the
enumeration above (`recheck`, `parseentity`, `scanschedule`, `RunUsenetRetry`) is 0/off by
default. This is deliberate: a feature whose entire purpose is that Mainstream Discover is
fast on a fresh install cannot hang off an opt-in toggle a fresh install never turns on. See
the plan's §1.7 for the full reasoning (the two worked "looks like a scheduler and isn't"
examples this file already carries, and why this one is genuinely different from both).

**Staged-for-approval is untouched by this feature — this is not a sixth exception.** It
adds no `AutoGrabTrigger` and the trigger enumeration under **Staged-for-approval** below
(`TriggerOperator` / `TriggerRequest` / `TriggerRetry` / `TriggerAirDate` /
`TriggerPreRelease`) stays unchanged at five. Stated explicitly because every recent
CLAUDE.md amendment about a new background thing has been about widening unattended
dispatch, and a reader will pattern-match this one the same way by default — it is not: this
scheduler cannot reach any mutating call site by construction (its only writes are to its
own read cache table).

(Bulk apply, added 2026-07-17, is a same-screen multi-select of
already-reviewed Pending proposals — see the amended engineering-convention
note below. It doesn't change this section: there is still no automation,
no scheduler, no background trigger of any kind. Every batch is a human
clicking "Apply Selected" after reviewing each row, not the system acting on
its own.)

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

- **Staged-for-approval.** Every mutating action (Apply, grab, tag) acts
  only on already-approved things — nothing is ever auto-applied without a
  human having reviewed it first at Scan/Discover time.
  - **AMENDED 2026-07-17 — bounded bulk-apply exception (a deliberate,
    documented reversal, not a silent one; see `docs/ROADMAP.md`'s Bulk
    apply entry and `CHANGELOG.md`).** The original text here read "one
    item at a time... no 'apply everything' path anywhere, by design." That
    blanket rule is now narrowed, not dropped: Rename, Dedup, and Purge's
    review queues each carry an opt-in, same-screen multi-select — the
    operator checks several already-reviewed Pending rows/groups from ONE
    workflow+mode screen and clicks "Apply Selected," which POSTs one
    `apply-batch` request. The backend applies each item sequentially with
    skip-and-continue semantics (one item's failure never blocks the rest;
    every item gets its own ok/error result, no automatic retry) and fires
    one combined player-notify for the whole batch. This is still NOT a
    queue-wide "apply everything," a scheduler, or cross-mode/cross-workflow
    batching — every single row's own Apply/Give back/Search/Dismiss button
    still acts on exactly one item, unchanged, and a batch never includes
    anything the operator didn't explicitly check. Grabs and tag actions are
    untouched by this — they still take exactly one already-approved thing,
    same as always.
  - **AMENDED 2026-07-24 — bounded Discover bulk-grab exception (a second,
    deliberate, documented reversal; see `docs/ROADMAP.md`'s Discovery &
    requests UX entry and `CHANGELOG.md`).** Distinct from the bulk-apply
    exception above in one load-bearing way: bulk-apply acts on
    *already-reviewed Pending proposals* staged by a prior Scan — there is
    a review step before the batch ever fires. Discover's Search/Grab
    flow has no such staging step at all (a click there already fires a
    live Prowlarr search + download-client add, staged-for-approval's
    "approval" *is* the operator's click). Bulk-grab is the same
    single-item primitive repeated N times from one opt-in "Select" mode
    on Discover, capped (≤20 flattened items — a season-expanded series
    counts one item per selected season), executed sequentially server-side
    (`POST /api/autograb-batch`, never a client-side/goroutine fan-out —
    that would recreate the exact "hundreds of concurrent indexer queries"
    pattern that got the per-card availability badge permanently banned,
    see the "Discover never queries Prowlarr" note below), with a
    three-state per-item result (grabbed / needs-a-manual-pick / error)
    that never silently swallows a partial failure. It is still NOT a
    queue-wide "grab everything," a scheduler, or cross-mode
    auto-approval — every item is one the operator explicitly checked, and
    every OTHER Discover affordance (the per-card Grab button, the
    season/episode picker) is unchanged and still strictly single-item.
    Selection is cleared on tab/route change and any selected-but-no-
    longer-rendered card is dropped before the request is built, so a
    stale selection can never fire a live grab of the wrong title.
  - **CORRECTED 2026-08-02 — the bulk-grab exception above is now
    EPISODE-granular, and two of its sentences are false as written (see
    `.omc/plans/autopilot-impl-season-episode-picker-redesign.md` §6.3 and
    `CHANGELOG.md`).** Placed adjacent to the note it corrects rather than at
    the end of this list, same as the CORRECTED note under **Automation**
    above: this is not a fifth reversal, it is a correction to the second one.
    The two superseded sentences, quoted verbatim so a future session can find
    them:
    1. *"capped (≤20 flattened items — a season-expanded series counts one
       item per selected season)"*
    2. *"every OTHER Discover affordance (the per-card Grab button, the
       season/episode picker) is unchanged and still strictly single-item"*

    **Corrected counting semantic:** a flattened item is a whole season **or a
    single episode**. A selected episode counts as exactly one item, the same
    as a selected season does, so a series contributing three checked episodes
    contributes three items to the cap. **The cap value is unchanged at 20 and
    the sequential server-side execution is untouched** (`POST
    /api/autograb-batch`, still never a client-side/goroutine fan-out). The
    backend needed no counting change at all: the frontend has always
    submitted an already-flattened item list, and the request DTO has always
    carried an episode number, so an episode-level item was already
    representable and already counted as one. Only `MaxBatchGrabItems`' doc
    comment was stale.

    **Sentence 2 is only HALF false — do not over-correct it.** The per-card
    Grab button **is still strictly single-item**: it picks one season or one
    episode and grabs that one thing, exactly as before. Only bulk-select
    gained episode granularity. The season/episode picker's *surface* changed
    (free-text inputs → poster grid — see the Discover entry at the end of
    **Current state**), but nothing about the single-item Grab path did.

    **New bound this granularity requires, enforced in the UI: whole-season
    and episode selections for the same season are MUTUALLY EXCLUSIVE.**
    Selecting `S4` and `S4E7` together would dispatch a season pack plus a
    single episode of that same season — two genuinely different releases, so
    `activeGrabForGID`'s download-client-GID dedup cannot catch it, and the
    result is a duplicate on disk, a direct hit on the mission's "no
    duplicates" bar. Toggling a season on clears that season's episode keys;
    toggling any episode of a season on clears that season's whole-season key.
    This needed a **read-only** key-enumeration accessor on `SelectionStore`,
    because the same title can render in two rows at once (Trending *and*
    Popular) and a card-local workaround is defeated by construction. The
    store still parses nothing — the caller owns the key format entirely,
    which is what keeps the selection safety centerpiece uncoupled from one
    screen's naming.

    **Registration stays on the CARD's chip, never on the modal's tile.**
    `buildBatch`'s orphan-drop submits only keys still registered by a
    currently-rendered component; a modal closes and unmounts its tiles, so
    registering there would silently drop every selection at submit time while
    every existing test still passed. That is the single most likely way to
    break the stale-selection guarantee this list's 2026-07-24 note ends with
    — the guarantee itself is unchanged and still holds.
  - **CORRECTED 2026-08-02 (later, same day) — the per-card Grab button is
    GONE from Discover's three card types, so the 2026-07-24 sentence the note
    directly above deliberately left half-standing is now false in its other
    half too (see
    `.omc/plans/autopilot-impl-discover-card-cleanup.md` and `CHANGELOG.md`).**
    Stacked on top of the CORRECTED note above rather than replacing it, per
    this file's append-and-correct convention — read them in order. The
    paragraph now superseded, quoted verbatim so a future session can find it:

    > *"**Sentence 2 is only HALF false — do not over-correct it.** The per-card
    > Grab button **is still strictly single-item**: it picks one season or one
    > episode and grabs that one thing, exactly as before."*

    That was true when written. It is not now: `PosterCard`, `LibraryCard` and
    `AdultCard` have **no inline Grab button at all**. Clicking the card body
    opens `DetailPopup`, which is the sole grab affordance those three cards
    offer. What is corrected is only the existence of the button — the
    bulk-grab exception's own bounds (the ≤20 cap, the season-or-episode
    counting semantic, sequential server-side execution, the orphan-drop, the
    season/episode mutual exclusion) are **all completely unchanged**.

    **This is a MECHANISM SUBSTITUTION, not a relocation — the single most
    important thing in this note.** The spec that ordered it called it "a clean
    relocation, not a capability loss"; that framing was checked against source
    and is **false**. The removed button called **`autoGrab`**
    (`internal/autograb`'s bitrate-quality-floor scorer picks the release, one
    click). `DetailPopup` calls **`manualGrab`** with a candidate the operator
    picks by hand off the availability grid (three-plus clicks). **Discover
    therefore has no per-card one-click auto-grab any more.** Anyone verifying
    this work by asserting "behavior is unchanged" is verifying the wrong thing.

    **`internal/autograb` is still reachable from Discover**, by two paths, so
    the scorer is not orphaned: **select-mode bulk grab** (`autoGrabBatch`,
    untouched by this work) and **`TraktWatchlistRow`**, which was deliberately
    left out of scope and **keeps its inline `GrabButton`**. Consequence worth
    stating plainly because it looks like an oversight and is not: the Trakt
    watchlist row is now the **only** card-level one-click auto-grab left in
    Discover, and `TraktWatchlistRow.tsx` is the **sole remaining consumer** of
    the still-exported `GrabButton`/`GrabDialog` pair. Do not delete either as
    dead code.

    **Adult lost a real capability here, and select-mode is the whole
    mitigation — do not restore the button without reading this.**
    `AdultCard`'s `sceneTarget()` carries two fields no other card does,
    `downloadUrl` + `downloadProtocol`: when a scene's feed is currently fresh
    it can dispatch **straight to the download client and skip Prowlarr
    entirely**. `DetailPopup`'s Adult path **cannot** do this — it grabs a
    Prowlarr-sourced candidate by construction, so a fresh-feed scene that used
    to need zero indexer queries now needs one. The capability survives ONLY
    through select-mode bulk grab, which still builds `sceneTarget()` with both
    fields intact. That mitigation is exercised by a real test, not asserted:
    `frontend/src/screens/discover/Adult.grab.test.tsx` submits a fresh-feed
    scene through select-mode and asserts the two fields appear in the actual
    `POST /api/autograb-batch` body. **If a future session is tempted to give
    `AdultCard` its inline Grab button back, understand first that the button
    carried unique capability no other card's did — and that threading
    `downloadUrl` into `DetailPopup` instead would restore the one-click path
    properly and make this whole note moot.**

    **Poster sizes changed with it** (pure layout, no behavior):
    Mainstream/Library `PosterCard`/`LibraryCard` 180px → **220px**
    (`aspect-[2/3]` unchanged), Adult `AdultCard` 200px → **240px**
    (`aspect-video` unchanged). `EntityCard`'s studio/performer tiles
    **deliberately stay at 200px** — they diverge from `AdultCard` now, which is
    correct, so never blanket find/replace `w-[200px]` in `Adult.tsx`.
    `CalendarView`'s grid container had to widen from `min-w-[92rem]` to
    `min-w-[106rem]` to fit seven 220px columns; without it cards bleed into the
    adjacent day at every viewport.

    **`LibraryCard` gained DetailPopup wiring it never had**, and its click is
    **guarded twice**, both deliberately: it is inert when `tmdbId <= 0` (a
    tracked item TMDB never matched yields id 0, and the popup reads `item.id`
    unconditionally — opening it would fire three requests keyed on 0 that
    cannot succeed, silently), and inert while **select-mode is on**.

    **The `insideModal` prop's full lifecycle, recorded because it is exactly
    the "added, then correctly retired" story this file documents elsewhere.**
    The season/episode picker redesign (same day, earlier) added `insideModal`
    to `PosterCard`: inside `DetailPopup`'s "More like this" rail it suppressed
    the Grab button on **Series** cards only, because that button's picker
    opened a second `Modal` nested inside the popup's own — two
    `fixed inset-0 z-50` overlays, both closing on one backdrop click. This
    cleanup removed the Grab button from `PosterCard` outright, so the rail has
    nothing left that can open a nested modal and `insideModal` became
    unreachable; it was deleted from `PosterCard` and its `DetailPopup` call
    site rather than left as dead code. **A deleted guard is only safe while
    the invariant that made it dead still holds**, so that invariant —
    every card body which opens `DetailPopup` is select-mode-inert, hence no
    popup can be raised mid-bulk-select — has its own dedicated test
    (`Discover.test.tsx`, "a LibraryCard body click is inert while select-mode
    is on"). If any Grab affordance is ever reinstated on the recommendations
    rail, a Series one re-creates the hazard and needs `insideModal` (or an
    inline picker) back.

    **CORRECTED — the paragraph above only states half the invariant, and
    overstates its test coverage.** Quoted verbatim, the sentence being
    corrected: *"that invariant — every card body which opens `DetailPopup` is
    select-mode-inert, hence no popup can be raised mid-bulk-select — has its
    own dedicated test."* True as far as it goes, but keeping `insideModal`
    safely dead requires BOTH directions, and only one was named:

    (a) **No card body opens `DetailPopup` while select-mode is on.** This is
    the half that was stated. It is true, and it IS tested —
    `Discover.test.tsx`, "a LibraryCard body click is inert while select-mode
    is on."

    (b) **Select-mode cannot be entered while a popup is open.** This half was
    left out. It is also true today, but for a different kind of reason than
    (a): `Modal` has no focus trap and the toolbar's Select button sits behind
    `DetailPopup`'s own `fixed inset-0 z-50` overlay, so there is no
    click/keyboard path to it while a popup is mounted. That is a
    UI-occlusion argument, not a code guard — nothing in the select-mode store
    or `DetailPopup` refuses to toggle select-mode while a popup is open, it is
    simply unreachable by mouse or keyboard right now. Both directions are
    load-bearing: (a) alone does not rule out entering select-mode WHILE a
    popup is already open, which is exactly the case (b) closes.

    **"Has its own dedicated test" also overstates coverage.** The cited test
    covers only direction (a), and only for `LibraryCard` — it says nothing
    about `PosterCard`/`AdultCard`, and direction (b) has no test at all,
    because it is a layout/occlusion fact, not something the code enforces.
  - **AMENDED 2026-08-01 — bounded unattended Usenet auto-grab exception (a
    third, deliberate, documented reversal; see `docs/ROADMAP.md`'s
    "Eliminate Connections tab; Usenet multi-subscription settings" entry
    (item 3) and `CHANGELOG.md`).** This one is **structurally different
    from both exceptions above, and the difference is the whole reason it
    needs its own bounds**: bulk-apply acts on already-reviewed Pending
    proposals staged by a prior Scan (a review step precedes the batch);
    bulk-grab has no staging step but the operator's own click *is* the
    approval. **This exception has no human action in the loop at all.**
    When it fires — from the 24h retry cycle in
    `internal/api/usenetretry.go` — nobody clicked anything, nobody
    reviewed anything, and a release is dispatched to a download client on
    the system's own initiative. That is a genuine reversal of
    staged-for-approval for one narrow path, not a reinterpretation of it.

    **What bounds it instead of a human, all four enforced in code, not by
    convention:**
    1. **An off-by-default global toggle, `usenet_autograb_enabled`**
       (`PUT /api/settings/usenet-autograb-enabled`, Settings → Usenet).
       Every install starts with unattended auto-grab OFF, and the retry
       scheduler's own opt-in gate (`usenet_retry_interval_seconds`, written
       server-side as a coupled side effect of that toggle: on → 86400,
       off → 0) means the loop does not even start until the operator turns
       it on. (The interval is read at boot, same as every other scheduler
       here, so switching the toggle ON takes effect on restart; switching it
       OFF stops a running loop on its next tick, and `RunAutoGrab` refuses
       every unattended trigger immediately regardless. Worth knowing before
       writing UI copy that promises instant effect.) The gate is enforced in
       exactly ONE place — inside
       `RunAutoGrab` (`internal/api/autograb_shared.go`) — so no trigger can
       route around it. Discover's shipped one-click Grab
       (`TriggerOperator`) is the deliberate exemption and stays **ungated**:
       the operator's click is still the approval, per the 2026-07-24
       amendment above. Gating it would silently break a live feature.
    2. **A qualification predicate that refuses to grab an ungradeable
       release.** Scoring uses `autograb.Select`, not
       `release.ScoreCandidate`, specifically because only the former can
       express "nothing qualified" (`Selection.Fallback` /
       `PickIndex == -1`, driven by `Grade.Qualified`). A candidate whose
       size or runtime is unknown is graded not-qualified and is never
       dispatched — Series season packs (runtime 0 by design) and Adult
       items with no duration therefore never auto-grab, by construction.
       **Do not "fix" this by loosening the floor**; the conservative refusal
       IS the bound.
    3. **No grab without a scored match, under any condition, including
       retry.** A retry re-runs the whole pipeline from the Prowlarr search
       through `autograb.Select`; it never re-dispatches a cached winner and
       never has a "just take the best available" fallback. `RunAutoGrab` is
       the single scoring-and-dispatch path for every trigger
       (`TriggerOperator` / `TriggerRequest` / `TriggerRetry`, plus the
       reserved-but-unimplemented `TriggerAirDate`), so "retry never
       bypasses scoring" is true by construction rather than by review.
       (**`TriggerAirDate` is no longer reserved as of 2026-08-01** — it is
       implemented by `monitorAirDates`, making four live triggers, not three
       plus a placeholder. The property this bound asserts is unaffected: it
       holds for the fourth trigger by the same construction. Quoted and
       corrected in full in the AMENDED 2026-08-01 note under **Automation**
       above; flagged here so a reader who opens this bound directly is not
       left with the superseded enumeration.)
       (**A FIFTH trigger, `TriggerPreRelease`, was added 2026-08-02** —
       Calendar pre-release requests, dispatched by `releaseDueGrabs`
       (`internal/api/prerelease.go`). Five live triggers now, not four. The
       property this bound asserts is again unaffected and again holds by the
       same construction — a held row is *created* by a parker that dispatches
       nothing, so there is no cached winner to re-dispatch, and its only route
       to dispatch is one `RunAutoGrab` call. Quoted and corrected in full in
       the AMENDED 2026-08-02 note under **Automation** above; the category it
       introduces is documented in the AMENDED 2026-08-02 note at the end of
       this list.)
    4. **A permanent-failure path that does not retry, and (as of 2026-08-01)
       no cap on the one that does.** A 451 `ErrArticleRemoved` (DMCA
       takedown) is terminal: `classifyDownloadState` maps it straight to
       `failed` and it is never re-searched. A 430 `ErrArticleNotFound` from
       every configured subscription, and any other unclassified/transient
       failure (a dial timeout, a decode error), are both treated as
       retryable — corrected 2026-08-01 after a Phase-4 review found the
       original transient-error path was silently terminal despite a test
       asserting otherwise (see `internal/api/search.go`'s
       `classifyDownloadState`). This does not widen the safety envelope: the
       toggle, the qualification predicate, and "no grab without a scored
       match" are all unchanged — only which failures get another attempt.
       **CORRECTED 2026-08-01 — the retry-attempt cap (`grabs.maxRetryAttempts
       = 5`) was removed, an explicit product decision, not a bug fix**: a
       `pending_retry` row now retries indefinitely on its normal interval
       instead of eventually becoming `failed`. `retry_count` is still
       incremented on every attempt (observability only — it no longer
       drives any status transition). Known, accepted consequence: an item
       that is structurally ungradeable forever (a Series season pack's
       runtime is always 0 by design, as is a duration-less Adult
       identification) now retries on every cycle indefinitely rather than
       terminating — bounded by the retry interval, not a tight loop, and an
       operator can still manually exclude a request to stop it. `Failed` is
       still a real, reachable status — just only via a genuinely terminal
       classification (the 451 path above), never via retry exhaustion.

    **Honest scope note, verified against the shipped code rather than
    predicted:** the toggle-ON Search hook (`runToggleGatedSearch`, reached
    from both `GET /api/modes/{mode}/search` and
    `GET /api/modes/adult/search`) **can never actually auto-grab.** Both
    routes carry only `?q=`, so `RuntimeSeconds` is 0 in every mode there,
    and `autograb.GradeCandidate` short-circuits on
    `SizeBytes <= 0 || RuntimeSeconds <= 0` *before* reaching the Lossless
    remux/bluray bypass — so not even a remux qualifies on that path. Every
    toggle-ON search parks a `pending_retry` row instead. (The implementation
    plan predicted "only a Lossless-source release can auto-grab here"; that
    prediction was wrong, and the truth is *more* bounded, not less.) The
    rows this feature can genuinely convert into an unattended dispatch are
    the ones the retry loop's GID sweep parks — real, already-dispatched
    grabs carrying a real TMDB id and runtime. For Search-hook rows the
    retry loop provides an indefinite, unsuccessful retry loop (2026-08-01:
    the attempt cap was removed, so this is no longer *termination* — see
    bound 4 above), not eventual success. Both facts are load-bearing before anyone "improves" this: the
    unattended dispatch surface is narrower than the toggle's name suggests.

    **CORRECTED 2026-08-01 (later, same day) — the SECOND half of the scope
    note above is superseded by series air-date monitoring. The first half is
    NOT, and must not be over-corrected along with it.** Superseded claim,
    quoted: *"The rows this feature can genuinely convert into an unattended
    dispatch are the ones the retry loop's GID sweep parks — real,
    already-dispatched grabs carrying a real TMDB id and runtime."* That is no
    longer the whole set. `TriggerAirDate` (`monitorAirDates`,
    `internal/api/airdatemonitor.go`) supplies a real per-episode runtime via
    `seriesEpisodeRuntimeSeconds` (`Episode > 0`), so `autograb.GradeCandidate`
    does **not** short-circuit on `RuntimeSeconds <= 0` and an air-date
    candidate can genuinely qualify and dispatch unattended.

    **State the novelty as VOLUME, not KIND — the stronger claim is false.**
    This is the first **routine, HIGH-VOLUME** unattended-dispatch surface;
    it is *not* the first unattended dispatch of any sort. A GID-swept row
    could already dispatch with no human in the loop before this feature
    existed — the quoted sentence says so itself — so do not re-read this
    correction as "nothing could auto-grab before." The difference is where
    the population comes from: a GID-swept row exists only because an operator
    already grabbed that exact release once and its retrieval failed, so it is
    bounded by past operator action; an air-date row is minted by the
    **system**, for every aired-but-missing episode of every monitored season,
    with no prior operator action for that episode at all. The formal bounds
    are unchanged — same toggle, same scorer, same single dispatch path — but
    the practical blast radius grows by orders of magnitude, which is why the
    per-season monitored flag defaults to **off** (an absent row means
    unmonitored, so nothing fires on upgrade until an operator monitors a
    season).

    **The *"can never actually auto-grab"* clause above is still ACCURATE and
    stays.** It is about the toggle-ON Search hook (`runToggleGatedSearch`)
    specifically, whose routes still carry only `?q=` and which still parks a
    `pending_retry` row every time. Air-date monitoring is a different trigger
    on a different path; it does not touch that hook.

    It is still NOT a queue-wide "grab everything", not cross-mode
    auto-approval, and not a loosening of staged-for-approval anywhere else
    — Rename/Purge/Dedup/Tag Apply, and every non-usenet grab path, are
    completely untouched.

    - **Documented spec deviation, recorded in the same amendment because a
      future session will look for it here: "queried in parallel" was
      reinterpreted as a RETRIEVAL-stage fan-out, not a search-stage one.**
      The spec literally said *"all configured subscriptions are queried in
      parallel (no priority ordering)… Results are ranked via the codebase's
      existing scoring mechanism."* Read literally, that asks each Usenet
      subscription to return searchable results to be scored. **That is not
      implementable, because NNTP has no search verb** — an NNTP server
      offers `ARTICLE`/`BODY` retrieval by message-ID and nothing else
      (`internal/usenet/pool.go`), and header enumeration is an
      indexer-scale operation, which is exactly what Prowlarr already is.
      Neither scorer has a single field an NNTP server could populate
      (`release.Candidate` and `autograb.Candidate` both take release
      metadata: title, size, resolution, codec, seeders, publish date).
      What was built instead is a **two-stage pipeline**: ONE Prowlarr
      search produces the usenet candidates → `autograb.Select` scores them
      and picks a winner (or refuses) → the winner's NZB is parsed to
      segments → **the fan-out across subscriptions happens at segment
      RETRIEVAL.** This is also the real-world reason an operator runs
      multiple providers — differing retention and takedown coverage, i.e.
      article *availability*, which is a retrieval concern, not a search
      one. Nothing in the spec is lost: "no priority ordering" holds
      (there is no priority field, no reorder UI, and no ranked-chain
      concept anywhere — `frontend/src/screens/settings/Usenet.tsx`
      documents this as a thing the page must never grow); "ranked via the
      existing scoring mechanism" holds (`autograb.Select`, an existing
      scorer, not a new one); "highest-scored candidate auto-downloads with
      no human review step" holds; "no match → stays pending, retries every
      24h" holds at *either* stage — no qualifying candidate, or no
      subscription holding the articles, both park the same
      `pending_retry` row; and "retry never bypasses scoring" holds because
      the retry restarts at the Prowlarr search.
      **Precision worth having, since one in-code comment overstates it:**
      per-segment subscription probing is *sequential fallback*, not
      simultaneous — `fetchSegmentAny` tries pools in turn until one holds
      the article, deliberately, because probing every server at once would
      download N copies of one article body for N times the bandwidth and
      connection consumption. The concurrency is across *segments* (an
      `errgroup` limited to the summed `MaxConns` of all enabled
      subscriptions), so multiple subscriptions genuinely do serve one
      download simultaneously, and every enabled subscription is tried for
      every segment. Read "queried in parallel" as **"every enabled
      subscription is tried for every segment, with no configurable
      priority, and segment fetches run concurrently across all of them"** —
      that is what ships. `Usenet.tsx`'s header comment says "queried in
      parallel with no ordering at all", which is right about the *ordering*
      and loose about the *parallelism*.
    - **AMENDED 2026-08-01 — torrent seeding + stale-torrent auto-cancel added
      destructive scheduler work (a fourth, deliberate, documented reversal;
      see `docs/ROADMAP.md` item 7 and `CHANGELOG.md`).**
      `downloader.pollLoop` now performs two unattended destructive actions
      that widen what that scheduler does, even though neither widens the
      auto-grab exception itself:
      **(1) Stale-torrent auto-cancel + cleanup.** When a torrent has zero
      progress, no peers, and has been idle past a configured threshold
      (default 4h, configurable via Settings → Torrent Behavior Settings,
      off-by-default via threshold = 0), `downloader.pollLoop` calls
      `Manager.Cancel(gid)`, which deletes the staged partial files from
      `StagingDir`. This is cleanup only — identical to a manual operator
      Cancel, and the library is never touched (staging-only, checked via
      `deleteDownloadFiles`' containment guard). No human review needed
      because the torrent is definitively dead (zero progress + no peers
      together mean no possibility of eventual success). The requeue step
      (parking to `pending_retry` via `parkGrabForRetry`) routes through
      `RunAutoGrab` under the existing `usenet_autograb_enabled` gate with
      no new bypass (§5 Pattern A, unchanged), so the exception surface does
      not widen.
      **(2) Ratio/duration-limit seeding cleanup.** When a torrent is seeding
      and hits a configured ratio or duration limit (either first), seeding
      stops and the staging-side seeding copy (not the library import) is
      deleted — same cleanup, same containment. Again, no new gate needed
      (seeding itself is `off` by default). This is purely an operational
      cleanup, not an acquisition or library mutation.
      **Extension patterns, load-bearing for the next auto-grab trigger
      source** (`series-monitoring-autograb`): there are two distinct ways to
      extend the shared retry mechanism. This spec uses **Pattern A ("new
      parker")**  — detect a condition (stale torrent), call
      `parkGrabForRetry()` to re-park an existing grab row for retry, and
      stop. The existing `retryDueGrabs` loop picks it up on the next cycle.
      **Pattern B ("new trigger")** is for initiating a first grab with no
      prior row — add a new `AutoGrabTrigger` const, add the detection logic
      where it's observable, call `RunAutoGrab()` with the new trigger, and
      stop. The difference is not academic: Pattern A re-arms a row by
      `ExistingGrabID`, so a stalled title on retry retains its TMDB id and
      metadata; Pattern B creates a fresh row. `series-monitoring-autograb`
      must use Pattern B because there is no prior row. Do not use both for
      the same trigger source.
  - **AMENDED 2026-08-02 — bounded DEFERRED-OPERATOR-APPROVAL exception:
    Calendar pre-release requests (a fifth, deliberate, documented reversal;
    see `docs/ROADMAP.md` items 4 and 8 and `CHANGELOG.md`).** Placed at the end
    of this list rather than adjacent to any note above it because it is a
    genuinely NEW category, not a correction to an existing one. An operator
    clicks an unreleased movie in Calendar's Upcoming view; that click mints a
    **held** grab row which sits dormant — no search, no dispatch, no indexer
    traffic — until its release date, potentially months later, at which point
    `releaseDueGrabs` dispatches it via `TriggerPreRelease` with nobody
    watching. The category sits BETWEEN the two exceptions above, which is
    exactly why neither of them covers it:

    | | `TriggerOperator` (2026-07-24) | **`TriggerPreRelease` (this note)** | `TriggerAirDate` (2026-08-01) |
    |---|---|---|---|
    | Human action | a click | **a click** | none |
    | Time from click to dispatch | immediate | **days to months** | n/a |
    | Who mints the row | the operator | **the operator** | the system |
    | Human present at dispatch | yes | **no** | no |
    | Per-cycle dispatch cap | n/a (ungated) | **`maxPreReleaseGrabsPerCycle = 20`** | `maxAirDateGrabsPerCycle = 20` |

    Call it **deferred operator approval**: *narrower* than `TriggerAirDate`
    (one explicitly chosen title per click, not a system-minted per-episode
    sweep) and *broader* than `TriggerOperator` (nobody is present when it
    fires). The 2026-07-24 bulk-grab amendment turns on "the operator's own
    click *is* the approval," which assumes the click and the dispatch are the
    same event; the 2026-08-01 usenet amendment turns on "no human action in
    the loop at all," which is not true here. **The four formal bounds in that
    amendment are all unchanged** — same off-by-default toggle enforced in the
    same single place inside `RunAutoGrab`, same `autograb.Select`
    qualification predicate, same "no grab without a scored match," same
    permanent-failure path. Bound 3's trigger enumeration goes from four to
    five; that correction is quoted and made in full in the AMENDED 2026-08-02
    note under **Automation** above.

    **The origin marker is a real database column, `grabs.hold_until`
    (migration `0057`), and WHY it is a column is the most important thing in
    this note.** A row with a non-empty `hold_until` originated as a Calendar
    pre-release request, and nothing else in the codebase can produce one. That
    is a queryable fact, not an inference from `status` + `retry_reason` +
    `retry_count`. Wade chose this design (Option B) on 2026-08-02 over a
    cheaper no-schema-change alternative, citing by name the HIGH-severity
    misclassification bug just fixed in `series-monitoring-autograb`, where
    origin *was* inferred from row shape and a reason string — the inference
    misfired and destroyed an operator's own grab (see `airdatemonitor.go`'s
    `airDateShaped`/`airDateOriginated` and their doc comments). The plan
    argued the other way through two Critic passes and its own evidence had
    moved against it by the end; the full audit trail is in
    `.omc/plans/autopilot-impl-calendar-grabs-requests.md` §5.4. **Do not
    "simplify" this back into a reason-string discriminator.**

    Two further properties of the column are load-bearing. It is **never
    cleared**, not even after the row is promoted and dispatched — provenance
    outlives the hold, and `DueForRelease`'s `retry_after = ''` guard, not a
    clear, is what makes promotion fire exactly once. And a **partial unique
    index**, `idx_grabs_held_request ON grabs (mode, tmdb_id) WHERE hold_until
    != ''`, enforces at-most-one-held-request-per-title in the DATABASE rather
    than in the route's check-then-act. **That index was added during Wave 2
    review, not in the original design**, and it closes a real hole: two
    genuinely concurrent clicks (a double-click, two tabs) can both miss
    `FindHeldRequest` and both `Create`, and nothing downstream can repair it —
    the shared `nonHeldMovieWork` predicate self-excludes EVERY held row, not
    just the one under evaluation, so promotion-time suppression cannot tell
    two held rows apart; both promote, both dispatch, one film downloads twice.
    It is PARTIAL because ordinary grabs legitimately repeat a
    `(mode, tmdb_id)` pair (a re-grab, a retry that minted a fresh row) and a
    total unique index would break every one of them. The migration's `Down`
    must **drop the index BEFORE the column** — SQLite refuses to drop a column
    named in a partial index's `WHERE` clause — and
    `internal/db/migration_grabs_hold_until_test.go` is what catches a future
    edit that reverses that ordering.

    **Dispatch is Pattern B; creation is a PARKER — and that split is legal.
    Recorded because it is the one place a reader could reasonably think the
    "do not use both for the same trigger source" rule directly above was
    broken.** That rule forbids two *dispatch* paths for one trigger source.
    There is exactly one here: `releaseDueGrabs` →
    `RunAutoGrab(TriggerPreRelease)`, textbook Pattern B. The row's *creation*
    is a parker (`parkPreReleaseRequest`) instead, because a held row is minted
    by a click rather than by a search, and a parker dispatches nothing — so
    the row is never a "cached winner" being re-dispatched; it was never
    dispatched at all. An earlier draft of the plan proposed naming this a
    third pattern, "Pattern C"; **that name is retired** — it was an artifact of
    the rejected design. Without this note a future session will spend a review
    cycle rediscovering that "parker + Pattern B" is legal.

    **The gate-flip burst is real, and it is capped — both halves matter.** On
    a default install nothing fires at all (the toggle is off, so the cycle
    never starts), so an operator can browse Calendar for months and accumulate
    dozens of held rows before ever enabling unattended auto-grab. The first
    cycle after they enable it sees that whole accumulated backlog come due at
    once. The burst is bounded by **`maxPreReleaseGrabsPerCycle = 20`**
    (`internal/api/prerelease.go`) — same value, same precedent as
    `maxAirDateGrabsPerCycle`; the rest stay held for the next cycle. This
    bound is affordable precisely BECAUSE `releaseDueGrabs` is a loop this
    feature owns. `retryDueGrabs`' own loop remains uncapped — a pre-existing
    property this feature does not contribute to and did not change.

    - **Three documented spec deviations, recorded in this amendment because a
      future session will look for them here** (same register as the usenet
      amendment's "queried in parallel" reinterpretation above):
      1. **AC6's "the same flow Discover's Grab button uses" was NOT taken
         literally.** That flow searches and dispatches immediately, which this
         feature's own dormancy constraint forbids. What is genuinely shared is
         the **row type and the dispatch gate**, not the entry point.
      2. **"Region-aware" was read as "region-scoped to US, by the convention
         `HasUSRelease` already established."** That helper is hard-coded to US
         and the spec explicitly declined to design a new TMDB date-resolution
         heuristic, so this is a named constant — not a new settings key, a new
         Settings control, or a migration.
      3. **AC9 lists History's completed-only filtering under the
         `go test ./...` bullet, but its test is TypeScript.** The filter is
         deliberately client-side (`listGrabsHandler` stays general), so the
         test lives in the frontend suite. That is the right engineering call,
         and it means one AC-named assertion does not exist as a Go test.
  - **AMENDED 2026-08-09 — Rename gains a DESTRUCTIVE third bulk outcome, and
    the bulk-apply exception's own description needs one clarification.** The
    2026-07-17 bulk-apply amendment says the operator "checks several
    already-reviewed Pending rows/groups from ONE workflow+mode screen and
    clicks 'Apply Selected,' which POSTs **one** `apply-batch` request." That
    was already loose — Rename's "Apply Selected" has always issued one
    `apply-batch` call **plus N per-id `dismiss` calls**, partitioned
    client-side in `confirmApplyAll` (`Rename.tsx`). This feature adds a
    **third** partition, `POST /api/proposals/delete-batch`, which
    **permanently deletes the source file from disk (`os.Remove`) and
    destroys the proposal row entirely** — no `Dismissed` status, no history
    entry, no trash, no undo.

    **This is NOT a new auto-grab-style exception and adds no unattended
    anything.** Staged-for-approval is fully intact: the operator picks the
    action per row, reviews every row in `ApplyAllConfirm`, and clicks
    Confirm. It is recorded here because it is the **first irreversible file
    deletion reachable from Rename's UI** — Dedup and Purge already delete
    files, Rename never did.

    **Four bounds, all enforced in code:** (1) Pending/Unmatched **Rename**
    proposals only, re-checked **server-side** on the resolved row, so a
    hand-crafted request cannot delete an Applied proposal's file or reach a
    Dedup/Purge row; (2) the deleted path is always the **resolved DB row's**
    `source_path`, never a client-supplied path; (3) capped at
    `MaxProposalPageSize` (200) — a **deliberate divergence** from Rename
    apply-batch, which is uncapped, because an uncapped destructive request
    is not the same risk as an uncapped rename; (4) `mode.Build` runs
    **before** any `os.Remove`, which is the **only** thing enforcing the
    Adult section lock on `/api/proposals/*` (that route classifies as
    `{organize}` only — see `dismissProposalHandler`'s doc comment for the
    same trap). **Reordering `mode.Build` after the deletion for "efficiency"
    silently deletes Adult files while Adult is locked.**

    **`applyBatchHandler` and `ApplyBatchItem` are UNTOUCHED, deliberately.**
    Routing delete through an `action` field on apply-batch would have fired
    `workflowEvent(Rename)` webhooks and logged `KindApplyBatch` activity for
    destroyed files — corrupting the one durable record a "leave no trace"
    deletion has. See `.omc/plans/autopilot-impl.md` §1.3.

    **Only trace a delete leaves anywhere:** the `delete_batch` organize-event
    row — whose `workflow`/`mode` MUST be captured from the first resolved
    proposal *inside* the delete loop. Copying `applyBatchHandler`'s
    post-loop `propStore.Get(req.Items[0].ID)` writes an empty workflow (the
    row is gone by then), and `organize_events` is queried `WHERE workflow =
    ?`, so the entry would never render on the Rename activity log. Treat
    that log line as load-bearing.

    **Two documented spec deviations, recorded here because a future session
    reading the spec alone would think the implementation is wrong:** (1)
    AC #5 said the delete path is "added to the batch-apply flow"; it is a
    **separate** `delete-batch` endpoint instead, because routing it through
    `applyBatchHandler` would fire rename webhooks and log `KindApplyBatch`
    for destroyed files. The AC's premise — that a server-side apply/dismiss
    dispatch exists to extend — was factually wrong; the partition is
    client-side, so a separate endpoint *is* the existing pattern. (2) AC #10
    asked for a **Go** test covering a mixed rename+dismiss+delete batch;
    that is structurally impossible under three separate endpoints, so the
    mixed-dispatch assertion lives in the frontend suite
    (`Rename.delete.test.tsx`, "a mixed confirm issues apply-batch, per-id
    dismiss, and ONE delete-batch") and the Go suite covers delete-only
    skip-and-continue. See `.omc/plans/autopilot-impl.md` §9.

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
  - **AMENDED 2026-08-03 — a configurable section PIN lock ships, and it
    makes THREE sentences of the paragraph above false. Authorization scope
    is no longer flat; identity still is (see `docs/ROADMAP.md` item 11 and
    `CHANGELOG.md`).** Recorded as a sub-bullet rather than a rewrite, per
    this file's append-and-correct convention. The three sentences, quoted
    verbatim so a future session can find them:
    1. *"No permissions system, no per-user roles —
       one login gates the whole app, across all three
       supported auth strategies (`password`, `oidc`, `none`)."*
    2. *"No user table, no roles, no permissions surface is introduced by any
       mode."*
    3. *"a key inherits the one operator's full access in every mode, it is
       not a second user or a permissions surface."*

    **Sentence 1 is still true of IDENTITY and now false of AUTHORIZATION
    SCOPE — the distinction is the whole point.** There is still exactly one
    login, one operator, and one credential set; nothing here authenticates a
    second party. What changed is that satisfying that one login no longer
    reaches every route: an operator can set a PIN and check which sidebar
    tabs (plus a dedicated "Adult content" pseudo-section) it protects, and a
    request that clears primary auth but presents no PIN is refused with
    **403 `section_locked`**.

    **Sentence 2 is now false as written: a per-section lock IS a permissions
    surface, narrowly scoped.** Still no user table and still no roles — but
    "no permissions surface" overstates it, and pretending otherwise would
    hide the one property a future session most needs to know before building
    against this.

    **Sentence 3 is the load-bearing one and is DIRECTLY false.** A key no
    longer inherits the operator's full access: on a locked section it must
    ADDITIONALLY present `X-Section-Pin: <PIN>`. That makes `X-Api-Key` a
    genuinely scoped credential for the first time — a key with no PIN header
    has strictly less access than a browser session holding an unlock ticket.
    This is a real, narrow divergence from the single-operator model, not a
    wording nit.

    **The anti-RBAC invariant, which is what keeps this from becoming a role
    layer: ONE PIN, ONE unlock ticket, and that ticket unlocks EVERY locked
    section at once.** There are no per-section PINs and there will not be.
    Unlocking `discover` therefore also unlocks `adult-content`. The ticket
    carries no identity and no per-section scope — it is a boolean "the
    operator entered the PIN recently" (absolute 30-minute TTL, non-sliding).
    With a single shared secret any other design would be security theatre
    and a de-facto role layer. **Do not add per-section PINs.**

    **The lock is INERT in auth mode `none` (GATE-1, decided deliberately).**
    The `none` banner already declares the instance fully open, and there is
    no authenticated caller to bound a config write, an unlock, or the
    brute-force counter — enforcing there would create an unauthenticated
    remote-brick surface and false assurance. The control endpoints answer
    **409** and the Settings panel renders disabled. **`SAKMS_SECTION_LOCK_DISABLE=1`**
    is the full-disarm env var: no section is enforced, and `PUT`/`DELETE
    /api/section-lock/pin` stop requiring `currentPin`, which is the only way
    out of a corrupt bcrypt hash. Both are documented in
    `docs/break-glass-recovery.md`.

    **Locking `settings` now also gates `/api/nodes*`, and that is a REAL
    BEHAVIOR CHANGE worth knowing before it surprises someone.** A
    gap-closure pass found the node-management routes classified as `settings`
    but structurally unreachable by the gate: `NewNodesMux` builds its own
    `auth.Middleware` internally rather than inheriting `cmd/sakms`'s single
    wrap, so the gate options had to be threaded through the constructor to
    reach them at all. Seven operator routes were affected (the five `op`
    routes plus `dualAuth`'s operator branch). **The node-agent bearer routes
    are deliberately NOT gated** — an agent holds no PIN and has no
    interactive surface to enter one, so gating them would take every node
    offline the moment `settings` was locked. `sectionlock_sl10_test.go` is
    what caught this and is what keeps it covered; a static test proving a
    route is *classified* is not the same as one proving it is *reachable by
    the gate*, and only the second kind would have found this.

    **MIGRATION NOTE — this is a breaking change on first use, not a
    hypothetical.** Every existing out-of-process script or automation hitting
    a route that becomes gated will get a **403 it has never seen before, the
    moment an operator locks that section for the first time** — including the
    newly-fixed `/api/nodes*` operator routes. Nothing breaks at upgrade time
    (no section is locked by default and no PIN exists), so the failure
    surfaces later, detached from the change that caused it. The fix for any
    such script is to add `-H "X-Section-Pin: <PIN>"`; there is no
    grandfathering path and no per-key exemption, by design — an exempt
    credential would be a hole straight through the feature.
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
  grab → check-import. The one-time `internal/sonarrimport` migration tool
  ("Import from Sonarr" in Settings) has been removed entirely (2026-07-12),
  same decision and reasoning as Whisparr's importer below — there is no
  Sonarr connection type anymore; `TestConnection` has no `"sonarr"` case,
  and Settings' Connections table doesn't list it. `internal/servarr`'s
  Sonarr support is kept (still a valid generic capability, same precedent
  as Radarr) even though nothing in `mode.Build` constructs one anymore.
  Series Dedup is built too
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
  identification (`mode.Session.Stash`, the phash-first `rename.ScanLibraryAdult`
  path reads a phash Stash already computed; SAK never computes one) and for
  player-rescan-notify (`Session.NotifyPlayers`); it is a downstream player,
  never an organizational authority, exactly like Jellyfin for Movies/Series.
  The one-time `internal/whisparrimport` migration tool ("Import from
  Whisparr" in Settings) has been removed entirely (2026-07-12) — a fresh
  install has no prior Whisparr history to migrate from, and Wade confirmed
  the library can always be rebuilt directly from the filesystem instead.
  There is no Whisparr connection type anymore; `TestConnection` has no
  `"whisparr"` case, and Settings' Connections table doesn't list it.
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
- **TMDB response cache (2026-08-02)**: `internal/tmdb/cache.go` — a
  fixed-capacity LRU with per-entry TTL, sitting behind `Client.do()`. `do()`
  is the single chokepoint every one of the ~35 `*Client` methods routes
  through, so caching the raw response body there makes **every TMDB call in
  the codebase** cached by one change, with no per-method work. Behavior on a
  miss is byte-for-byte what it was, error strings included; the one real
  behavior change is that a second identical call inside the TTL does not hit
  the network.
  - **It is a package-level singleton, and that is load-bearing, not a
    shortcut.** `mode.Build` constructs a brand-new `tmdb.Client` on every
    call, from 33 non-test call sites that are essentially all
    per-HTTP-request handlers — so a cache field on the `*Client` value would
    live exactly as long as one request and would never serve a single hit.
    The in-repo precedent is `internal/imageproxy`, whose cache is a
    process-lifetime singleton constructed once in `main.go` for the same
    reason. A package var rather than a `main.go` construction because the
    alternative is threading a `*Cache` parameter through `mode.Build`'s 33
    call sites plus every test that builds a session, for zero behavioral
    gain — priced and rejected, not overlooked.
  - **10-minute TTL, 512 entries, 256 KB per-entry cap — worst case 128 MB,
    typically far less.** The per-entry cap is not optional: `do()` is shared
    with heavy endpoints (a long-running show's aggregate credits runs to
    hundreds of KB) and per-entry size is otherwise bounded only by
    `httpx.MaxResponseBodySize` (10 MB), so 512 entries alone bounds nothing
    useful. An oversized body is simply not cached and re-fetches. The TTL is
    a deliberate compromise, not a tuned value: season/episode data is
    near-immutable and would happily take an hour, but `/trending` and
    `/discover` rows visibly should not, and a per-endpoint TTL table is
    rejected as premature. If it proves wrong the symptom is observable
    (Discover rows not refreshing) and the knob is two constants in one file.
  - **Errors are never cached.** Non-2xx responses, transport failures, and
    bodies that fail to unmarshal all return before the `put` — a TMDB blip
    must not blank a Discover row for the whole TTL. Nothing is cached
    negatively either: a 404 just re-fetches. No singleflight, so two
    concurrent misses for one key both fetch — the same explicitly accepted,
    self-correcting waste `internal/imageproxy` documents.
  - **The cache key is computed BEFORE `api_key` is written into the query**,
    with a short non-reversible fingerprint of the key appended instead. So
    the operator's TMDB key is never stored in (or recoverable from) a
    process-lifetime map key, and rotating it invalidates every entry
    immediately rather than serving a revoked key's responses for up to a TTL.
  - **Test isolation is a real hazard here, in two places.** In-package tests
    give each client its own fresh cache (a reused ephemeral `httptest` port
    would otherwise collide two tests' keys and silently serve an earlier
    test's body). `internal/api` tests cannot reach the field at all — they
    swap `tmdb.DefaultBaseURL` and build through `mode.Build` — so
    `internal/tmdb` exports a test-only `ResetDefaultCache()` for them to call
    in setup. Two `internal/api` tests that differ only in a *request-level*
    detail the upstream TMDB traffic does not reflect will otherwise have one
    of them fully cache-served — passing vacuously, or failing confusingly.
  - **AMENDED 2026-08-03 — `Config.BypassCache` added; the "complete consumer
    list" in `internal/tmdb/cache.go`'s doc comment now names two non-`mode.Build`
    consumers, not one (discover-scheduled-refresh plan §7.1).** A client built
    with `BypassCache: true` skips this cache in both directions (no read, no
    write) — `internal/discoverrefresh`'s scheduler is the second such consumer,
    after `internal/recheck`'s own `tmdb.Client` (`recheck.go:222`). It bypasses
    deliberately: this cache's 10-minute TTL sits behind every `tmdb.Client` a
    manual "refresh now" would otherwise silently hit within, turning a
    troubleshooting action into a no-op that reports success without actually
    re-fetching anything (see `Config.BypassCache`'s own doc comment,
    `internal/tmdb/client.go:78-97`, for the full three-interaction analysis).
    Unlike `internal/recheck`, which inherits the shared cache harmlessly (its
    repeated reads are exactly what a cache benefits), `internal/discoverrefresh`
    opts OUT of it entirely — its own read-through row cache (a different,
    24h/operator-tunable store) is the one that is supposed to absorb repeat
    reads here, not this 10-minute LRU.
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
- **Frontend (SolidJS + Vite SPA, Seerr-inspired redesign, 2026-07-13)**:
  the frontend is a SolidJS + Vite single-page app, compiled at build time to
  static HTML/JS/CSS and embedded in the Go binary exactly as before
  (`internal/web`, `//go:embed static`). **No Node.js runs in production** —
  the binary still just serves static files, only a richer generated bundle.
  The old 2,284-line hand-written vanilla-JS `static/index.html` was removed
  in an atomic cutover: `pnpm build`'s output now *is* the embedded `static/`
  tree (whole dir gitignored), so a bare `go build ./cmd/sakms` fails cleanly
  (`pattern static: no matching files found`) until the frontend is built —
  the Dockerfile's discarded Node stage does this automatically. Load-bearing
  decisions the redesign preserves (don't quietly reverse them):
  - **Single-operator, staged-for-approval** carry over verbatim — the
    visual style is from Seerr, its multi-user request/approval model is
    explicitly NOT (see `frontend/SEERR_SCOPE.md`). Bulk apply (2026-07-17,
    see the amended note above) is an opt-in, same-screen multi-select of
    already-reviewed proposals within sakms's own single-operator model —
    not Seerr's multi-user bulk request pattern, which stays out of scope.
  - **API request/response types are generated from Go DTOs**
    (`internal/apidto` → `ts/dto.gen.ts` via `go run ./cmd/gendto`), never
    hand-duplicated. The `*T`/`omitempty` three-state secret rule
    (nil = preserve stored secret, `""` = clear, `"x"` = set) is mapped
    deliberately in the client — a naive `apiKey?: string` would silently
    wipe secrets on an untouched save.
  - **Images are proxied** through the Go backend (`/api/images/proxy`),
    never hot-linked from TMDB/TPDB — keeps operator browsing off those hosts
    and traffic same-origin behind the internal-only middleware.
  - **Auth boot is the single highest-risk piece** (a regression is a total,
    break-glass-only lockout): a 3-way branch (setup / login / app) across all
    three modes, OIDC as a full-page redirect (never XHR), and the break-glass
    `X-Api-Key` recovery path all survive — see `frontend/src/auth/boot.ts`.
  - **One-click auto-grab** is gated by a *separate* bitrate-quality-floor
    scorer, `internal/autograb` — deliberately distinct from and permanently
    coexisting with `internal/release.ScoreCandidate` (which still ranks the
    manual Search view). See `internal/autograb`'s package doc for the full
    reconciliation and why there are two scorers, not one.
  - **Break-glass after a wipe**: Only a failed deploy's
    rollback, or an explicit manual `--wipe-data` CLI flag, wipes the DB +
    `secret.key`; either case logs a fresh `X-Api-Key` once on the next start
    (`main.go:104`). If a deploy lands with a broken auth-boot shell after a
    wipe, that key is the recovery net — the retrieval + use procedure is in
    `docs/break-glass-recovery.md`.
  - **AMENDED 2026-08-02 — the frontend has an ICON LIBRARY now, and exactly
    ONE `RowEditor` (see
    `.omc/plans/autopilot-impl-row-dnd-consolidation-and-pagination.md`,
    `docs/ROADMAP.md` item 2, and `CHANGELOG.md`).** Two claims elsewhere in
    this file's frontend material are superseded; both are quoted rather than
    rewritten, per this file's convention.

    **1. `lucide-solid` is the frontend's FIRST icon dependency — 3 runtime
    deps → 4.** Before this, every icon in the app was either a hand-rolled
    inline SVG (`Carousel.tsx`'s chevrons, the sidebar's icon set) or a unicode
    glyph. A real dependency was chosen deliberately over a local
    `icons.tsx` of inline SVGs, which would have been smaller and
    dependency-free — Wade chose the real library explicitly. It is used for
    the new Edit/Re-scan **action-slot icons only**; the existing hand-rolled
    chevrons and sidebar icons were **not** swapped, and swapping them is
    unrequested scope.

    **Deep per-icon imports are MANDATORY, and this is a real resolution
    hazard, not a style preference:**
    ```ts
    import Pencil from "lucide-solid/icons/pencil";      // correct
    import { Pencil } from "lucide-solid";               // NEVER
    ```
    The barrel re-exports ~1500 icons (dev-server transform cost, and a real
    risk of a non-tree-shaken chunk). Worse, the package's `exports["./icons/*"]`
    map lists `"solid": "./dist/source/icons/*.jsx"` **before**
    `"import"`/`"browser": "./dist/esm/icons/*.mjs"`, and export-condition
    matching is first-match-wins in key order. `vite-plugin-solid` injects the
    `solid` condition, so both Vite and Vitest can resolve to **raw uncompiled
    JSX inside `node_modules`**; `vitest.config.ts` additionally pins
    `resolve.conditions: ["development","browser"]`, which interacts with it.
    The deep import alone proved sufficient here — **no `optimizeDeps.exclude`
    and no `test.server.deps.inline` were needed**, and neither is present.
    If a future icon or a `lucide-solid` upgrade breaks with a JSX syntax error
    from inside `node_modules`, that is this hazard: the remedy ladder is
    (1) deep import, (2) `optimizeDeps: { exclude: ["lucide-solid"] }` in
    `vite.config.ts`, (3) `test: { server: { deps: { inline: ["lucide-solid"] }}}`
    in `vitest.config.ts`. Icon slugs are kebab-case and Lucide renames icons
    between majors — verify a slug exists under
    `node_modules/lucide-solid/dist/esm/icons/` before importing it.

    **2. There is exactly ONE `RowEditor` and ZERO duplicate solid-dnd wiring
    anywhere in the frontend.** SliderAdmin, RssFeedAdmin, AdultRowAdmin,
    Mainstream and Adult Discover all render
    `screens/discover/RowEditor.tsx`. SliderAdmin's ▲▼ pair, its `SliderRow`
    and `move()`, and RssFeedAdmin's entire parallel `@thisbeyond/solid-dnd`
    block and `FeedRow` are all deleted. `@thisbeyond/solid-dnd` is imported in
    exactly one file.
    - **`reorderAdultFeedIds` (`screens/settings/RssFeedAdmin.tsx`) SURVIVES,
      and it is NOT drag wiring — do NOT delete it in a future dedup sweep.**
      It sits immediately below the solid-dnd import block that WAS deleted and
      looks exactly like part of it. It is the **adult-subset→full-id-list
      mapping**: `reorderRssFeeds` → `Store.Reorder` demands the full set of
      every existing feed's ids exactly once across ALL targets (Mainstream can
      create movie/tv feeds too), so without it a reorder 422s as
      `ErrReorderMismatch` on any install that also has a movie/tv feed. Its
      own unit tests are the backstop.
    - **Discover's rows deliberately carry NO actions**, and `RowEditor`'s
      `actions` slot being optional and generic is exactly why nothing
      structural stops that from changing. A test is what stops it —
      `Discover.test.tsx`, "the row editor carries NO per-entity actions
      (Settings-only)" — following the
      `TestAdultDescriptionMakesNoProwlarrCall` precedent this file names for
      guarantees of this shape. It asserts absence **structurally** (no
      `Edit …`/`Re-scan …` control and no `<svg>` in any row), not "nothing
      threw."
    - **Accepted accessibility trade-off, recorded rather than glossed over:**
      SliderAdmin's ▲▼ buttons were that screen's only keyboard-reachable
      reorder path, and drag has no keyboard equivalent — so **keyboard-only
      reorder access is REMOVED on that one screen.** Explicitly accepted
      (single-operator, low-stakes: row order is cosmetic, and every other
      control on the row stays keyboard-reachable), and consistent with
      RssFeedAdmin and Discover, which have been drag-only all along.
    - **Rows on both Settings screens are TITLE-ONLY now**, so two subtitles
      disappeared. Neither is a loss: SliderAdmin's filter-type/value/target
      summary is in `SliderForm`, reached by the new Edit action;
      RssFeedAdmin's `protocol:` line moved into a new `ProtocolStatusDialog`,
      which fires on Re-scan (scanning → detected/error) **and on a successful
      Add** — without that second trigger an operator would have no way at all
      to learn what protocol was auto-detected for a feed they just created.
      **`ProtocolStatusDialog` is status-only and NEVER collects a protocol.**
      `ProtocolFallbackDialog` remains the sole manual-pick path, on the
      inconclusive-422 branch only, preserving that file's "protocol is a
      derived, read-only fact, never an operator-typed field" invariant.
    - **Documented spec deviation:** AC2 said Re-scan "opens the existing
      status dialog." No such dialog existed — the only one was
      `ProtocolFallbackDialog` ("Choose a protocol"), whose whole job is
      collecting an operator's manual pick, so wiring Re-scan straight to it
      would have broken the derived-protocol invariant. Read as "**a** status
      dialog, from which the existing fallback picker is still reached."

    **3. Carousel pagination.** The forward chevron is now a **track-trailing-
    edge overlay** (absolutely positioned in a `relative` wrapper around the
    scroll track) and is **visible on touch viewports too**. Three things about
    it each look like a bug from the outside and are not:
    - It is anchored to the wrapper's right edge, **not measured onto whichever
      card is currently last-visible**. Netflix's own affordance is
      viewport-edge-anchored, and per-card measurement would mean a
      `getBoundingClientRect` sweep of every child on every scroll frame in a
      handler that already runs `updateScrollState` + the lazy-load check — and
      `Carousel` is generic over `renderItem`, so it can assume nothing about
      child geometry. **Do not build per-card measurement.**
    - **The back chevron is unchanged and still `hidden sm:flex`** (desktop-
      only). So on touch a row has a forward overlay and no back arrow, relying
      on native swipe to go back. That asymmetry is what the spec asked for —
      **do not "fix" it.**
    - The native scrollbar is hidden via a new `no-scrollbar` Tailwind v4
      `@utility` (`index.css`) applied to **`Carousel` ONLY**.
      **`PaginatedStrip` (`screens/discover/shared.tsx`) deliberately keeps its
      native scrollbar**, and every Adult Discover row uses `PaginatedStrip` —
      so Mainstream/Trakt/slider/RSS rows show no scrollbar while Adult's rows
      do. That asymmetry is spec scope-boundary compliance, not an omission;
      unifying it is a deferred follow-up in `docs/ROADMAP.md` item 2.

    Scroll **mechanics** are semantically unchanged throughout —
    `LOAD_MORE_THRESHOLD_PX`, `SCROLL_STEP_RATIO`, `updateScrollState`,
    `onScroll`, `scrollByStep`, the item-count effect and the bounds-aware
    disable are all untouched. `scrollbar-width: none` removes the scrollbar's
    *rendering*, not the element's overflow behaviour, so
    `scrollWidth`/`clientWidth`/`scrollLeft` are unaffected. Note that
    `.omc/plans/autopilot-impl-discover-scheduled-refresh.md` cites
    `Carousel.tsx` **by line number**; those citations have drifted, but every
    symbol they name is semantically intact.
- **Sidebar + section-tab redesign (2026-07-13)**: the horizontal top nav was
  replaced with a collapsible left sidebar (icon-only when collapsed,
  localStorage-persisted). A generic `useScreenTabs`/`ScreenTabs` mechanism
  (`frontend/src/components/ui.tsx`) lets any screen register its own tab set
  with the shell's one consistent tab-bar slot — Settings uses it to split
  into Connections/Auth/AI/Library/Advanced tabs instead of one long scrolling
  page; Discover uses it for a Mainstream/Adult split, where Mainstream merges
  Movies+Series into paginated Trending/Popular rows plus a paginated
  existing-library row and a search bar.
  - **Discover never queries Prowlarr, full stop.** An earlier version of this
    redesign gave every Discover card a live per-card Prowlarr
    "availability" probe (`internal/availability`, badge-only, no grab) —
    removed entirely (HTTP route, frontend calls, badge component) after
    review found it fired hundreds of concurrent live indexer queries on a
    single page load with a populated library, and the owner made this a
    firm architectural rule: Discover is TMDB/TPDB-sourced only; the
    filesystem/library is what's already "available"; Prowlarr is
    grab-time-only (searching for a release once an operator actually picks
    something to download). Don't reintroduce an availability/indexer probe
    into Discover for any reason — if a "do I already own this" signal is
    ever wanted there, source it from the tracked library (`/api/tracked`),
    never from Prowlarr. `internal/availability`'s Go package itself is kept
    (it still backs the separate, pre-existing `internal/recheck` background
    watch feature, unrelated to Discover) — only the Discover-facing HTTP
    handler was removed.
  - **Clarification, not a reversal (2026-07-14):** the Discover detail
    popup's `GET /api/modes/{mode}/discover/availability`
    (`internal/api/discover_availability.go`) does call Prowlarr, but this
    is not the badge that was removed above. The removed feature fired
    automatically, per-card, on every page load — hundreds of concurrent
    queries with a populated library. This endpoint fires once, for one
    title, only when an operator explicitly clicks a card to open its
    detail popup — the same trigger shape (a human action, not an automatic
    probe) as the pre-existing manual Search screen already had. If a
    future change makes this fire without an explicit click, or fires for
    more than the one clicked title, that's the rule being broken again —
    treat it the same way the original badge was treated.
  - **Clarification, not a reversal (2026-07-30):** the Adult Discover
    Performers/Studios live drill-down (`GET /api/modes/adult/discover/newest/entity-scenes`,
    `internal/api/adultdiscover_newest_scenes.go`) does call Prowlarr, but this
    is a narrow, bounded exception to the general "Discover never queries Prowlarr"
    rule, not a loosening of it. The handler fires exactly once, for one entity
    (a single performer or studio name), only when an operator explicitly opens
    a drill-down card — the same trigger shape (explicit human action, one entity,
    one query) as the detail popup's availability check (2026-07-14 entry above).
    The returned result set is rendered with singlePage pagination (no per-scroll
    re-query) and is honest about its sources (concurrent RSS feed best-effort results
    + single Prowlarr search call, Image/Studio/Date/Rating fields left empty to
    signal live-only limitations). This carve-out was owner-approved during
    deep-interview round 6 as part of the larger Performers/Studios redesign
    that replaced TPDB/StashDB-catalog-crawl rows with rows derived from
    already-matched RSS/Prowlarr-browse scenes. If a future change makes this
    fire without an explicit operator click, or for more than one entity per
    request, or with per-scroll re-queries, that's the rule being broken again —
    treat it the same way the original per-card badge was treated.
  - **Correction to the above (2026-07-30, same day):** "Image/Studio/Date/Rating
    fields left empty to signal live-only limitations" is now stale — this
    handler was enriched the same day, in two layers, neither of which adds a
    second Prowlarr call (the one-call-per-open constraint above is unaffected):
    (1) RSS-sourced results are joined against `adult_newest_releases` — a
    local DB lookup against releases the background RSS scan cycle has already
    identified, zero external calls; (2) any item that lookup misses (mostly
    Prowlarr-sourced, since a live search has no pre-existing pool entry) goes
    through `identify.EnrichNewestScenes` (`internal/identify/enrichnewest.go`):
    fetch the drilled-into entity's own newest-scene catalog once per
    configured box (StashDB/FansDB/TPDB), then match raw release titles
    against that catalog locally (zero further network calls per comparison).
    `Rating` still has no source and stays empty; everything else populates
    where a confident match exists, with a raw-title/no-image fallback
    otherwise. Grab safety is unaffected — the grab query is built from
    `ReleaseTitle`, never the enriched display `Title`.
  - **Trigger correction (2026-07-30, later same day):** the carve-out's
    trigger shape moved from *automatic on drill-down open* to *explicit
    Show More click* — the "fires exactly once on an explicit operator open"
    wording above is superseded on that one point (the one-call-per-trigger,
    one-entity, never-per-scroll/per-card invariants are all unchanged). The
    handler now takes a `page` param (PaginatedStrip's `load(page)` contract):
    `page=1` (the initial open) returns ONLY pool-joined items and fires ZERO
    Prowlarr calls — and now DROPS any RSS item with no pool match instead of
    showing it raw (adopting `resolveRssFeedHandler`'s established
    drop-unmatched precedent; the earlier "keep unmatched RSS items raw"
    carve-out is retired). `page>1` (an explicit Show More click) is what fires
    the single Prowlarr search, checks a new release-level cache
    (`adult_newest_scene_matches`, migration 0050) by download URL, runs
    `identify.EnrichNewestScenes` only for cache misses (resolving against the
    ONE box the pool already recorded in `entity_source`, via an exact-name
    match mirroring `PerformerImage`/`StudioImage` — the earlier multi-box
    fuzzy resolve with near-tie ambiguity decline is removed as unnecessary
    once a specific entity's drill-down is open), and drops any still-unmatched
    item. So the "one Prowlarr call per drill-down" exception is now "one
    Prowlarr call per Show More click" — still exactly one call per explicit
    operator action, still one entity, still never per-scroll/per-card. If a
    future change makes `page>1` fire more than one Prowlarr search, or makes
    `page=1` fire any, that's the rule being broken again — treat it the same
    way the original per-card badge was treated. Response envelope for both
    pages is `{items, hasMore}`; `page=1` HasMore is always true (Show More
    always offered), `page>1` always false (Prowlarr doesn't paginate further).
  - **Clarification, not a reversal (2026-08-02):** the Adult Discover pop-up
    and drill-down enrichment endpoint
    (`GET /api/modes/adult/discover/description`,
    `internal/api/adultdescription.go`) reaches TPDB REST and/or a stash-box
    GraphQL endpoint only — zero Prowlarr calls, ever, though `mode.Build`
    still allocates `sess.Prowlarr` from the connection store whenever one is
    configured (a plain struct constructor, no I/O); this handler simply never
    reads or calls it. The guarantee is behavioral (no call site), not
    structural (a nil client) — do not "verify" this rule by asserting
    `sess.Prowlarr == nil`, that assertion is false whenever Prowlarr is
    configured; `TestAdultDescriptionMakesNoProwlarrCall` is what actually
    enforces it. So this is narrower than the carve-outs above it, not a
    widening of them. It fires
    once per explicit operator action — opening a scene's `DetailPopup`, or
    opening a performer/studio drill-down — one entity, never per-card, never
    per-scroll, never on `PaginatedStrip` page advance. It is a **dedicated
    endpoint rather than an extension of the existing Discover response
    envelope (Wade's explicit choice)**: extending the envelope would ship a
    prose description on every card in every row, most of which are never
    opened, and would require a migration for the pooled newest-releases path
    (`adult_newest_releases` has no description column) — the dedicated
    endpoint needs neither.

    **Call budget: zero Prowlarr, ever; at most two upstream catalog calls per
    request, structurally** (resolution never fans out across boxes) — exactly
    **one** for a scene (`GetSceneByID`/`FindScene`, strictly following the
    scene's own `entity_source`), and **two** for a performer or studio (an
    exact-name resolve against TPDB, then a detail fetch). The entity path's
    two calls are paid **even when the drill originated from a stash-box row**
    — that is Wade's Option A decision (2026-08-02): a performer or studio's
    bio is always resolved via TPDB regardless of the entity's own catalog
    source, a narrow, documented exception to AC4's letter ("the entity's own
    primary source") for the bio path only, made specifically to get
    bio-banner coverage on all four drill-down entry points (stash-box
    studios, stash-box performers, newest-row studios, newest-row performers)
    instead of only the ones whose pool `entity_source` happened to be
    `tpdb`. Scene descriptions are unaffected by this exception and still
    strictly follow `entity_source`. **Do not write "one call" for the
    entity path** — it is two, not one, whenever it resolves at all; there is
    no zero-call case any more (an earlier draft of this feature's plan
    priced one, and corrected it — see `.omc/plans/autopilot-impl-adult-popup-enrichment.md`
    §2.2). The response's `Source` field echoes the box actually consulted,
    so a stash-box-sourced entity's response reports `source: "tpdb"` — that
    is the intended honest-provenance property, not a bug.

    **No tags field on this endpoint, and none is coming.** Live schema
    introspection (2026-08-02) — complete field enumeration against TPDB
    REST's OpenAPI schema and both stash-box GraphQL endpoints, not an
    inference — found no performer/studio tags field in any of the three
    catalogs. The response DTO ships no `Tags` field for this reason. **Do
    not add a tags pill row for performers or studios** — there is nothing in
    any configured catalog to bind it to.

    If a future change makes this endpoint fire without an explicit click,
    for more than one entity per request, or ever construct a Prowlarr
    client, that's the rule being broken again — treat it the same way the
    original per-card badge was treated.

    **CORRECTED 2026-08-02 (later, same day) — there are TWO drill-down entry
    points now, not four (see
    `.omc/plans/autopilot-impl-adult-rows-config-unification.md` §8,
    `docs/ROADMAP.md`'s multi-idea-session item 1, and `CHANGELOG.md`).**
    Appended rather than edited in place, per this file's own convention. The
    superseded clause, quoted verbatim so a future session can find it:

    > *"made specifically to get bio-banner coverage on all four drill-down
    > entry points (stash-box studios, stash-box performers, newest-row
    > studios, newest-row performers) instead of only the ones whose pool
    > `entity_source` happened to be `tpdb`"*

    Adult Discover's optional StashDB/FansDB structural rows were deleted
    outright, along with their twelve backend routes and five frontend
    fetchers, when Settings and Discover were unified onto one row order.
    Nothing in the app can raise a stash-box studio or stash-box performer
    drill any more. **Corrected enumeration: two entry points — newest-row
    studios and newest-row performers.**

    **The Option A decision itself is UNCHANGED and is still the right one —
    do not read this as grounds to revert it.** A performer's or studio's bio
    is still always resolved via TPDB regardless of the entity's own catalog
    source, and it still has to be: a newest-row entity's pool
    `entity_source` can perfectly well be `stashdb`/`fansdb` (that is exactly
    what `sourceLabel` still renders provenance for), and neither stash box
    exposes a performer bio or studio description field at all. Reverting to
    strict-AC4 resolution would blank the banner on those entities — the same
    failure Option A was chosen to avoid, just reached from the surviving
    half of the surface. **The call budget is completely unchanged**: zero
    Prowlarr, one upstream call for a scene, two for an entity.
  - **Library sidebar tab added (2026-08-01)** — a new `frontend/src/screens/Library.tsx`
    (own sidebar entry, `/library` route) is now the tracked-catalog browsing
    screen for Movies/Series (title search, genre filter, added-date sort via
    the newly-exposed `createdAt` field). `Tag.tsx` narrowed in the same change
    to pure tag-CRUD — its browsing grid was deleted, `PosterCard`/`DetailPanel`
    moved to Library verbatim. **Tag deliberately still has its own direct
    sidebar entry, not drill-in-only from Library.** This looks like it
    contradicts the spec's own AC5 ("Tag no longer a standalone browsing entry
    point") but is the resolution to a genuine internal contradiction between
    that AC and AC7 ("Adult's table view completely unchanged"): Movies/Series
    got a real separate Library screen to browse into, but Adult never did —
    Adult's tag-CRUD table has always lived inside `Tag.tsx` itself (Adult is
    explicitly out of scope for Library, per Non-Goal 1), so `Tag.tsx` is the
    *only* screen that reaches it. Removing Tag from the sidebar would make
    Adult's tag CRUD unreachable outright. Wade confirmed keeping Tag in the
    sidebar for all three modes rather than either breaking Adult access or
    building Adult a redundant second table screen the spec never asked for.
    **Do not "fix" this by removing Tag's sidebar entry** without first giving
    Adult an equivalent entry point — see
    `.omc/plans/autopilot-impl-library-sidebar-tab.md` §2 and
    `docs/ROADMAP.md` item 12 for the full reasoning.
  - **Queue sidebar grouping added (2026-08-01)** — the three former top-level
    routes `/downloads`, `/grabs`, `/requests` (each with its own sidebar entry)
    are now client-side tabs under ONE **Queue** entry at `/queue`, via a new
    thin wrapper `frontend/src/screens/Queue.tsx`. Sidebar 10 entries → 8;
    `APP_ROUTES` 11 → 9. Removal is clean — no redirects, no route aliases; the
    `<Route path="*">` NotFound fallback handles stale bookmarks, and
    `IconGrabs`/`IconRequests` were deleted outright (`IconDownloads` renamed to
    `IconQueue`) per this repo's "no dead code left behind" convention.
    `Downloads.tsx`/`Requests.tsx`/`Grabs.tsx` are internally untouched and
    their own suites pass unmodified. **`Queue.tsx` is deliberately a
    near-line-for-line mirror of `Organize.tsx`, not a shared abstraction
    extracted from the two** — same `createPersistedString` persistence
    (`sakms.queue.tab`), same sanitize-for-display-without-rewriting fallback,
    same single shadowing `ScreenTabsContext.Provider value={undefined}`
    wrapping all three children. That duplication is intended per the
    no-premature-abstraction convention; don't "fix" it by hoisting a shared
    helper for two callers. The Provider is load-bearing for exactly one child:
    the embedded `Grabs` renders `ModeTabs`, which would otherwise clobber
    Queue's own tab bar in the shell's single tab slot (`Downloads`/`Requests`
    render no `ModeTabs`/`ScreenTabs` at all). **Tab order is
    Downloads → Requests → Grabs, deliberately NOT the old sidebar order** —
    and `docs/ROADMAP.md` item 4's 2026-07-31 amendment swapping Grabs for
    Calendar as tab 3 is Phase 4 scope, deferred on purpose per
    `.omc/plans/roadmap-implementation-sequencing.md:104-106`. See
    `.omc/plans/autopilot-impl-queue-navigation-grouping.md`.
    - **CORRECTED 2026-08-02 — the deferred swap has been PERFORMED. Tab 3 is
      Calendar, and `Grabs.tsx` is deleted outright (see
      `.omc/plans/autopilot-impl-calendar-grabs-requests.md` §1.1,
      `docs/ROADMAP.md` items 4 and 8, and `CHANGELOG.md`).** Stacked on the
      note above rather than rewriting it, per this file's append-and-correct
      convention. **Three** sentences in it are now false, quoted verbatim so a
      future session can find them:
      1. *"`Downloads.tsx`/`Requests.tsx`/`Grabs.tsx` are internally untouched
         and their own suites pass unmodified."*
      2. *"The Provider is load-bearing for exactly one child: the embedded
         `Grabs` renders `ModeTabs`, which would otherwise clobber Queue's own
         tab bar in the shell's single tab slot"* — quoted up to, but not
         including, its trailing parenthetical, which is still true.
      3. *"**Tab order is Downloads → Requests → Grabs, deliberately NOT the
         old sidebar order** — and `docs/ROADMAP.md` item 4's 2026-07-31
         amendment swapping Grabs for Calendar as tab 3 is Phase 4 scope,
         deferred on purpose per
         `.omc/plans/roadmap-implementation-sequencing.md:104-106`."*

      **Corrected: tab order is Downloads → Requests → Calendar**, still
      deliberately NOT the old sidebar order.
      `frontend/src/screens/Grabs.tsx` **and** `Grabs.test.tsx` were **DELETED**
      — not kept alongside Calendar — per this repo's "no dead code left
      behind" convention. Nothing was lost: Calendar's History view carries the
      read-only completed/imported grab log forward, still with no bulk actions
      and no mutate-many affordances. `Downloads.tsx` and `Requests.tsx` are
      still there (both were edited by this feature for other reasons).
      **A stored `sakms.queue.tab` of `"grabs"` lands the operator on
      Downloads through the existing sanitizing fallback** — the deliberate,
      tested outcome, and deliberately NOT a `"grabs"` → `"calendar"` storage
      rewrite, which would be the only storage-migration path in the app.

      **Corrected: the Provider still exists and is still load-bearing for
      exactly one child — but be precise about WHICH component registers,
      because it is not the obvious one.** It is **Calendar's `History`
      child**, not `Calendar` itself: `calendar/index.tsx`'s own
      History/Upcoming switch is a plain `ScreenTabBar` that draws inline and
      registers nothing, while `calendar/History.tsx` renders the real
      three-mode `ModeTabs`. History is Calendar's default view, so the
      collision is live from the moment the Calendar tab is opened. Downloads
      and Requests still render neither `ModeTabs` nor `ScreenTabs` at all.

      **Unchanged and still accurate:** "Sidebar 10 entries → 8; `APP_ROUTES`
      11 → 9." Calendar never had a route of its own, so it added nothing —
      the three routes collapsed into `/queue` are still `/downloads`,
      `/grabs` and `/requests`, which is what `routing.test.ts` asserts are
      dead. The `Organize.tsx`-mirror / no-premature-abstraction stance is
      unchanged too. `Queue.tsx`'s own file header is the authoritative
      description of all of this.

- **Mainstream Discover — Seerr-parity expansion (2026-07-14)**: supersedes
  this section's earlier "paginated Trending/Popular rows" description —
  rows are now horizontal arrow-navigated carousels
  (`frontend/src/components/Carousel.tsx`, bounds-aware disable, lazy-load
  near the trailing edge), and Mainstream gained fixed **Upcoming
  Movies/Upcoming Shows** rows alongside Trending/Popular (still TMDB-only,
  the no-Prowlarr rule above still holds). Two new features layer on top:
  - **Admin-defined custom Discover sliders** (Seerr's CreateSlider/
    DiscoverSliderEdit equivalent) — `internal/discoversliders` (SQLite-backed
    CRUD + reorder, migration `0023`), `internal/api/discover_sliders.go`
    (CRUD + resolve-to-TMDB-items routes), a Settings → **Sliders** tab
    (`frontend/src/screens/SliderAdmin.tsx`) to create/edit/reorder/delete.
    Filter types: `genre|keyword|studio|network|upcoming|trending|popular`;
    `filter_value` required for the first four, forbidden for the fixed
    three (enforced both by the Store and the editor's picker UI, not a
    freeform text field). Target `movie|tv|mixed` — a mixed slider's items
    are movie-then-tv concatenated server-side; each card still grabs
    through its own mode's path (never routed through the wrong one).
  - **Trakt.tv watchlist integration** — `internal/trakt` (OAuth device-code
    flow, encrypted credential/token storage via `internal/secrets`,
    migration `0022`), a Settings → Connections "Trakt (Watchlist)" card
    (client_id/secret, three-state secret semantics, Connect button →
    device code + verification URL → polls to Connected), and a Discover
    "Trakt Watchlist" row that only renders once linked. **Config-driven,
    not hardcoded** — client_id/secret come from a Trakt application the
    operator registers themselves at trakt.tv/oauth/applications, same
    externally-owned-app pattern as any OAuth integration.
  - **Auto-grab's "service isn't configured" failures now get an in-dialog
    setup prompt** instead of a bare error (or, before a same-day fix,
    getting permanently stuck — see below): `GrabError` in Discover.tsx
    detects a missing Prowlarr/qBittorrent/NZBGet from the backend's fixed
    error strings and renders a URL(+username/password or +API key) form
    reusing the same `upsertConnection`/`buildConnectionUpsertBody` Settings'
    own form calls, plus a LAN-discovery hint (`fetchNetscanKnown`,
    confirm-first — never silently auto-fills, same convention as Settings'
    `ConnectionRow`). Shared by both Mainstream and Adult (one `GrabDialog`
    component, Prowlarr/qBittorrent/NZBGet are global connections, not
    per-mode) — no separate Adult-specific work needed.
  - **Real bug found and fixed during this work's own verification pass**:
    `GrabDialog`'s error and success branches were sibling `<Show>` blocks,
    so the success branch's `result()` read still executed even while
    `result.error` was set — Solid resources re-throw on read after the
    fetcher errors (by design, for `ErrorBoundary` integration), and that
    uncaught throw happened mid-render, leaving the dialog stuck on
    "Searching and scoring releases…" forever. Fixed by nesting the success
    Show inside `when={!result.error}`. Independently architect-reviewed
    (fresh context, own build/test run): PASS, no blocking findings.
  - See `docs/ROADMAP.md`'s new "Unified downloader" backlog entry — this
    work's two-separate-external-apps friction (Prowlarr for search, then
    qBittorrent *or* NZBGet for the actual download) is the concrete driver
    for that idea, not yet started.
  - **Season/episode picker is a poster grid (2026-08-02)** — the two
    free-text `S`/`E` number inputs are replaced by a two-level grid: a season
    grid (proxied season poster, name, episode count, air year) that drills
    into an episode grid (proxied still, number, name, air date, runtime),
    with a "Whole season" tile at the head of the episode level preserving
    today's `episode: 0` / `SeasonSpecified: true` semantic that protects
    Season 0 / Specials. Season posters and episode stills reuse the existing
    `/api/images/proxy` path — **zero backend image work**, and the
    never-hot-link rule is unaffected. Data arrives through the existing
    `GET /api/modes/{mode}/discover/detail`, extended with a response-scoping
    `sections=seasons` param rather than a new endpoint; the handler fans out
    per-season episode fetches server-side (bounded concurrency, soft-failing
    per season) so the browser makes one request, not N. Load-bearing details
    a future session should not quietly undo:
    - **The picker moved OUT of `Mainstream.tsx` into its own
      `frontend/src/screens/discover/SeasonEpisodePicker.tsx`.** Anything that
      used to import it from `Mainstream` no longer can — notably the
      sequenced-after `discover-card-cleanup` work, which must re-verify
      against the new location, not the old export.
    - **There are SIX mount points, not the three the spec names** — the
      Discover category rows' card, `DetailPopup`'s gating step, bulk-select,
      the "In your library" row's card, the Trakt watchlist row, and the
      cards in `DetailPopup`'s "More like this" rail plus `CalendarView`.
      Every one of those except `DetailPopup`'s inline gating step and
      bulk-select reaches the picker *through* `GrabButton`, which is why one
      change covers them — but do not plan an edit here believing the surface
      is three files. (`Library.tsx`'s own `PosterCard` is a
      same-named, unrelated component and is NOT affected.)
    - **One component, but deliberately different containers.** A grid does
      not fit the 180px card column, so card-triggered sites open the picker
      in the shared `Modal`, while `DetailPopup` renders it **inline** in the
      modal body it already has. **Never a nested modal** — both modals are
      `fixed inset-0 z-50` with a backdrop click that would close both. That
      is also why the Grab button is suppressed on **Series** cards in
      `DetailPopup`'s recommendations rail: clicking such a card already
      re-targets the popup, from which the full inline picker is reachable.
      **Do NOT suppress Movies there** — a movie's grab opens no picker and
      nests nothing, and blanket suppression would remove a shipped
      affordance for a problem it does not have.
      - **SUPERSEDED 2026-08-02 (later, same day) — the `insideModal`
        suppression described in the two sentences above no longer exists, and
        neither does the Grab button it suppressed.** The Discover card cleanup
        removed `PosterCard`'s inline Grab button outright, so the
        recommendations rail has nothing left that can open a nested modal;
        `insideModal` was deleted from `PosterCard` and from its `DetailPopup`
        call site as newly-unreachable code. The **"never a nested modal"
        principle itself still stands and is still why `DetailPopup` renders
        the picker inline** — only the rail-specific carve-out is retired.
        Full lifecycle, plus the invariant whose test now protects the deleted
        guard, is under **Established engineering conventions →
        Staged-for-approval**'s CORRECTED 2026-08-02 (later, same day) note.
    - **The degraded fallback is required, and the spec never asked for it.**
      If the fetch errors or returns zero seasons, the component renders the
      **old two free-text `S`/`E` inputs** plus a one-line error. Without it,
      a TMDB outage or an unconfigured TMDB connection would make every Series
      grab in the app impossible, at every mount point simultaneously. It will
      look like removable dead code to a future session; it is not.
    - **Grid, not carousel — deliberate.** Every surrounding Discover row is a
      `Carousel`; the picker is a CSS grid at both levels. Scoped to the
      picker only, recorded so nobody "fixes" the inconsistency.
    - Episode-level **bulk**-select is new and is bounded by its own rules —
      see the CORRECTED 2026-08-02 note under **Established engineering
      conventions → Staged-for-approval**. The per-card Grab button remains
      strictly single-item.
    - **AMENDED 2026-08-09 — a SEVENTH surface exists, and it is a separate
      component rather than a mode of this one:
      `frontend/src/screens/discover/SeasonEpisodeAccordion.tsx`** (spec
      `.omc/specs/deep-interview-rename-episode-picker-collapsible.md`, plan
      `.omc/plans/autopilot-impl-rename-episode-picker-collapsible.md`).
      Rename's Re-pick / Move takeover (`SearchTakeover.tsx`'s Series step 2)
      mounts the accordion instead of the grid: seasons are collapsible rows,
      multi-open, with the season matching the proposal's current slot expanded
      on mount, and episodes render as compact text rows with **no stills**.
      **This does NOT change the enumeration above — there are still SIX
      `SeasonEpisodePicker` mount points, and all six keep the poster grid,
      unchanged.** SearchTakeover was a seventh mount point of the picker and is
      now the sole mount point of the accordion; the picker is not imported
      there any more.

      **Duplicated, not parameterised — deliberately, and the interview
      originally decided the other way.** Round 3 chose "prop flag on the
      existing component"; that was reversed before implementation on
      discovering the existing Non-Goal in
      `.omc/specs/deep-interview-repick-move-fullpage-takeover.md`: *"No change
      to `SeasonEpisodePicker`'s own internal behavior, its 5 existing mount
      points, its degraded fallback, or its 'Whole season' (`episode: 0`)
      semantics — it is consumed as-is, not modified."* So `seasonLabel`, the
      degraded `FreeTextPicker` (both notice strings, both aria-labels, the
      season-required/episode-optional asymmetry) and the
      `fetchTitleDetail(…, "seasons")` self-fetch are copied into the new file,
      the same precedent as `SearchTakeover.tsx`'s own `GRID_CLASS`/`TILE_CLASS`
      duplication (D-6). `git diff` on `SeasonEpisodePicker.tsx` is empty.
      **The duplicated fallback is the one thing that can silently drift** —
      `SearchTakeover.test.tsx`'s "degraded free-text fallback maps a blank
      Episode through the same D-1 rule" test is what pins it.

      **The `currentSlot` auto-expand does NOT reopen D-2.** That deviation
      (`SearchTakeover.tsx`'s `currentSlot` prop doc) says the current slot is
      read-only context rather than a pre-fill, because the picker has no
      pre-selection concept and adding one would modify a protected file.
      Expanding a row selects nothing and stages no commit, so the constraint
      holds; only the prop's "DISPLAY-ONLY" wording needed amending, which it
      got in place.

    - **AMENDED 2026-08-09 (later, same day) — `SearchTakeover`'s Series-mode
      search queries BOTH TMDB catalogs and merges them, and the ONE thing worth
      recording here is why that merge is not in the backend** (spec
      `.omc/specs/deep-interview-series-search-includes-movies.md`, plan
      `.omc/plans/autopilot-impl-series-search-includes-movies.md`). A Series
      search now calls `tmdb-search` twice (movies + series, `Promise.all`,
      movies-first per `Mainstream.tsx:826-827`), badges each row "Movie"/"Series",
      and does not de-duplicate. Motivating case: a short film TMDB files under
      movies that the operator tracks in their Series library.

      **`GET /api/modes/{mode}/tmdb-search` has ZERO diff, and blending there is
      the trap.** It is the obvious place to fix this and it is wrong: that
      endpoint is SHARED with Discover's Mainstream search bar, which already
      does its own client-side dual-call merge — a series-mode response that
      included movies would make Mainstream render every movie TWICE. That
      reasoning is not derivable from `SearchTakeover.tsx`, which otherwise has
      no reason to know Mainstream exists, so the full WHY lives as a comment on
      the dual-call block itself. `internal/api/proposals.go` and
      `internal/rename` are likewise untouched: `repickProposalHandler` never
      verifies a `tmdbId` is a real TV show, and `ApplyLibrarySeries` already
      degrades to a bare `SxxExx` when episode-title lookup fails — the same path
      an anthology proposal's synthetic id already takes.

      **`useCatalogItem` still branches on `props.searchMode`, never on a hit's
      origin** — a movie-origin pick in Series mode drills to step 2 like any
      other, and the accordion's existing zero-seasons free-text fallback is
      reused verbatim with its copy unchanged (deliberate, spec Round 5). Known
      accepted tradeoff: the `Promise.all` is NOT caught, so a movies-catalog
      failure now fails a Series search. Mainstream catches only because it feeds
      `setSetupError`; there is no equivalent here, and a rejection is what
      populates `results.error`, which the render already handles.

    - **CORRECTED 2026-08-09 (same day, Phase-4 review) — the anthology-precedent
      sentence directly above is FALSE, and the actual risk it was papering over
      is a real one: possible silent overwrite of a tracked show's library row.**
      The superseded sentence, quoted verbatim so a future session can find it:
      *"`ApplyLibrarySeries` already degrades to a bare `SxxExx` when episode-title
      lookup fails — the same path an anthology proposal's synthetic id already
      takes."* That equivalence does not hold. Anthology synthetic ids are always
      **negative** (`anthologyTMDBID`, `series_tvdb_episode_match.go:131-137`,
      an FNV hash negated), and `rename.go:1900` gates the TMDB episode-title
      lookup on `p.TMDBID > 0` — so a synthetic id **provably never reaches**
      `SeasonDetails`. A movie-origin pick's id is **positive and real**. TMDB's
      movie and TV catalogs are independently numbered and CAN share an integer
      (TV ids run roughly 1 to the high-200,000s; plenty of older movies —
      exactly the "old short film" motivating case — fall in that range too).

      **What actually happens on a collision, worse than the superseded sentence
      implied:** `SeasonDetails(ctx, movieId, season)` can SUCCEED against an
      unrelated TV show sharing that id — the accordion shows that show's
      season/episode names as if they belonged to the picked title, and on Apply,
      `UpsertSeries`'s `ON CONFLICT(tmdb_id) DO UPDATE` (`library_series.go:88-100`,
      `UNIQUE(tmdb_id)`) **overwrites that real tracked show's title/year/
      root_folder_path/genres/cast** with the short film's — a direct hit on
      this file's own "no drift, no corruption" mission bar, not a cosmetic
      mislabel.

      **A structural fix exists and was NOT taken — Wade's explicit decision,
      recorded so a future session does not "helpfully" add it without
      re-asking.** Negating a movie-origin id before commit (mirroring
      `anthologyTMDBID`'s own negative-synthetic-id trick) would make
      `p.TMDBID > 0` false and structurally skip the hazard, no backend change
      required. Offered during Phase-4 review; Wade chose the warning-only
      mitigation instead — the `origin: "movies" | "series"` field on
      `PickedShow` and the `ErrorText` advisory in step 2 (*"the season list
      below ... may belong to an unrelated show"*) are what shipped. **This is a
      known, accepted residual risk, not an oversight**: the corruption is
      possible only on an id collision AND only when the operator ignores the
      warning and proceeds past a wrong-looking episode name. If this is ever
      revisited, the id-negation approach is the one already priced out as
      cheap and backend-free — re-derive it from this note rather than
      re-investigating from scratch.
