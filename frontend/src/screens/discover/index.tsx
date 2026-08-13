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
  Card,
  Muted,
  ScreenTabs,
  type TabDef,
  SectionLockOverlay,
  useAdultEnabled,
  useSectionLock,
} from "../../components/ui";
import { ADULT_CONTENT_SECTION, sectionLabel } from "../../api/sectionLock";
import { MainstreamDiscover } from "./Mainstream";
import { AdultDiscover } from "./Adult";
import { SelectionProvider, createSelection } from "./selection";
import { BulkBar } from "./BulkBar";
import {
  ADULT_MEDIA_TABS,
  MAINSTREAM_MEDIA_TABS,
  type AdultMediaTab,
  type MainstreamMediaTab,
} from "../mediaNav";

const LEGACY_DISCOVER_TABS: TabDef[] = [
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
export const DiscoverMainstream: Component = () => {
  const selection = createSelection();
  const [tab, setTab] = createSignal<MainstreamMediaTab>("series");
  const [editMode, setEditMode] = createSignal(false);
  // mainstreamFiltering mirrors MainstreamDiscover's active-filter state (it
  // owns the filter signal; this toggle lives one level up). Row-reordering
  // Edit mode is meaningless against a filtered grid, so the Edit toggle is
  // disabled and forced off while a Mainstream filter is active. Select mode is
  // NOT disabled here — selecting from a filtered grid is a primary use case.
  const [mainstreamFiltering, setMainstreamFiltering] = createSignal(false);
  createEffect(() => {
    if (mainstreamFiltering()) setEditMode(false);
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
  const editDisabled = () => mainstreamFiltering();

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
    setTab(id as MainstreamMediaTab);
  };

  return (
    <SelectionProvider store={selection}>
      <div>
        <ScreenTabs
          tabs={MAINSTREAM_MEDIA_TABS}
          current={tab}
          onSelect={selectTab}
          trailing={toggles()}
          class="flex items-center gap-1"
        />
        <div class="mt-4">
          <MainstreamDiscover
            contentType={tab()}
            editMode={editMode}
            onFilteringChange={setMainstreamFiltering}
          />
        </div>
        <BulkBar />
      </div>
    </SelectionProvider>
  );
};

export const DiscoverAdult: Component = () => {
  const adultEnabled = useAdultEnabled();
  const lock = useSectionLock();
  const adultLocked = () => lock.isLocked(ADULT_CONTENT_SECTION);
  const selection = createSelection();
  const [tab, setTab] = createSignal<AdultMediaTab>("scenes");
  const [editMode, setEditMode] = createSignal(false);
  const [adultSorting, setAdultSorting] = createSignal(false);
  createEffect(() => {
    if (adultSorting()) setEditMode(false);
  });

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

  const toggleSelect = () => {
    const on = !selection.selectMode();
    selection.setSelectMode(on);
    if (on) setEditMode(false);
    if (!on) selection.clear();
  };

  const toggleEdit = () =>
    setEditMode((v) => {
      const next = !v;
      if (next) selection.setSelectMode(false);
      return next;
    });

  const toggles = () => (
    <Show when={tab() === "scenes"}>
      <div class="flex items-center gap-1">
        <Button class="!px-3 !py-1.5 !text-sm" onClick={toggleSelect}>
          {selection.selectMode() ? "Done selecting" : "Select"}
        </Button>
        <Button
          class="!px-3 !py-1.5 !text-sm"
          disabled={adultSorting()}
          onClick={toggleEdit}
        >
          {editMode() ? "Done" : "Edit"}
        </Button>
      </div>
    </Show>
  );

  const selectTab = (id: string) => {
    setEditMode(false);
    selection.setSelectMode(false);
    selection.clear();
    setTab(id as AdultMediaTab);
  };

  return (
    <SelectionProvider store={selection}>
      <Show
        when={adultEnabled()}
        fallback={<Muted class="mt-4">Adult mode is disabled in Settings.</Muted>}
      >
        <ScreenTabs
          tabs={ADULT_MEDIA_TABS}
          current={tab}
          onSelect={selectTab}
          trailing={toggles()}
          class="flex items-center gap-1"
        />
        <div class="mt-4">
          <Show
            when={!adultLocked()}
            fallback={<SectionLockOverlay label={sectionLabel(ADULT_CONTENT_SECTION)} />}
          >
            <Show when={tab() === "scenes"} fallback={<AdultMoviesPlaceholder />}>
              <AdultDiscover
                editMode={editMode}
                onSortingChange={setAdultSorting}
              />
            </Show>
          </Show>
        </div>
        <BulkBar />
      </Show>
    </SelectionProvider>
  );
};

const AdultMoviesPlaceholder: Component = () => (
  <Card title="Adult Movies">
    <Muted>
      Adult Movies will use TPDB/catalog adult movie entities. The route and tab
      are scaffolded here so the follow-up enrichment/data slice has a dedicated
      surface instead of reusing Scenes.
    </Muted>
  </Card>
);

export const Discover: Component = () => {
  const adultEnabled = useAdultEnabled();
  const lock = useSectionLock();
  const adultLocked = () => lock.isLocked(ADULT_CONTENT_SECTION);
  const selection = createSelection();
  const [tab, setTab] = createSignal("mainstream");
  const [editMode, setEditMode] = createSignal(false);
  const [mainstreamFiltering, setMainstreamFiltering] = createSignal(false);
  const [adultSorting, setAdultSorting] = createSignal(false);
  createEffect(() => {
    if (mainstreamFiltering() || adultSorting()) setEditMode(false);
  });

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

  const editDisabled = () =>
    (tab() === "mainstream" && mainstreamFiltering()) ||
    (tab() === "adult" && adultSorting());

  const toggleSelect = () => {
    const on = !selection.selectMode();
    selection.setSelectMode(on);
    if (on) setEditMode(false);
    if (!on) selection.clear();
  };

  const toggleEdit = () =>
    setEditMode((v) => {
      const next = !v;
      if (next) selection.setSelectMode(false);
      return next;
    });

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
            tabs={LEGACY_DISCOVER_TABS}
            current={tab}
            onSelect={selectTab}
            trailing={toggles()}
            class="flex items-center gap-1"
          />
          <div class="mt-4">
            <Switch>
              <Match when={tab() === "adult" && adultLocked()}>
                <SectionLockOverlay
                  label={sectionLabel(ADULT_CONTENT_SECTION)}
                />
              </Match>
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
