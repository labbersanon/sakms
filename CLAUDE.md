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
    batching — every single row's own Apply/Give back/Re-pick/Dismiss button
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
