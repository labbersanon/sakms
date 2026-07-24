// Discover — the Seerr-inspired browse landing, MUTATING (Stage 2). The
// Mainstream tab is a search bar over four stacked, independently-paginated TMDB
// category rows (Trending/Popular × Movies/Series) plus a paginated "In your
// library" row of what's already tracked; the Adult tab is a TPDB scene browse.
// Discovery is sourced purely from TMDB/TPDB (and the local library) — Prowlarr
// is never consulted here; it's only involved later, when a grab actually
// retrieves a title. Poster/scene art renders ONLY through the image proxy
// (src/api/discover.ts's proxyImage/tmdbPoster), never hot-linked from
// TMDB/TPDB (plan Decision #7).
//
// One-click auto-grab (plan Decision #5): a card's "Grab" triggers the backend
// auto-grab — search + bitrate-quality-floor scoring — which either grabs the
// top qualifier outright or returns a ranked manual pick list when nothing
// clears the floor (never a silent failure, never "grab the least-bad option").
// Per-mode nuance is respected exactly:
//   - Movies: one click grabs directly (the clean 1-poster=1-title case).
//   - Series: one click opens a season/episode picker FIRST — "one click per
//     season/episode selection", since no release exists to score until a
//     specific episode/pack is chosen. Season-0/Specials is preserved:
//     submitting the picker always sets seasonSpecified=true (a bare season
//     number can't tell "Season 0 picked" from "no season picked").
//   - Adult: one click grabs a scene, sourcing the bitrate scorer's runtime
//     from the scene's TPDB durationSeconds.
//
// AMENDED 2026-07-24 (Discover-depth Pass 2) — bounded bulk-grab exception, a
// KNOWING, user-approved narrowing of the original "no bulk actions anywhere"
// convention below. That original text read: "No bulk actions anywhere
// (Guardrail #3): every affordance grabs exactly one title/episode/scene per
// click." It is amended, not deleted, so the reasoning stays visible:
//   - There is now ONE bulk surface on Discover: an OPT-IN "Select" mode
//     (mutually exclusive with Edit). In it, the operator checks specific,
//     personally-chosen cards, then "Grab all" runs a SINGLE capped (≤20
//     flattened items), SEQUENTIAL, server-side batch call (/api/autograb-batch).
//     It is not a queue-wide "grab everything", not a scheduler, not cross-mode
//     auto-approval — every item is one the operator explicitly picked.
//   - EVERY OTHER Discover affordance is UNCHANGED and still strictly
//     single-item: the per-card "Grab" button (Movies grab directly; Series open
//     the season/episode picker; Adult grabs one scene), and that picker itself.
//     Only the new Select-mode batch is bulk.
//   - This does NOT reverse "Guardrail #3" as numbered in frontend/SEERR_SCOPE.md
//     — that Guardrail is the single-operator / not-multi-user rule, which this
//     feature fully preserves (still one operator, no user/role/permission
//     concept is introduced). SEERR_SCOPE.md needs no change. What is narrowed is
//     only the "no bulk actions on Discover" convention text, not the
//     single-operator model.
//
// This screen is split across discover/: the grab pipeline, setup-modal, and
// PaginatedStrip pagination engine shared by both tabs live in shared.tsx;
// MainstreamDiscover (rows/cards/library/search) in Mainstream.tsx; AdultDiscover
// (scene rows/cards/drill-down) in Adult.tsx; select-mode selection state in
// selection.tsx, its floating bar in BulkBar.tsx, its results in
// BulkResultModal.tsx; this file is the thin tab shell.

import {
  type Component,
  createEffect,
  createSignal,
  on,
  Show,
  Switch,
  Match,
} from "solid-js";
import { useLocation } from "@solidjs/router";
import {
  Button,
  type TabDef,
  ScreenTabs,
  useAdultEnabled,
} from "../../components/ui";
import { MainstreamDiscover } from "./Mainstream";
import { AdultDiscover } from "./Adult";
import { SelectionProvider, createSelection } from "./selection";
import { BulkBar } from "./BulkBar";

// MAINSTREAM_TABS replaces the old Movies/Series/Adult set: Mainstream (all
// TMDB titles, both modes combined on one page) and Adult (TPDB scene view).
const MAINSTREAM_TABS: TabDef[] = [
  { id: "mainstream", label: "Mainstream" },
  { id: "adult", label: "Adult" },
];

// Discover is the tab shell: Mainstream (combined Movies+Series) / Adult. Tabs
// register with the app shell (which draws the bar in its consistent location);
// rendered standalone (a unit test with no shell context) it falls back to
// drawing the bar inline, the same pattern ModeTabs uses — so tests can still
// click "Adult" without mounting the whole shell.
//
// editMode drives the Optional RSS Discover rows + inline row editor feature;
// selection (selection.tsx) drives the F3 bulk-grab Select mode. Both live in
// the tab bar's trailing slot and are MUTUALLY EXCLUSIVE (turning one on forces
// the other off). Switching tabs OR changing route resets both — and, for
// selection, clears every checked card — so no stale Edit/Select state (or, far
// more importantly, no stale selection that could fire a live grab of a
// no-longer-visible title — plan pre-mortem #5) carries across a context change.
export const Discover: Component = () => {
  const adultEnabled = useAdultEnabled();
  const selection = createSelection();
  const [tab, setTab] = createSignal("mainstream");
  const [editMode, setEditMode] = createSignal(false);
  // mainstreamFiltering mirrors MainstreamDiscover's active-filter state (it
  // owns the filter signal; this toggle lives one level up). Row-reordering
  // Edit mode is meaningless against a filtered grid, so the Edit toggle is
  // disabled and forced off while a Mainstream filter is active. Select mode is
  // NOT disabled here — selecting from a filtered grid is a primary use case.
  const [mainstreamFiltering, setMainstreamFiltering] = createSignal(false);
  // adultSorting is AdultDiscover's equivalent of mainstreamFiltering — Edit
  // mode (row reordering) is meaningless against a sorted grid, same
  // reasoning as the Mainstream filter case, so the toggle is disabled and
  // forced off while an Adult sort is active too.
  const [adultSorting, setAdultSorting] = createSignal(false);
  createEffect(() => {
    if (mainstreamFiltering() || adultSorting()) setEditMode(false);
  });

  // Route-change clear (pre-mortem #5, secondary defense — the registry
  // orphan-drop in selection.tsx is the primary one). useLocation throws
  // without a Router (the standalone Discover unit tests mount bare), so it is
  // guarded: no router → no location → the effect tracks a constant and never
  // fires. In the real app, navigating away/back clears any stale selection.
  let location: ReturnType<typeof useLocation> | undefined;
  try {
    location = useLocation();
  } catch {
    location = undefined;
  }
  createEffect(
    on(
      () => location?.pathname,
      () => {
        selection.clear();
        selection.setSelectMode(false);
      },
      { defer: true },
    ),
  );

  // Edit is disabled while a filter/sort grid is up (its rows can't reorder);
  // Select never is.
  const editDisabled = () =>
    (tab() === "mainstream" && mainstreamFiltering()) ||
    (tab() === "adult" && adultSorting());

  const toggleSelect = () => {
    const on = !selection.selectMode();
    selection.setSelectMode(on);
    if (on) setEditMode(false); // mutual exclusivity
    if (!on) selection.clear(); // leaving Select mode drops the working set
  };

  const toggleEdit = () =>
    setEditMode((v) => {
      const next = !v;
      if (next) selection.setSelectMode(false); // mutual exclusivity
      return next;
    });

  // toggles is the Select + Edit pair shown in the tab bar's trailing slot (and,
  // when Adult is disabled and there is no tab bar, above the Mainstream page)
  // so bulk-grab is reachable in every Discover configuration.
  const toggles = () => (
    <div class="flex items-center gap-1">
      <Button class="!px-3 !py-1.5 !text-sm" onClick={toggleSelect}>
        {selection.selectMode() ? "Done selecting" : "Select"}
      </Button>
      <Button
        class="!px-3 !py-1.5 !text-sm"
        disabled={editDisabled()}
        onClick={toggleEdit}
      >
        {editMode() ? "Done" : "Edit"}
      </Button>
    </div>
  );

  const selectTab = (id: string) => {
    setEditMode(false);
    selection.setSelectMode(false);
    selection.clear();
    setTab(id);
  };

  return (
    <SelectionProvider store={selection}>
      <div>
        {/* When Adult mode is disabled, do NOT render ScreenTabs at all — a
            filtered-to-one-entry tab bar would show a visibly degenerate lone
            "Mainstream" pill. Render Mainstream content directly instead (a
            Critic-mandated fix, see ralplan-adult-disable-switch.md step 6). The
            Select/Edit toggles still render above it so bulk-grab isn't dead in
            this configuration. If `tab()` was "adult" when this flips off, it
            resolves for free since only MainstreamDiscover renders regardless of
            `tab()`'s value. */}
        <Show
          when={adultEnabled()}
          fallback={
            <div class="mt-4">
              <div class="mb-2 flex justify-end">{toggles()}</div>
              <MainstreamDiscover
                editMode={editMode}
                onFilteringChange={setMainstreamFiltering}
              />
            </div>
          }
        >
          <ScreenTabs
            tabs={MAINSTREAM_TABS}
            current={tab}
            onSelect={selectTab}
            trailing={toggles()}
            class="flex items-center gap-1"
          />
          <div class="mt-4">
            <Switch>
              <Match when={tab() === "adult"}>
                <AdultDiscover
                  editMode={editMode}
                  onSortingChange={setAdultSorting}
                />
              </Match>
              <Match when={tab() === "mainstream"}>
                <MainstreamDiscover
                  editMode={editMode}
                  onFilteringChange={setMainstreamFiltering}
                />
              </Match>
            </Switch>
          </div>
        </Show>
        <BulkBar />
      </div>
    </SelectionProvider>
  );
};
