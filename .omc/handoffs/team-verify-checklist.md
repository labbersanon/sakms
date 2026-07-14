# team-verify checklist: mainstream-discover-seerr

Prep doc for the lead's team-verify pass once #5/#6/#7/#8/#9 land on
`mainstream-discover-seerr`. Written and verified (steps 1-3 below) against
the current `team/mainstream-discover-seerr/worker-4` branch state
(task #4 only — no carousel/admin-editor/Trakt UI exists yet in this
worktree). Re-run steps 1-3 once merged to confirm nothing regressed, then
do steps 4a-4d against the real, merged UI.

Confirmed in this session: `go build`, `pnpm build`, and a real boot +
browser walkthrough (setup wizard -> login -> Discover shell -> Settings
tabs) all worked cleanly with zero console errors, on Node 22.22.2 / pnpm
9.15.9 / go1.26.5, matching the pinned toolchain versions.

## 1. Stand up the stack (single-port, production-shaped — recommended for verify)

This mirrors how the app actually ships (one Go binary serving the compiled
SPA), so it's the path least likely to hide a runtime-broken frontend behind
dev-only scaffolding. Run from the root of whichever worktree has the merged
code (or the merge target branch checked out):

```sh
cd frontend
pnpm install --frozen-lockfile   # first time / after a lockfile change
pnpm build                       # tsc --noEmit && vite build -> ../internal/web/static
cd ..
go build -o /tmp/sakms-verify ./cmd/sakms
```

`pnpm build` MUST be run before `go build`/`go run` — the frontend's output
directory is `internal/web/static`, which is gitignored and entirely
generated; `//go:embed static` fails cleanly (`pattern static: no matching
files found`) until it exists. If `pnpm build` fails on a TS error, that's
already a real signal — don't skip past it with a stale `static/` dir from a
previous build.

Run against a scratch data dir so you never touch a real deployment's DB:

```sh
rm -rf /tmp/sakms-verify-data && mkdir -p /tmp/sakms-verify-data
SAKMS_ADDR=:8099 SAKMS_DATA_DIR=/tmp/sakms-verify-data /tmp/sakms-verify
```

Confirm it's alive before opening a browser:

```sh
curl -s http://localhost:8099/healthz                # -> ok
curl -s http://localhost:8099/api/auth/status         # -> {"configured":false,"authenticated":false,"mode":"password"}
```

The first log line that matters: `API key generated (shown once, store it
now): <KEY>` — copy it if you'll need break-glass/`X-Api-Key` testing later
(see `docs/break-glass-recovery.md`); otherwise ignore it.

### Alternative: HMR dev loop (only if actively iterating on a fix mid-verify)

Two processes instead of one — more moving parts, so prefer the single-port
path above unless you're actually editing frontend code between checks:

```sh
# terminal 1
go run ./cmd/sakms                      # :8080, needs internal/web/static to exist at all (even a stale build) — see note below

# terminal 2
cd frontend && pnpm dev                 # prints its own localhost URL (Vite default 5173)
```

Vite's dev server proxies `/api/*` and `/healthz` to `SAKMS_DEV_BACKEND`
(default `http://localhost:8080`) — open the Vite URL, not the Go port,
to get HMR. Note `go run` still needs `internal/web/static/` to physically
exist (even stale/empty-ish) purely so the `//go:embed` compiles; it never
serves those files to you in this mode since you're hitting Vite's port.

## 2. First-boot login walkthrough (verified working this session)

A brand-new `SAKMS_DATA_DIR` always lands on **"Create your SAK login"** —
no unauthenticated path exists to anything else. Steps, confirmed via a real
CDP browser session against the build above:

1. Open `http://localhost:8099/` (or the Vite URL in dev mode). You'll see
   the "Create your SAK login" card — Authentication mode defaults to
   **Password**, with Username/Password/Confirm fields and two buttons:
   **Create login** and **Skip — no authentication**.
2. Fill username/password/confirm, click **Create login**. This POSTs
   `/api/auth/setup` and logs you in immediately — no separate login step
   after setup.
3. You land directly in the app shell: left sidebar (Discover / Grabs /
   Rename / Purge / Dedup / Tag / Settings, collapsible), "Log out" top
   right, and the Discover screen with a Mainstream/Adult tab split.
4. **Expected, not a bug**: if no TMDB connection is configured yet, a
   "Set up TMDB" modal appears over Discover (it re-renders on nearly every
   route change/interaction until dismissed by actually saving a key or
   clicking Close — this is existing behavior, unrelated to this team's
   work). For carousel verification (4a below) you need a **real TMDB API
   key** saved via Settings -> Connections -> tmdb, otherwise every row
   shows "Nothing here yet." and there's nothing to scroll/paginate.
5. Settings' tab bar (Connections / Auth / AI / Library / Advanced) renders
   and switches correctly; Connections lists all current service rows
   (prowlarr, qbittorrent, nzbget, tmdb, ollama, openai, gemini, anthropic,
   stashdb, fansdb, tpdb, brave, stash, jellyfin — **and should now also show
   a `trakt` row once task #9's handlers + task #8's Settings UI land**; if
   it doesn't, that's a real merge gap to flag).
6. Reload the page — session cookie keeps you logged in without re-entering
   credentials (confirms the auth-boot cookie path, not just the in-memory
   SPA state).

For OIDC or break-glass/`X-Api-Key` recovery-path testing specifically, this
session didn't have a live IdP to test against — follow
`docs/break-glass-recovery.md` verbatim instead; it's an operational runbook
written to be followed cold and doesn't need a live incident to dry-run
(Case A: `configured:false` -> public setup wizard; Case B: `configured:true`
but locked out -> the "Trouble logging in?" panel on the login screen, or the
`X-Api-Key` header against the protected recovery routes it documents).

## 3. Pre-flight for the carousel/admin/Trakt checks below

Before 4a-4d, from Settings -> Connections:

- Save a real **tmdb** API key (get one free at
  themoviedb.org/settings/api) — without this, Discover has no data and
  every row check in 4a is a false negative, not a real pass/fail signal.
- Optionally save **prowlarr** + **qbittorrent** or **nzbget** if you want
  4d's one-click grab to actually complete end-to-end rather than just
  render the grab dialog/candidate list.
- For 4c, no live Trakt app is required to check the OAuth *UI* renders and
  polls correctly against a mocked/placeholder device-code response (per
  task #2/#8's scope) — only a real end-to-end watchlist pull needs
  Wade's real Trakt credentials, which is explicitly out of scope for this
  team-verify pass per the team plan's own risk note.

## 4. Feature verification script (run once #5-#9 are merged)

Use CDP (`navigate` / `computer` / `read_page` / `read_console_messages`)
against the running instance from step 1, not just a visual skim — check
`read_console_messages` with `onlyErrors: true` after every screen load in
every step below; a clean console is part of the pass bar, not optional.

### 4a. New Discover carousel rows (genre/studio/network/upcoming, plus existing trending/popular)

1. Navigate to `/discover`, Mainstream tab. Confirm each row (Trending
   Movies/Shows, Popular Movies/Shows, plus whatever new
   Genre/Studio/Network/Upcoming rows #6 adds) renders a horizontal
   carousel — cards laid out in a single scrolling row, not a wrapping
   grid.
2. Use `read_page` (filter: interactive) to find the row's left/right
   arrow buttons. At the natural left edge (row just loaded, scrolled to
   start): confirm the left arrow is disabled (`disabled` attribute or
   equivalent visual/aria state) and the right arrow is enabled (assuming
   more than one page of cards).
3. Click the right arrow repeatedly (or use `scroll` on the row's
   container) until reaching the end. Confirm the right arrow becomes
   disabled at the last page and does not wrap/overscroll past it, and
   the left arrow re-enables.
4. Click left arrow back to start; confirm it returns cleanly to the
   disabled-left state, no stuck/double-disabled state, no console errors.
5. Repeat for at least one Genre row, one Studio/Network row, and one
   Upcoming row (each is a materially different backend query per task #1
   — a bug isolated to one filter type is a real, reportable finding, not
   noise).
6. Do this for at least one row with a custom admin-created slider too
   (see 4b) — a custom slider's row must scroll/paginate identically to
   the built-in ones, since it's rendered through the same carousel
   component.

### 4b. Admin slider editor (create/edit/reorder/delete)

Backend contract this UI is built on (`internal/discoversliders`, this
worktree's task #4, already merged): `Slider{ID, Title, FilterType,
FilterValue, Target, SortOrder, Enabled, CreatedAt, UpdatedAt}`,
`FilterType` one of `genre|keyword|studio|network|upcoming|trending|popular`,
`Target` one of `movie|tv|mixed`. Validation to watch for in the UI, since
it mirrors real backend rejections you should see surfaced as user-facing
errors, not silent failures or raw 500s:

- `FilterValue` is **required** for genre/keyword/studio/network and
  **must be blank** for upcoming/trending/popular — the create/edit form
  should enforce or at least surface this, not let you submit an invalid
  combination and get a generic error.
- Reorder is a single "here's the full new order" action (drag-to-reorder
  or up/down controls covering every existing slider at once) — there is
  no per-item bulk reorder-a-subset affordance, matching the project's
  single-explicit-action convention.
- Create/Delete are one-item-at-a-time — no multi-select bulk create or
  delete anywhere in this editor.

Steps:

1. From wherever task #7 surfaces the editor (likely Settings or a
   Discover-adjacent admin panel — check both), create a new slider: pick
   a filter type requiring a value (e.g. Genre), supply a value, a target,
   a title, save.
2. Navigate to `/discover` and confirm the new slider now renders as its
   own row, in the position its sort_order implies (should default to
   last).
3. Edit the slider (change title and/or target), save, confirm the row's
   heading and/or content updates on Discover without a manual page
   reload being required (or if it does require a nav/refresh, confirm
   that at least works — note which behavior you observed).
4. Reorder it (move it to the top), confirm Discover's row order actually
   changes to match.
5. Delete it, confirm the row disappears from Discover.
6. Try to create one with an invalid filter/value combination (e.g.
   "genre" with no value, or "trending" with a value) and confirm the UI
   rejects it with a legible message rather than a raw network error.

### 4c. Trakt Settings OAuth flow

1. Settings -> Connections (or wherever task #8 places it) -> find the
   `trakt` row/section. Confirm it's config-driven (client_id/secret input
   fields), not hardcoded — there should be no working Trakt connection
   out of the box on a fresh instance.
2. Enter placeholder/test client_id+secret (real values only if Wade has
   supplied live test credentials by verify time — check with the lead
   first per the team plan's guardrail on this), trigger the OAuth
   device-code flow.
3. Confirm the UI renders both the **device code** and the **verification
   URL** clearly (this is the Seerr/device-flow pattern — a user needs to
   copy the code and visit the URL on another device/tab).
4. Confirm the UI polls token status (visible loading/pending state,
   not a one-shot check) and would transition to a connected state on
   success — if no live Trakt app exists yet, this can only be confirmed
   against whatever mocked/placeholder response task #2/#8 built in; note
   explicitly in your verify report whether this was checked against real
   Trakt or mocked state, since that's a materially different confidence
   level.
5. If a working Trakt connection exists (real or mocked through to
   completion), confirm a Watchlist row appears on Discover.

### 4d. One-click grab still works on cards in new rows

1. Pick a card in one of the new rows (Genre/Studio/Network/Upcoming, or a
   custom slider row) and click its **Grab** affordance.
2. Confirm the same `GrabDialog` behavior as existing rows: it fires
   auto-grab on open, and either shows a success line (top qualifier
   grabbed automatically) or falls back to a manual candidate list with
   "Grab this" per result — per `frontend/src/screens/Discover.tsx`'s
   existing one-click auto-grab design (bitrate-quality-floor scorer,
   `internal/autograb`, distinct from the manual Search-view scorer).
3. Confirm the mode routing is correct for a **mixed** row specifically
   (a row whose Target is `mixed`, combining movies+series) — each card
   must grab through its own mode's path (movie vs series), not silently
   route a series through the movie grab endpoint. This is exactly the
   failure mode `Discover.tsx`'s own comments call out as a real risk for
   any combined row, so a custom "mixed" slider is the one new case most
   likely to regress it.
4. If qBittorrent/NZBGet + Prowlarr are configured (step 3 above), confirm
   the grab actually completes (check `/grabs` afterward); if not
   configured, confirming the dialog/candidate-list UI renders correctly
   without a hard error is still a meaningful partial check — note which
   you did.

## Notes for whoever runs this

- Kill the scratch server and delete its data dir when done
  (`SAKMS_DATA_DIR` from step 1) — don't leave a stray process bound to
  the verify port.
- This checklist was written and steps 1-2 dry-run BEFORE #5-#9 merged
  (only this branch's own `internal/discoversliders` backend exists here)
  — treat 4a-4d as a script to execute, not something already confirmed
  passing. If the merged app's actual DOM/component structure differs
  materially from what's described (e.g. the editor lives somewhere
  unexpected, or row markup doesn't match), that's fine — the intent
  (what to click, what state to check) matters more than exact selectors,
  which weren't available to verify against yet.
