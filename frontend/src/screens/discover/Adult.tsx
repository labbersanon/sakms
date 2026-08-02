// AdultDiscover — the scene-shaped browse and its cards: a search bar over
// the Prowlarr-matched "newest rows" (see fetchAdultNewestRows) — including
// the RSS-derived Performers/Studios admin rows, dynamically gender-split for
// Performers (see the newestrow: branch in renderRow) — plus optional
// StashDB/FansDB scene/Studios/Performers rows shown only when that
// connection is configured (see the STASH_BOX_ROWS and configuredServices doc
// comments below). Searching swaps the rows for a plain result grid; clicking
// a Studio/Performer card — RSS-derived or a stash-box (StashDB/FansDB)
// source — drills down into a paginated (stash-box) or single-page
// (RSS-derived, live) grid of just that entity's scenes. A stash-box drill
// threads its `box` through AdultDrill so the box-scoped id hits its own
// catalog, never a different one (see EntityCard/AdultDrill). Extracted from
// the original single-file Discover.tsx.
//
// Row order (inline row editor): the browse row block is driven by a merged,
// operator-reorderable key list (see api/rowOrder.ts's mergeRowOrder). Two row
// classes: Studios/Performers/stash-box rows are STRUCTURAL (drag-reorder + a
// Show/Hide toggle, no Delete — they have no deletable backing entity);
// admin-added newest rows (target=adult) are ENTITY rows (drag-reorder +
// enable-toggle + Delete inline, backed by their own CRUD APIs). Only applies
// to the plain browse view — search results and a Studio/Performer drill-down
// are unaffected. editMode (from Discover/index.tsx's tab-bar Edit toggle)
// swaps the row list for RowEditor's UI.
//
// RSS feeds (target=adult) are NOT part of this row-order/editor system: their
// admin (add/edit/enable/delete/reorder) lives in Settings → UI → Discover →
// Adult, and each enabled feed's RssFeedRow renders here on an independent
// path, after all structural rows, ordered by the feed's own sort_order — fully
// decoupled from the row-order store so editing the structural order can never
// relocate a feed row.

import {
  type Component,
  type JSX,
  createEffect,
  createResource,
  createSignal,
  onCleanup,
  For,
  Show,
} from "solid-js";
import {
  type AdultDiscoverItem,
  type AdultSearchScenesPage,
  type AdultSortBy,
  type PerformerSummary,
  type SearchReleaseResult,
  type StashBox,
  type StudioSummary,
  fetchAdultDescription,
  fetchAdultDiscoverMergedRecent,
  fetchAdultDiscoverSorted,
  fetchAdultSearch,
  fetchNewestEntityScenes,
  fetchStashBoxPerformers,
  fetchStashBoxPerformerScenes,
  fetchStashBoxScenes,
  fetchStashBoxStudios,
  fetchStashBoxStudioScenes,
  proxyImage,
} from "../../api/discover";
import { type AdultSortValue, AdultSortBar } from "./FilterSortBar";
import { fetchConnections } from "../../api/settings";
import {
  type AdultNewestReleaseItem,
  deleteAdultNewestRow,
  fetchAdultNewestRowItems,
  fetchAdultNewestRows,
  fetchPerformerGenders,
  updateAdultNewestRow,
} from "../../api/adultNewestRows";
import { Button, ErrorText, Muted, yearOf } from "../../components/ui";
import {
  type GrabTarget,
  ConfigureConnectionModal,
  PaginatedStrip,
  SearchReleasePicker,
  SelectCheckbox,
  TextPoster,
  notConfiguredService,
} from "./shared";
import { useSelection } from "./selection";
import { type DetailTarget, DetailPopup } from "./DetailPopup";
import { type RssFeed, fetchRssFeeds } from "../../api/rssFeeds";
import { RssFeedRow } from "./RssFeedRows";
import { RowEditor, type RowDescriptor } from "./RowEditor";
import { useRowOrder } from "./useRowOrder";

// sourceLabel maps a non-TPDB AdultDiscoverItem.source to its display label —
// "" (no label) for "tpdb" (the default, unlabeled source) or an unrecognized/
// absent value, so AdultCard's subtitle only ever adds a label for a scene
// that actually came from an optional stash-box source (StashDB/FansDB).
const sourceLabel = (source: string): string => {
  switch (source) {
    case "stashdb":
      return "StashDB";
    case "fansdb":
      return "FansDB";
    default:
      return "";
  }
};

// toAdultDiscoverItem adapts an AdultNewestReleaseItem (the admin newest-rows
// pipeline's cached match) into the AdultDiscoverItem shape AdultCard/DetailPopup
// already render — the two differ only in two fields the newest-rows DTO
// doesn't carry (rating/slug; durationSeconds and releaseTitle ARE real now,
// threaded through from identify.MatchResult.RuntimeSeconds and
// prowlarr.Release.Title respectively — see adultnewest.MatchedRelease.
// EntityDurationSeconds/FirstSeenReleaseTitle's doc comments for the live
// bugs this fixes: a hardcoded 0 duration silently failed to auto-qualify
// anything against Adult's bitrate-quality-floor scorer, and a Grab-time
// query reconstructed from studio+title could find zero raw Prowlarr
// results even when a real release existed, since TPDB's own title text
// includes tokens real indexer filenames never contain). rating: 0 renders
// no ★; slug: "" makes DetailPopup's TPDB external link fall through to
// undefined rather than a guaranteed-broken URL. releaseTitle passes through
// unchanged via the spread below (already present on AdultNewestReleaseItem).
const toAdultDiscoverItem = (
  item: AdultNewestReleaseItem,
): AdultDiscoverItem => ({
  ...item,
  rating: 0,
  slug: "",
});

// AdultCard is one scene, from TPDB or (via the merged row / an optional
// StashDB/FansDB row) a stash-box source. Both frequently return no art, so
// the image is Show-guarded with a text fallback (the old frontend rendered
// Adult text-only; this adds art where the source provides it, via the
// proxy). A non-"tpdb" item.source appends a provenance label to the
// subtitle line so a merged-in or optional-source scene is distinguishable.
//
// Clicking the card body (poster/title/subtitle) opens DetailPopup via
// onDetail — the card's only grab affordance, since 2026-08-02 removed the
// inline Grab button that used to sit below it. The native title= tooltip
// (previously the scene's own title, not an overview — AdultDiscoverItem has
// no overview field at all) is replaced by a CSS-only hover overlay showing
// the same studio/date summary the subtitle line already displays — there is
// no description to show for a scene, so this isn't a richer version of the
// removed tooltip, just its replacement per the same convention PosterCard
// uses.
const AdultCard: Component<{
  item: AdultDiscoverItem;
  onDetail: (t: DetailTarget) => void;
  // onOpenReleases, when passed (only by the catalog-Search result grid),
  // reroutes the card's primary click to open the release picker (seeded with
  // this scene's already-fetched variants) instead of DetailPopup. Browse/
  // drill-down call sites omit it, so their click→DetailPopup behavior is
  // unchanged. (It also used to suppress the one-click Grab button (M3); that
  // second job went away with the button itself on 2026-08-02.)
  onOpenReleases?: () => void;
}> = (props) => {
  const selection = useSelection();
  const inSelect = () => selection?.selectMode() ?? false;
  const src = () => proxyImage(props.item.image);
  const subtitle = () =>
    [props.item.studio, yearOf(props.item.date), sourceLabel(props.item.source)]
      .filter(Boolean)
      .join(" · ");
  // A scene is one selectable item, keyed on its stash-box/TPDB scene id.
  const sceneKey = () => `adult:${props.item.id}`;
  const sceneTarget = (): GrabTarget => ({
    mode: "adult",
    label: props.item.title,
    request: {
      title: props.item.title,
      studio: props.item.studio,
      releaseTitle: props.item.releaseTitle,
      durationSeconds: props.item.durationSeconds,
      // Direct-enclosure grab (D4/C1): when the card's feed is currently fresh
      // it carries a downloadUrl+protocol, so the bulk batch dispatches straight
      // to the download client and skips Prowlarr. Absent (browse-only / stale
      // feed) → undefined, dropped by JSON.stringify → the Prowlarr search path
      // runs unchanged. protocol maps to downloadProtocol.
      //
      // Claude 2026-08-02: this used to read "both the single Grab and the bulk
      // batch"; the single-Grab half is gone with the inline Grab button.
      // Reason: SELECT-MODE BULK GRAB IS NOW THE ONLY WAY TO REACH THE
      // Prowlarr-skipping path for a fresh-feed scene — DetailPopup's Adult
      // grab always searches Prowlarr and cannot carry these two fields. This
      // is the one real capability loss in the card cleanup, accepted and
      // recorded in .omc/plans/autopilot-impl-discover-card-cleanup.md §0.2
      // (GATE-A).
      // Troubleshooting: keeps the loss traceable so a future session does not
      // read the survival of these two fields as "nothing changed".
      // Review if: DetailPopup ever learns to thread downloadUrl through its
      // Adult grab, at which point the one-click path is restored and §0.2's
      // GATE-A is moot.
      downloadUrl: props.item.downloadUrl,
      downloadProtocol: props.item.protocol,
    },
  });
  createEffect(() => {
    if (!selection || !inSelect()) return;
    const cleanup = selection.register(sceneKey(), sceneTarget());
    onCleanup(cleanup);
  });
  const checked = () => selection?.has(sceneKey()) ?? false;
  // In select-mode the card body toggles selection instead of opening the
  // DetailPopup; outside it, the click-to-open behavior is unchanged.
  const onBody = () => {
    if (inSelect()) {
      selection?.toggle(sceneKey());
      return;
    }
    if (props.onOpenReleases) {
      props.onOpenReleases();
      return;
    }
    props.onDetail({ mode: "adult", item: props.item });
  };
  // Claude 2026-08-02: scene-card width 200px → 240px.
  // Reason: proportional match to Mainstream's 180→220px bump (+20%), the
  // conservative end of the spec's 230-260 band — see
  // .omc/plans/autopilot-impl-discover-card-cleanup.md §3.1. aspect-video is
  // deliberately unchanged (AC7).
  // Troubleshooting: EntityCard's two w-[200px] tiles below are a DIFFERENT
  // component and stay at 200px — never find/replace this width in this file.
  // Review if: the carousel's visible-card count drops below 3 on desktop.
  return (
    <div class="w-[240px] shrink-0">
      <div class="group cursor-pointer" onClick={onBody}>
        <div class="relative aspect-video overflow-hidden rounded-lg border border-border bg-surface">
          <Show when={inSelect()}>
            <SelectCheckbox checked={checked()} />
          </Show>
          <Show when={src()} fallback={<TextPoster label={props.item.title} />}>
            <img
              src={src()}
              alt={props.item.title}
              loading="lazy"
              class="h-full w-full object-cover"
            />
          </Show>
          <div class="absolute inset-0 flex items-end bg-black/70 p-2 opacity-0 transition-opacity group-hover:opacity-100">
            <p class="line-clamp-4 text-xs text-white">
              {subtitle() || props.item.title}
            </p>
          </div>
        </div>
        <div class="mt-1.5 truncate text-sm text-fg">{props.item.title}</div>
        <div class="truncate text-xs text-muted">{subtitle() || "—"}</div>
      </div>
      {/* Claude 2026-08-02: the inline Grab button and its
          `!onOpenReleases || inSelect()` M3 suppression gate used to live here.
          Reason: the gate existed SOLELY to hide that button on catalog-Search
          cards, so it had no condition left to express once the button went —
          see .omc/plans/autopilot-impl-discover-card-cleanup.md §3.2. Unlike
          PosterCard, AdultCard has nothing else to render here (its
          SelectCheckbox sits inside the poster frame above), so the slot is
          empty rather than collapsed.
          Troubleshooting: select-mode is deliberately UNAFFECTED — the
          checkbox and sceneTarget()'s registration both sit above and are
          untouched, which is what preserves the direct-enclosure
          (Prowlarr-skipping) bulk path §0.2 relies on.
          Review if: any non-select-mode affordance is added back to this
          card. */}
    </div>
  );
};

// EntityCard is one Studio or Performer on the Adult browse rows — image-or-text
// tile + name, no grab (these are pure browse/navigation, not gradable items).
// Art frequently absent, so the image is Show-guarded with a text fallback and
// any non-empty URL is routed through the proxy (never hot-linked).
//
// kind selects the artwork frame: "performer" is a 2:3 portrait crop
// (object-cover — a headshot fills the frame the way a person photo should),
// "studio" is a 16:9 frame with the logo letterboxed via object-contain
// rather than cropped — a studio logo is usually squarish or wide-with-
// padding, not portrait-shaped, so object-cover on it was cutting off the
// logo's edges (found live 2026-07-15: both Studios and Performers shared
// one aspect-video/object-cover frame before this fix).
//
// onSelect is OPTIONAL: the Studios/Performers rows that support drill-down
// (TPDB's, and now StashDB/FansDB's) pass it, and the whole card renders as a
// button that drills into that entity's scenes (via setDrill). A stash-box row
// passes `box: row.box` too, so setDrill carries the `(box, id)` identity a
// stash-box entity actually is — the box-scoped fetcher then queries that box's
// own catalog, never TPDB's. Rows with no drill target (e.g. the admin
// newest-rows studio/performer tiles) omit onSelect, rendering a plain,
// non-interactive <div> tile instead — same art/name, no click behavior.
// namesDiverged (merged rows only) flags a fuzzy — but not near-exact — pairing
// of a TPDB and a StashDB entity: the two source names genuinely differ, so the
// card MUST show BOTH ("TPDB: {altName}" and "StashDB: {name}") as two
// separately-legible lines rather than silently collapsing to one. This is the
// provenance-surfacing safety valve for the fuzzy merge (plan Q3/Q6/Principle
// 4): an operator has to actually SEE both names to catch a bad automatic merge,
// which a single-line `truncate` at 200px would hide. altName holds the OTHER
// (TPDB) name and is only set when namesDiverged. The non-diverged common case
// (TPDB-only, StashDB-only, FansDB, and near-exact-collapsed merged cards) is
// unchanged — a single `truncate` line.
const EntityCard: Component<{
  kind: "studio" | "performer";
  name: string;
  image: string;
  altName?: string;
  namesDiverged?: boolean;
  onSelect?: () => void;
}> = (props) => {
  const src = () => proxyImage(props.image);
  // hoverTitle carries both labeled names on a diverged card so the full,
  // un-clamped text is available on hover; a plain card keeps its single name.
  const hoverTitle = () =>
    props.namesDiverged
      ? `TPDB: ${props.altName ?? ""} / StashDB: ${props.name}`
      : props.name;
  const artwork = () => (
    <>
      <div
        class="overflow-hidden rounded-lg border border-border bg-surface"
        classList={{
          "aspect-[2/3]": props.kind === "performer",
          "aspect-video": props.kind === "studio",
        }}
      >
        <Show when={src()} fallback={<TextPoster label={props.name} />}>
          <img
            src={src()}
            alt={props.name}
            loading="lazy"
            class="h-full w-full"
            classList={{
              "object-cover": props.kind === "performer",
              "object-contain": props.kind === "studio",
            }}
          />
        </Show>
      </div>
      <Show
        when={props.namesDiverged}
        fallback={<div class="mt-1.5 truncate text-sm text-fg">{props.name}</div>}
      >
        {/* Two per-line line-clamp-1 rows (NOT one shared truncate) so both the
            TPDB and StashDB names stay genuinely visible within the 200px tile —
            neither line eats the width and hides the other. */}
        <div class="mt-1.5 text-sm text-fg">
          <div class="line-clamp-1">TPDB: {props.altName}</div>
          <div class="line-clamp-1">StashDB: {props.name}</div>
        </div>
      </Show>
    </>
  );
  return (
    <Show
      when={props.onSelect}
      fallback={
        <div class="w-[200px] shrink-0 text-left" title={hoverTitle()}>
          {artwork()}
        </div>
      }
    >
      {(onSelect) => (
        <button
          type="button"
          class="w-[200px] shrink-0 text-left"
          title={hoverTitle()}
          onClick={() => onSelect()()}
        >
          {artwork()}
        </button>
      )}
    </Show>
  );
};

// STASH_BOX_ROWS drives the optional StashDB/FansDB row sections — one entry
// per box, each gated behind configuredServices().has(box) so it renders
// nothing at all (not even PaginatedStrip's "Nothing here yet" fallback)
// when that connection isn't configured. sceneRows lists which scene feeds
// that box gets (StashDB: Trending only; FansDB: both — nothing merges
// FansDB into the TPDB feed). Studios/Performers are fixed per box (every
// box gets both). This table is the collapsed form of what was several
// near-identical hand-written PaginatedStrip blocks — same fetchStashBox*
// calls, same card components, just data-driven.
//
// The old fixed "Recently Released"/"Highest Rated" TPDB catalog-browse rows
// (fetchAdultDiscoverMergedRecent/fetchAdultDiscoverCategory) were removed
// 2026-07-15 — stale/redundant once the Prowlarr-matched "newest rows"
// (fetchAdultNewestRows, above the Studios/Performers browse rows below)
// shipped as the confirmed-downloadable alternative.
const STASH_BOX_ROWS: {
  box: StashBox;
  label: string;
  sceneRows: { title: string; kind: "recent" | "trending" }[];
}[] = [
  {
    box: "stashdb",
    label: "StashDB",
    sceneRows: [{ title: "StashDB Trending", kind: "trending" }],
  },
  {
    box: "fansdb",
    label: "FansDB",
    sceneRows: [
      { title: "FansDB Recently Released", kind: "recent" },
      { title: "FansDB Trending", kind: "trending" },
    ],
  },
];

// STASH_BOX_ORDERABLE_ROWS flattens STASH_BOX_ROWS into individual rows, each
// with its own stable Discover row-order key ("stashbox:{box}:{kind}") — the
// row-order feature interleaves these individually with newest
// rows/Studios/Performers, not as one per-box block.
type StashBoxOrderableRow = {
  key: string;
  box: StashBox;
  label: string;
  shape: "scenes" | "studios" | "performers";
  sceneKind?: "recent" | "trending";
};
const STASH_BOX_ORDERABLE_ROWS: StashBoxOrderableRow[] = STASH_BOX_ROWS.flatMap(
  (row) => [
    ...row.sceneRows.map((sr) => ({
      key: `stashbox:${row.box}:${sr.kind}`,
      box: row.box,
      label: sr.title,
      shape: "scenes" as const,
      sceneKind: sr.kind,
    })),
    // Studios/Performers rows are generated ONLY for FansDB. StashDB has no
    // dedicated Studios/Performers row at all — its entities surface only via
    // the RSS-derived newestrow: Performers/Studios rows once matched (see
    // renderRow). StashDB's scene rows (e.g. Trending, above) and all of
    // FansDB's rows are untouched.
    ...(row.box === "fansdb"
      ? [
          {
            key: `stashbox:${row.box}:studios`,
            box: row.box,
            label: `${row.label} Studios`,
            shape: "studios" as const,
          },
          {
            key: `stashbox:${row.box}:performers`,
            box: row.box,
            label: `${row.label} Performers`,
            shape: "performers" as const,
          },
        ]
      : []),
  ],
);

// AdultDrill is the active drill-down target — a discriminated union of two
// shapes:
//   - FansDB variant ({box, id}): a stash-box drill carrying the `(box, id)`
//     identity a stash-box entity actually is, routed to the box-scoped
//     fetcher. Unchanged from before this feature.
//   - Newest variant ({source: "newest"}): a RSS-derived Performers/Studios
//     admin-row card (see the newestrow: branch below). The drill loader
//     fires the live entity-scenes handler (fetchNewestEntityScenes) by
//     entity NAME (this pool has no stable box-scoped id, only a
//     Prowlarr/RSS name match) — see internal/api/adultdiscover_newest_scenes.go.
// The two are told apart by the presence of `box` (`"box" in d`).
//
// `image` is on BOTH members: the bio banner's art frame renders the same
// artwork the browse card the operator just clicked was already showing. Every
// setDrill site already has it in scope, so this costs no extra fetch.
type AdultDrill =
  | {
      kind: "studio" | "performer";
      name: string;
      image: string;
      box: StashBox;
      id: string;
    }
  | {
      kind: "studio" | "performer";
      name: string;
      image: string;
      source: "newest";
    };

// AdultDiscover is the scene-shaped browse, row-based like Mainstream: a search
// bar over two ordered scene rows (Recently Released, Highest Rated), a Studios
// row, and a Performers row. Searching swaps the rows for a plain result grid;
// clicking a Studio/Performer card drills down into a paginated grid of just
// that entity's scenes (with a "Back to browse" control). Owns the single grab
// dialog for every scene card (rows, search, drill-down) and the not-configured
// setup modal, raised once when any strip's fetch reports TPDB missing.
// editMode (from Discover/index.tsx's tab-bar Edit toggle) swaps the browse
// row block for RowEditor's reorder/enable/delete UI. onSortingChange lets
// the tab shell disable its Edit toggle while a sort is active — mirrors
// Mainstream.tsx's onFilteringChange (RowEditor already can't render during
// a sort since it lives in the sorting() fallback branch below, but leaving
// the Edit button clickable-but-inert while sorting reads as a bug from the
// operator's side, same reasoning Mainstream's gating documents).
export const AdultDiscover: Component<{
  editMode?: () => boolean;
  onSortingChange?: (active: boolean) => void;
}> = (props) => {
  // Claude 2026-08-02: the grabTarget signal (and the <GrabDialog> it drove)
  // was removed along with AdultCard's inline Grab button — AdultCard was its
  // only writer, so both became unreachable.
  // Reason: Adult's grab wiring is the ONE deliberate exception to the card
  // cleanup's "leave the onGrab cascade threaded" decision, because here it is
  // genuinely dead AND noUnusedLocals fails the build on the unread signal
  // pair — see .omc/plans/autopilot-impl-discover-card-cleanup.md §0.5/§3.3.
  // Mainstream keeps its GrabDialog (TraktWatchlistRow still writes it).
  // Troubleshooting: GrabTarget is still imported — sceneTarget()'s return type
  // uses it for the bulk-grab registration.
  // Review if: a per-card single grab is ever restored to Adult Discover.
  const [detailTarget, setDetailTarget] = createSignal<DetailTarget | null>(null);
  // releasePicker is the catalog-Search release picker for a clicked searched
  // scene card — seeded with that scene's already-fetched release variants, so
  // it makes ZERO network calls. Browse/drill-down cards never set it.
  const [releasePicker, setReleasePicker] = createSignal<
    { title: string; releases: SearchReleaseResult[] } | null
  >(null);
  const [setupError, setSetupError] = createSignal<unknown>(null);
  const [dismissedSetup, setDismissedSetup] = createSignal(false);
  const [reloadToken, setReloadToken] = createSignal(0);

  const [draft, setDraft] = createSignal("");
  const [submitted, setSubmitted] = createSignal("");
  const searching = () => submitted().trim().length > 0;

  // drill is the active Studio/Performer drill-down (null = the browse rows).
  const [drill, setDrill] = createSignal<AdultDrill | null>(null);

  // entityDetail is the drilled-into performer's/studio's catalog bio — ONE
  // call per drill-down open. Keyed on `drill` itself, so it refires exactly
  // once when a drill opens and NEVER on pagination (PaginatedStrip's own
  // "Show more" doesn't touch this signal). A null drill suppresses the fetcher
  // entirely, so the browse view fires nothing.
  //
  // The fetcher swallows its own error to empty text: this app has no
  // ErrorBoundary anywhere, so an unguarded read of an errored Solid resource
  // throws mid-render and takes the whole SPA down. "Couldn't fetch it" and
  // "this entity has no bio" render identically — no banner — which is also
  // semantically right under the feature's no-placeholder rule.
  const [entityDetail] = createResource(drill, async (dd) => {
    try {
      // name is ALWAYS sent; source is the box the drill already knows, when
      // it knows one (the stash-box variant). A newest-row drill has no box.
      return await fetchAdultDescription({
        kind: dd.kind,
        name: dd.name,
        source: "box" in dd ? dd.box : undefined,
      });
    } catch {
      return { text: "", source: "" };
    }
  });
  // Claude 2026-08-02: gate on entityDetail.loading rather than reading
  // entityDetail() directly.
  // Reason: Solid resources RETAIN their previous value across a source-key
  // change while the new fetch is in flight — they don't clear. entityDetail
  // is keyed on `drill`, so opening performer B's drill-down right after
  // closing performer A's would otherwise render A's bio (still sitting in
  // the resource) inside B's banner for the whole descriptionTimeout window.
  // While loading is true there is no trustworthy value to show, so this
  // returns "" — the same "no bio" shape the banner's own <Show> already
  // treats as absent, so no new empty-state branch is needed.
  // Troubleshooting: performer/studio bio banner showing the PREVIOUS
  // entity's biography during a drill switch (Phase 4 architect finding).
  // Review if: entityDetail's fetcher stops being keyed on `drill`.
  const entityBio = () => (entityDetail.loading ? "" : entityDetail()?.text ?? "");

  // adultSort is the sort bar's value. sorting() is true only when a non-
  // default sort is chosen AND the view isn't a search or a drill-down (a
  // sort has no defined meaning inside one studio's/performer's scene list).
  // When sorting, a single sorted grid replaces the browse rows.
  const [adultSort, setAdultSort] = createSignal<AdultSortValue>("default");
  const sorting = () => !searching() && !drill() && adultSort() !== "default";
  createEffect(() => props.onSortingChange?.(sorting()));

  // Changing the sort clears search and any drill-down (all three views are
  // mutually exclusive).
  const applyAdultSort = (v: AdultSortValue) => {
    clearSearch();
    setDrill(null);
    setAdultSort(v);
  };

  // sortTitle labels the sorted grid by the active sort.
  const sortTitle = () =>
    adultSort() === "newest"
      ? "Newest Releases"
      : adultSort() === "recently_created"
        ? "Recently Added"
        : "Recently Updated";

  // connections drives which OPTIONAL Adult Discover sources (StashDB/FansDB)
  // render at all — fetched once on mount, same as any other read-only
  // Settings data. configuredServices() is the set of service names with a
  // stored connection; a StashDB/FansDB row is gated behind
  // configuredServices().has("stashdb"|"fansdb") so it doesn't render AT ALL
  // (not even PaginatedStrip's "Nothing here yet" fallback) when that source
  // isn't configured — TPDB is a required core dependency and has no such
  // gate.
  //
  // The fetcher swallows its own error (-> []) rather than letting the
  // resource enter Solid's errored state: configuredServices() reads
  // connections() bare inside JSX with no <Show when={connections.error}>
  // guard, and this app has no ErrorBoundary anywhere in its tree — an
  // unguarded read of an errored resource re-throws on every subsequent
  // read (by design, for ErrorBoundary integration; see GrabDialog's own
  // doc comment for this exact Solid gotcha), which would crash the whole
  // SPA instead of just hiding these two optional rows. Defaulting to []
  // on failure is also the semantically correct degrade here: "couldn't
  // learn what's configured" and "nothing is configured" both mean the
  // same thing to configuredServices() — don't show the optional rows.
  const [connections] = createResource(async () => {
    try {
      return await fetchConnections();
    } catch {
      return [];
    }
  });
  const configuredServices = () =>
    new Set((connections() ?? []).map((c) => c.service));

  // newest rows are the operator-defined "newest" rows (Prowlarr-backed, matched
  // to TPDB/StashDB/FansDB entities by the background scan) — the confirmed
  // downloadable-right-now value-add this feature exists for, so they lead the
  // browse view. Keyed on reloadToken (not once-on-mount) so an inline
  // enable/delete in the row editor refetches the list, already sortOrder-
  // ascending from the backend (Store.List's ordering). The fetcher swallows
  // its own error (-> []) for the exact same reason connections above does: an
  // unguarded read of an errored resource re-throws on every render and this app
  // has no ErrorBoundary, which would crash the whole SPA instead of just hiding
  // these optional rows.
  const [newestRowsData] = createResource(reloadToken, async () => {
    try {
      return await fetchAdultNewestRows();
    } catch {
      return [];
    }
  });
  // allNewestRows feeds the row editor / known-keys (a DISABLED newest row must
  // still appear in Edit mode with an unchecked Enabled toggle, so it can be
  // re-enabled — see the descriptorFor/visibleKeys split below);
  // enabledNewestRows feeds the actual rendered Discover row content.
  const allNewestRows = () => newestRowsData() ?? [];
  const enabledNewestRows = () => allNewestRows().filter((r) => r.enabled);

  // performerGenders is the dynamic gender-split (Option 5A) reference list —
  // every distinct gender value actually present across cached Performer rows
  // (sorted, non-empty). A "performer"-typed newestrow renders one
  // PaginatedStrip per value here instead of one flat tile-strip, so a newly
  // backfilled/matched gender produces a new strip automatically, no code
  // change. Keyed on reloadToken for the same reasons newestRowsData is; the
  // fetcher swallows its own error (-> []) for the same "don't crash the
  // ErrorBoundary-less SPA on an optional list" reasoning documented on
  // connections/newestRowsData above — an empty list here just means no
  // gender strips render yet (fresh install / pre-backfill), not an error.
  const [performerGendersData] = createResource(reloadToken, async () => {
    try {
      return await fetchPerformerGenders();
    } catch {
      return [];
    }
  });
  const performerGenders = () => performerGendersData() ?? [];

  const [results] = createResource(
    () => (searching() ? submitted().trim() : null),
    async (q): Promise<AdultSearchScenesPage> => {
      // A search error is surfaced the same way a row's is: handed to
      // setSetupError so a "tpdb isn't configured yet" failure raises the same
      // setup modal (the render's notConfiguredService gate decides modal vs.
      // plain error), instead of being swallowed into an empty "No scenes
      // found". One detection path for every Adult fetch, not two.
      try {
        return await fetchAdultSearch(q);
      } catch (e) {
        setSetupError(e);
        return { items: [], hasMore: false };
      }
    },
  );

  const clearSearch = () => {
    setDraft("");
    setSubmitted("");
  };

  const configureFor = () => notConfiguredService(setupError());

  // --- Discover row order: Studios/Performers/stash-box rows (structural:
  // reorderable + Show/Hide) + admin-added newest rows (entity: reorderable +
  // enable-toggle + Delete), fully interleavable via Edit mode (RowEditor).
  // Only applies to the plain browse view. RSS feeds are intentionally excluded
  // from this system — see the file header and the independent feed-render path
  // in the browse block below. ---
  const [feedsData] = createResource(reloadToken, () =>
    fetchRssFeeds().catch(() => [] as RssFeed[]),
  );
  const adultFeeds = () => (feedsData() ?? []).filter((f) => f.target === "adult");
  const enabledAdultFeeds = () => adultFeeds().filter((f) => f.enabled);

  const stashBoxKnownRows = () =>
    STASH_BOX_ORDERABLE_ROWS.filter((r) => configuredServices().has(r.box));

  // knownKeys is every row this screen currently knows about. Default order
  // (an empty stored order, e.g. a fresh install) matches the page's
  // original hardcoded row sequence exactly: newest rows (which now include
  // the RSS-derived Performers/Studios admin rows themselves — see the
  // newestrow: branch below), then any configured stash-box rows. RSS feeds
  // are deliberately NOT included — they are decoupled from the row-order
  // store entirely and render on their own sort_order-ordered path after all
  // structural rows (so reordering structural rows can never silently
  // relocate a feed row to the tail via mergeRowOrder's unknown-key append).
  const knownKeys = () => [
    ...allNewestRows().map((r) => `newestrow:${r.id}`),
    ...stashBoxKnownRows().map((r) => r.key),
  ];

  const { orderedKeys, persistOrder, isHidden, toggleHidden, error: rowOrderError } =
    useRowOrder("adult", knownKeys);
  // rowActionError covers a newest-row toggle/delete's own mutation failure
  // (updateAdultNewestRow/deleteAdultNewestRow) — a distinct failure mode from
  // useRowOrder's error (a saveRowOrder persist failure) but shown in the
  // same spot; editError combines them so RowEditor's error line doesn't
  // need two <Show> blocks.
  const [rowActionError, setRowActionError] = createSignal("");
  const editError = () => rowOrderError() || rowActionError();

  const descriptorFor = (key: string): RowDescriptor | undefined => {
    if (key.startsWith("newestrow:")) {
      const id = Number(key.slice("newestrow:".length));
      const row = allNewestRows().find((r) => r.id === id);
      return row
        ? { key, label: row.title, kind: "entity", enabled: row.enabled }
        : undefined;
    }
    if (key.startsWith("stashbox:")) {
      const row = STASH_BOX_ORDERABLE_ROWS.find((r) => r.key === key);
      return row
        ? { key, label: row.label, kind: "structural", hidden: isHidden(key) }
        : undefined;
    }
    return undefined;
  };

  const rowDescriptors = (): RowDescriptor[] =>
    orderedKeys()
      .map(descriptorFor)
      .filter((d): d is RowDescriptor => d !== undefined);

  const toggleRowEnabled = async (row: RowDescriptor) => {
    try {
      if (row.key.startsWith("newestrow:")) {
        const id = Number(row.key.slice("newestrow:".length));
        const nr = allNewestRows().find((r) => r.id === id);
        if (!nr) return;
        // Reconstruct the FULL upsert body from the looked-up row — the
        // descriptor only carries key/label/kind/enabled, so genreFilter/rowType
        // must come from the row object or the PUT would silently clear them.
        await updateAdultNewestRow(id, {
          title: nr.title,
          rowType: nr.rowType,
          genreFilter: nr.genreFilter,
          enabled: !nr.enabled,
        });
      } else {
        return;
      }
      setReloadToken((n) => n + 1);
    } catch (e) {
      setRowActionError((e as Error).message);
    }
  };

  const deleteRow = async (row: RowDescriptor) => {
    if (!row.key.startsWith("newestrow:")) return;
    if (!confirm(`Delete "${row.label}"?`)) return;
    try {
      await deleteAdultNewestRow(Number(row.key.slice("newestrow:".length)));
      persistOrder(orderedKeys().filter((k) => k !== row.key));
      setReloadToken((n) => n + 1);
    } catch (e) {
      setRowActionError((e as Error).message);
    }
  };

  const visibleKeys = () =>
    orderedKeys().filter((key) => {
      if (key.startsWith("newestrow:")) {
        return enabledNewestRows().some((r) => `newestrow:${r.id}` === key);
      }
      // structural rows (Studios/Performers/stash-box): shown unless hidden.
      return !isHidden(key);
    });

  const renderRow = (key: string): JSX.Element => {
    if (key.startsWith("newestrow:")) {
      const id = Number(key.slice("newestrow:".length));
      const row = enabledNewestRows().find((r) => r.id === id)!;

      // Scene/Movie rows are unchanged: one strip of grabbable AdultCards.
      if (row.rowType === "movie" || row.rowType === "scene") {
        return (
          <PaginatedStrip<AdultNewestReleaseItem>
            title={row.title}
            reloadToken={reloadToken}
            load={(page) => fetchAdultNewestRowItems(row.id, page)}
            onError={setSetupError}
          >
            {(item) => (
              <AdultCard
                item={toAdultDiscoverItem(item)}
                onDetail={setDetailTarget}
              />
            )}
          </PaginatedStrip>
        );
      }

      // Studio rows: a single strip, drill-down wired (Option 5A — Studios
      // are never gender-split).
      if (row.rowType === "studio") {
        return (
          <PaginatedStrip<AdultNewestReleaseItem>
            title={row.title}
            reloadToken={reloadToken}
            load={(page) => fetchAdultNewestRowItems(row.id, page)}
            onError={setSetupError}
          >
            {(item) => (
              <EntityCard
                kind="studio"
                name={item.title}
                image={item.image}
                onSelect={() =>
                  setDrill({
                    kind: "studio",
                    name: item.title,
                    image: item.image,
                    source: "newest",
                  })
                }
              />
            )}
          </PaginatedStrip>
        );
      }

      // Performer rows: dynamic gender-split (Option 5A) — one PaginatedStrip
      // PER DISCOVERED gender value (performerGenders(), see above), each
      // fetching only that gender's leg via fetchAdultNewestRowItems's
      // optional gender param. Mirrors the old hardcoded single-key→two-strips
      // fan-out, made dynamic: the row stays ONE reorderable useRowOrder key
      // (`newestrow:${row.id}`), only the RENDERING fans out to N strips. No
      // strip renders at all until the backfill/matching has populated at
      // least one gender (empty performerGenders() → empty Fragment, matching
      // the "starts sparse, grows forward" spec intent — not an error state).
      return (
        <For each={performerGenders()}>
          {(gender) => (
            <PaginatedStrip<AdultNewestReleaseItem>
              title={`${gender.charAt(0).toUpperCase()}${gender.slice(1)} Performers`}
              reloadToken={reloadToken}
              load={(page) => fetchAdultNewestRowItems(row.id, page, gender)}
              onError={setSetupError}
            >
              {(item) => (
                <EntityCard
                  kind="performer"
                  name={item.title}
                  image={item.image}
                  onSelect={() =>
                    setDrill({
                      kind: "performer",
                      name: item.title,
                      image: item.image,
                      source: "newest",
                    })
                  }
                />
              )}
            </PaginatedStrip>
          )}
        </For>
      );
    }
    if (key.startsWith("stashbox:")) {
      const row = STASH_BOX_ORDERABLE_ROWS.find((r) => r.key === key)!;
      if (row.shape === "scenes") {
        return (
          <PaginatedStrip
            title={row.label}
            reloadToken={reloadToken}
            load={(page) => fetchStashBoxScenes(row.box, row.sceneKind!, page)}
            onError={setSetupError}
          >
            {(item) => (
              <AdultCard item={item} onDetail={setDetailTarget} />
            )}
          </PaginatedStrip>
        );
      }
      if (row.shape === "studios") {
        return (
          <PaginatedStrip<StudioSummary>
            title={row.label}
            reloadToken={reloadToken}
            load={(page) => fetchStashBoxStudios(row.box, page)}
            onError={setSetupError}
          >
            {(s) => (
              <EntityCard
                kind="studio"
                name={s.name}
                image={s.image}
                onSelect={() =>
                  setDrill({
                    kind: "studio",
                    id: s.id,
                    name: s.name,
                    image: s.image,
                    box: row.box,
                  })
                }
              />
            )}
          </PaginatedStrip>
        );
      }
      return (
        <PaginatedStrip<PerformerSummary>
          title={row.label}
          reloadToken={reloadToken}
          load={(page) => fetchStashBoxPerformers(row.box, page)}
          onError={setSetupError}
        >
          {(p) => (
            <EntityCard
              kind="performer"
              name={p.name}
              image={p.image}
              onSelect={() =>
                setDrill({
                  kind: "performer",
                  id: p.id,
                  name: p.name,
                  image: p.image,
                  box: row.box,
                })
              }
            />
          )}
        </PaginatedStrip>
      );
    }
    // RSS feeds render on their own independent path (see the browse block),
    // never through renderRow — visibleKeys no longer yields rssfeed: keys.
    // This terminal return only satisfies renderRow's return-on-every-path
    // contract for keys that resolve to nothing live.
    return null;
  };

  return (
    <div>
      <form
        class="mb-4 flex gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          // A new search takes precedence over any drill-down (Clear returns to
          // the rows, not back into a stale drill) and any active sort.
          setDrill(null);
          setAdultSort("default");
          setSubmitted(draft());
        }}
      >
        <input
          class="w-full max-w-sm rounded-md border border-border bg-bg px-3 py-2 text-sm text-fg outline-none focus:border-accent"
          placeholder="Search scenes by title…"
          value={draft()}
          onInput={(e) => setDraft(e.currentTarget.value)}
        />
        <Show when={searching()}>
          <Button onClick={clearSearch}>Clear</Button>
        </Show>
      </form>

      <Show when={!searching() && !drill()}>
        <AdultSortBar value={adultSort} onChange={applyAdultSort} />
      </Show>

      <Show when={setupError()}>
        <Show
          when={!dismissedSetup() && configureFor()}
          fallback={<ErrorText>{(setupError() as Error)?.message}</ErrorText>}
        >
          {(service) => (
            <ConfigureConnectionModal
              service={service()}
              onClose={() => setDismissedSetup(true)}
              onSaved={() => {
                setDismissedSetup(true);
                setSetupError(null);
                setReloadToken((n) => n + 1);
              }}
            />
          )}
        </Show>
      </Show>

      <Show
        when={searching()}
        fallback={
          <Show
            when={drill()}
            fallback={
              <Show
                when={sorting()}
                fallback={
                  <>
                <Show when={props.editMode?.()}>
                  <RowEditor
                    rows={rowDescriptors()}
                    onReorder={persistOrder}
                    onToggleEnabled={(r) => void toggleRowEnabled(r)}
                    onToggleHidden={(r) => toggleHidden(r.key)}
                    onDelete={(r) => void deleteRow(r)}
                  />
                  <Show when={editError()}>
                    <ErrorText>{editError()}</ErrorText>
                  </Show>
                </Show>
                <For each={visibleKeys()}>{(key) => renderRow(key)}</For>
                {/* RSS feed rows render on an independent path, fully decoupled
                    from the row-order store: always after all structural rows,
                    ordered by each feed's own sort_order (already applied by
                    fetchRssFeeds' backend List query, so no client-side sort).
                    Feed admin (add/edit/enable/delete/reorder) now lives in
                    Settings → UI → Discover → Adult. */}
                <For each={enabledAdultFeeds()}>
                  {(feed) => (
                    <RssFeedRow
                      feed={feed}
                      reloadToken={reloadToken}
                      onError={setSetupError}
                    />
                  )}
                </For>
                  </>
                }
              >
                <PaginatedStrip<AdultDiscoverItem>
                  title={sortTitle()}
                  reloadToken={() => adultSort()}
                  load={(page) =>
                    adultSort() === "newest"
                      ? fetchAdultDiscoverMergedRecent(page)
                      : fetchAdultDiscoverSorted(adultSort() as AdultSortBy, page)
                  }
                  onError={setSetupError}
                  containerClass="flex flex-wrap gap-3"
                >
                  {(item) => (
                    <AdultCard item={item} onDetail={setDetailTarget} />
                  )}
                </PaginatedStrip>
              </Show>
            }
          >
            {(d) => (
              <div>
                {/* Row 1 — the entity name is promoted out of the scene
                    strip's heading and into the Back-button row, UNGATED for
                    both performers and studios. min-w-0 is mandatory: without
                    it a long name refuses to shrink in this flex row and
                    pushes the whole drill-down layout wide. */}
                <div class="mb-3 flex items-center gap-3">
                  <Button class="!py-1 text-xs" onClick={() => setDrill(null)}>
                    Back to browse
                  </Button>
                  <h2 class="min-w-0 truncate text-lg font-semibold text-fg">
                    {d().name}
                  </h2>
                </div>
                {/* Row 2 — performer/studio bio banner, catalog-sourced, ABOVE
                    the existing scene grid and never a modal. Renders only when
                    the catalog has real text; an entity with none is a plain
                    drill-down exactly as before (no placeholder, no skeleton).
                    Tags are deliberately absent — no catalog exposes tags on a
                    performer or a studio, so there is nothing to bind to.

                    Claude 2026-08-02: items-start on the flex container.
                    Reason: the default align-items:stretch sets the art frame's
                    cross size and OVERRIDES aspect-ratio, so a studio logo's
                    frame geometry would vary with the bio's line count
                    (~144x81 next to one line, ~144x144 next to four).
                    items-start restores height:auto so aspect-ratio governs.
                    Troubleshooting: same stretch-row hazard EntityCard's frame
                    avoids by living in a non-flex tile.
                    Review if: the banner ever stops being a flex row. */}
                <Show when={entityBio()}>
                  <div class="mb-4 flex items-start gap-3 rounded-xl border border-border bg-surface p-4">
                    <div
                      class="shrink-0 overflow-hidden rounded-lg border border-border bg-surface-2"
                      classList={{
                        "w-20": d().kind === "performer",
                        "aspect-[2/3]": d().kind === "performer",
                        "w-36": d().kind === "studio",
                        "aspect-video": d().kind === "studio",
                      }}
                    >
                      <Show
                        when={proxyImage(d().image)}
                        fallback={<TextPoster label={d().name} />}
                      >
                        <img
                          src={proxyImage(d().image)}
                          alt={d().name}
                          loading="lazy"
                          class="h-full w-full"
                          classList={{
                            "object-cover": d().kind === "performer",
                            "object-contain": d().kind === "studio",
                          }}
                        />
                      </Show>
                    </div>
                    <div class="min-w-0 flex-1">
                      <p class="line-clamp-4 text-sm text-muted" title={entityBio()}>
                        {entityBio()}
                      </p>
                    </div>
                  </div>
                </Show>
                <PaginatedStrip
                  title="Scenes"
                  reloadToken={reloadToken}
                  load={(page) => {
                    // Capture the drill once so TypeScript narrows the union on
                    // the `"box" in dd` guard. FansDB variant ({box, id}) →
                    // box-scoped stash-box fetcher, unchanged, still paginated
                    // (its own singlePage behavior — none — is untouched here).
                    // Newest variant ({source: "newest"}) → the live
                    // entity-scenes handler by NAME (this pool has no stable
                    // box-scoped id — see AdultDrill's doc comment); its
                    // response is now a real {items, hasMore} envelope
                    // (page=1 pool-only, page=2 Prowlarr-triggered on an
                    // explicit "Show more" click), so it's paginated normally
                    // like every other PaginatedStrip caller — no singlePage.
                    const dd = d();
                    if ("box" in dd) {
                      return dd.kind === "studio"
                        ? fetchStashBoxStudioScenes(dd.box, dd.id, page)
                        : fetchStashBoxPerformerScenes(dd.box, dd.id, page);
                    }
                    return fetchNewestEntityScenes(dd.kind, dd.name, page);
                  }}
                  onError={setSetupError}
                  containerClass="flex flex-wrap gap-3"
                  // perPage=0 for the newest variant only (box variant keeps the
                  // default 20, untouched): shared.tsx's DE-2 auto-advance chain
                  // treats an envelope page shorter than perPage as "sparse,
                  // fetch another page automatically" — correct for the merged
                  // Studios/Performers rows it was built for (same source, just
                  // paginated further), but WRONG here, since page 2 of this
                  // drill isn't more of the same source, it's a live Prowlarr
                  // search that must fire ONLY on an explicit "Show more" click
                  // (CLAUDE.md's carve-out). target = props.perPage ?? 20; with
                  // perPage=0, `items gained < target` is never true (gained is
                  // never negative), so DE-2 never auto-fires page 2 on a
                  // sparse/empty pool page 1 — the operator's click is what
                  // triggers it, exactly once, matching the backend contract.
                  perPage={"box" in d() ? undefined : 0}
                >
                  {(item) => (
                    <AdultCard item={item} onDetail={setDetailTarget} />
                  )}
                </PaginatedStrip>
              </div>
            )}
          </Show>
        }
      >
        <section class="mt-2">
          <h2 class="mb-2 text-sm font-semibold uppercase tracking-wide text-muted">
            Search results
          </h2>
          <Show when={!results.loading} fallback={<Muted>Searching…</Muted>}>
            <Show
              when={(results()?.items?.length ?? 0) > 0}
              fallback={<Muted>No scenes found.</Muted>}
            >
              <div class="flex flex-wrap gap-3">
                <For each={results()?.items}>
                  {(s) => (
                    <AdultCard
                      item={s.scene}
                      onDetail={setDetailTarget}
                      onOpenReleases={() =>
                        setReleasePicker({
                          title: s.scene.title,
                          releases: s.releases,
                        })
                      }
                    />
                  )}
                </For>
              </div>
            </Show>
          </Show>
        </section>
      </Show>

      <Show when={releasePicker()}>
        {(rp) => (
          <SearchReleasePicker
            mode="adult"
            title={rp().title}
            releases={rp().releases}
            onClose={() => setReleasePicker(null)}
          />
        )}
      </Show>
      <Show when={detailTarget()}>
        {(t) => <DetailPopup target={t()} onClose={() => setDetailTarget(null)} />}
      </Show>
    </div>
  );
};
