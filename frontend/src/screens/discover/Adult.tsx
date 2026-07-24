// AdultDiscover — the scene-shaped browse and its cards: a search bar over
// the Prowlarr-matched "newest rows" (see fetchAdultNewestRows), a TPDB
// Studios row, and a TPDB Performers row, plus optional StashDB/FansDB
// scene/Studios/Performers rows shown only when that connection is
// configured (see the STASH_BOX_ROWS and configuredServices doc comments
// below). Searching swaps the rows for a plain result grid; clicking a TPDB
// Studio/Performer card drills down into a paginated grid of just that
// entity's scenes (the optional sources' Studios/Performers cards are
// non-interactive — no stash-box scenes-by-entity drill-down endpoint
// exists yet, see EntityCard). Extracted from the original single-file
// Discover.tsx.
//
// Row order (Optional RSS Discover rows + inline row editor): the browse
// row block is driven by a merged, operator-reorderable key list (see
// api/rowOrder.ts's mergeRowOrder) — newest rows/Studios/Performers/
// stash-box rows are reorderable but not removable here (they already have
// their own management surfaces elsewhere); admin-added RSS feed rows
// (target=adult) are both reorderable AND removable/toggleable inline. Only
// applies to the plain browse view — search results and a Studio/Performer
// drill-down are unaffected. editMode (from Discover/index.tsx's tab-bar
// Edit toggle) swaps the row list for RowEditor's UI; the "+ Add RSS feed"
// tile at the bottom is always visible regardless of edit mode.

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
  type AdultSortBy,
  type PerformerSummary,
  type StashBox,
  type StudioSummary,
  fetchAdultDiscover,
  fetchAdultDiscoverMergedRecent,
  fetchAdultDiscoverSorted,
  fetchAdultPerformerScenes,
  fetchAdultPerformers,
  fetchAdultStudioScenes,
  fetchAdultStudios,
  fetchStashBoxPerformers,
  fetchStashBoxScenes,
  fetchStashBoxStudios,
  proxyImage,
} from "../../api/discover";
import { type AdultSortValue, AdultSortBar } from "./FilterSortBar";
import { fetchConnections } from "../../api/settings";
import {
  type AdultNewestReleaseItem,
  fetchAdultNewestRowItems,
  fetchAdultNewestRows,
} from "../../api/adultNewestRows";
import { Button, ErrorText, Muted, yearOf } from "../../components/ui";
import {
  type GrabTarget,
  ConfigureConnectionModal,
  GrabDialog,
  PaginatedStrip,
  SelectCheckbox,
  TextPoster,
  notConfiguredService,
} from "./shared";
import { useSelection } from "./selection";
import { type DetailTarget, DetailPopup } from "./DetailPopup";
import {
  type RssFeed,
  deleteRssFeed,
  fetchRssFeeds,
  updateRssFeed,
} from "../../api/rssFeeds";
import { RssFeedRow } from "./RssFeedRows";
import { RowEditor, type RowDescriptor } from "./RowEditor";
import { AddRssFeedModal } from "./AddRssFeedModal";
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
// Clicking the card body (poster/title/subtitle — NOT the Grab button below,
// unchanged) opens DetailPopup via onDetail. The native title= tooltip
// (previously the scene's own title, not an overview — AdultDiscoverItem has
// no overview field at all) is replaced by a CSS-only hover overlay showing
// the same studio/date summary the subtitle line already displays — there is
// no description to show for a scene, so this isn't a richer version of the
// removed tooltip, just its replacement per the same convention PosterCard
// uses.
const AdultCard: Component<{
  item: AdultDiscoverItem;
  onGrab: (t: GrabTarget) => void;
  onDetail: (t: DetailTarget) => void;
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
    },
  });
  createEffect(() => {
    if (!selection || !inSelect()) return;
    const cleanup = selection.register(sceneKey(), sceneTarget());
    onCleanup(cleanup);
  });
  const checked = () => selection?.has(sceneKey()) ?? false;
  const grab = () => props.onGrab(sceneTarget());
  // In select-mode the card body toggles selection instead of opening the
  // DetailPopup; outside it, the click-to-open behavior is unchanged.
  const onBody = () => {
    if (inSelect()) {
      selection?.toggle(sceneKey());
      return;
    }
    props.onDetail({ mode: "adult", item: props.item });
  };
  return (
    <div class="w-[200px] shrink-0">
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
      <div class="mt-1.5">
        <Button class="w-full !py-1 text-xs" onClick={grab}>
          Grab
        </Button>
      </div>
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
// onSelect is OPTIONAL: TPDB's Studios/Performers rows pass it and the whole
// card renders as a button that drills down into that entity's scenes (via
// setDrill). StashDB/FansDB's Studios/Performers rows deliberately do NOT pass
// it — there is no stash-box scenes-by-entity drill-down endpoint, and TPDB's
// drill-down route expects a TPDB id, so wiring a stash-box id into setDrill
// would silently query the wrong catalog. Omitting onSelect renders a plain,
// non-interactive <div> tile instead — same art/name, no click behavior — so
// those rows stay visible (the backend QueryStudios/QueryPerformers browse
// still shows real data) without a broken or misleading drill-down.
const EntityCard: Component<{
  kind: "studio" | "performer";
  name: string;
  image: string;
  onSelect?: () => void;
}> = (props) => {
  const src = () => proxyImage(props.image);
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
      <div class="mt-1.5 truncate text-sm text-fg">{props.name}</div>
    </>
  );
  return (
    <Show
      when={props.onSelect}
      fallback={
        <div class="w-[200px] shrink-0 text-left" title={props.name}>
          {artwork()}
        </div>
      }
    >
      {(onSelect) => (
        <button
          type="button"
          class="w-[200px] shrink-0 text-left"
          title={props.name}
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
// rows/Studios/Performers/RSS feed rows, not as one per-box block.
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
  ],
);

// AdultDrill is the active drill-down target: which entity kind, its opaque TPDB
// id (passed verbatim to the drill-down endpoint), and its name for the header.
type AdultDrill = { kind: "studio" | "performer"; id: string; name: string };

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
  const [grabTarget, setGrabTarget] = createSignal<GrabTarget | null>(null);
  const [detailTarget, setDetailTarget] = createSignal<DetailTarget | null>(null);
  const [setupError, setSetupError] = createSignal<unknown>(null);
  const [dismissedSetup, setDismissedSetup] = createSignal(false);
  const [reloadToken, setReloadToken] = createSignal(0);

  const [draft, setDraft] = createSignal("");
  const [submitted, setSubmitted] = createSignal("");
  const searching = () => submitted().trim().length > 0;

  // drill is the active Studio/Performer drill-down (null = the browse rows).
  const [drill, setDrill] = createSignal<AdultDrill | null>(null);

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

  // newestRows are the operator-defined "newest" rows (Prowlarr-backed, matched
  // to TPDB/StashDB/FansDB entities by the background scan) — the confirmed
  // downloadable-right-now value-add this feature exists for, so they lead the
  // browse view. Fetched once on mount, already sortOrder-ascending from the
  // backend (Store.List's ordering), filtered to enabled here. The fetcher
  // swallows its own error (-> []) for the exact same reason connections above
  // does: an unguarded read of an errored resource re-throws on every render
  // and this app has no ErrorBoundary, which would crash the whole SPA instead
  // of just hiding these optional rows.
  const [newestRowsData] = createResource(async () => {
    try {
      return await fetchAdultNewestRows();
    } catch {
      return [];
    }
  });
  const newestRows = () =>
    (newestRowsData() ?? []).filter((r) => r.enabled);

  const [results] = createResource(
    () => (searching() ? submitted().trim() : null),
    async (q): Promise<AdultDiscoverItem[]> => {
      // A search error is surfaced the same way a row's is: handed to
      // setSetupError so a "tpdb isn't configured yet" failure raises the same
      // setup modal (the render's notConfiguredService gate decides modal vs.
      // plain error), instead of being swallowed into an empty "No scenes
      // found". One detection path for every Adult fetch, not two.
      try {
        return await fetchAdultDiscover(q);
      } catch (e) {
        setSetupError(e);
        return [];
      }
    },
  );

  const clearSearch = () => {
    setDraft("");
    setSubmitted("");
  };

  const configureFor = () => notConfiguredService(setupError());

  // --- Discover row order: newest rows/Studios/Performers/stash-box rows
  // (reorderable, not removable here) + admin-added RSS feed rows
  // (reorderable AND removable/toggleable), fully interleavable via Edit
  // mode (RowEditor). Only applies to the plain browse view. ---
  const [feedsData] = createResource(reloadToken, () =>
    fetchRssFeeds().catch(() => [] as RssFeed[]),
  );
  const adultFeeds = () => (feedsData() ?? []).filter((f) => f.target === "adult");
  const enabledAdultFeeds = () => adultFeeds().filter((f) => f.enabled);

  const stashBoxKnownRows = () =>
    STASH_BOX_ORDERABLE_ROWS.filter((r) => configuredServices().has(r.box));

  // knownKeys is every row this screen currently knows about. Default order
  // (an empty stored order, e.g. a fresh install) matches the page's
  // original hardcoded row sequence exactly: newest rows, Studios,
  // Performers, then any configured stash-box rows, then RSS feeds.
  const knownKeys = () => [
    ...newestRows().map((r) => `newestrow:${r.id}`),
    "studios",
    "performers",
    ...stashBoxKnownRows().map((r) => r.key),
    ...adultFeeds().map((f) => `rssfeed:${f.id}`),
  ];

  const { orderedKeys, moveRow, persistOrder, error: rowOrderError } =
    useRowOrder("adult", knownKeys);
  // rowActionError covers a toggle/delete's own mutation failure
  // (updateRssFeed/deleteRssFeed) — a distinct failure mode from
  // useRowOrder's error (a saveRowOrder persist failure) but shown in the
  // same spot; editError combines them so RowEditor's error line doesn't
  // need two <Show> blocks.
  const [rowActionError, setRowActionError] = createSignal("");
  const editError = () => rowOrderError() || rowActionError();

  const descriptorFor = (key: string): RowDescriptor | undefined => {
    if (key === "studios") return { key, label: "Studios", removable: false };
    if (key === "performers") return { key, label: "Performers", removable: false };
    if (key.startsWith("newestrow:")) {
      const id = Number(key.slice("newestrow:".length));
      const row = newestRows().find((r) => r.id === id);
      return row ? { key, label: row.title, removable: false } : undefined;
    }
    if (key.startsWith("stashbox:")) {
      const row = STASH_BOX_ORDERABLE_ROWS.find((r) => r.key === key);
      return row ? { key, label: row.label, removable: false } : undefined;
    }
    if (key.startsWith("rssfeed:")) {
      const id = Number(key.slice("rssfeed:".length));
      const f = adultFeeds().find((f) => f.id === id);
      return f ? { key, label: f.title, removable: true, enabled: f.enabled } : undefined;
    }
    return undefined;
  };

  const rowDescriptors = (): RowDescriptor[] =>
    orderedKeys()
      .map(descriptorFor)
      .filter((d): d is RowDescriptor => d !== undefined);

  const toggleRowEnabled = async (row: RowDescriptor) => {
    if (!row.key.startsWith("rssfeed:")) return;
    try {
      const f = adultFeeds().find((f) => `rssfeed:${f.id}` === row.key);
      if (!f) return;
      await updateRssFeed(f.id, {
        title: f.title,
        feedUrl: f.feedUrl,
        target: f.target,
        protocol: f.protocol,
        enabled: !f.enabled,
      });
      setReloadToken((n) => n + 1);
    } catch (e) {
      setRowActionError((e as Error).message);
    }
  };

  const deleteRow = async (row: RowDescriptor) => {
    if (!row.key.startsWith("rssfeed:")) return;
    if (!confirm(`Delete "${row.label}"?`)) return;
    try {
      await deleteRssFeed(Number(row.key.slice("rssfeed:".length)));
      persistOrder(orderedKeys().filter((k) => k !== row.key));
      setReloadToken((n) => n + 1);
    } catch (e) {
      setRowActionError((e as Error).message);
    }
  };

  const visibleKeys = () =>
    orderedKeys().filter((key) => {
      if (key.startsWith("rssfeed:")) {
        return enabledAdultFeeds().some((f) => `rssfeed:${f.id}` === key);
      }
      return true;
    });

  const renderRow = (key: string): JSX.Element => {
    if (key === "studios") {
      return (
        <PaginatedStrip<StudioSummary>
          title="Studios"
          reloadToken={reloadToken}
          load={(page) => fetchAdultStudios(page)}
          onError={setSetupError}
        >
          {(s) => (
            <EntityCard
              kind="studio"
              name={s.name}
              image={s.image}
              onSelect={() => setDrill({ kind: "studio", id: s.id, name: s.name })}
            />
          )}
        </PaginatedStrip>
      );
    }
    if (key === "performers") {
      return (
        <PaginatedStrip<PerformerSummary>
          title="Performers"
          reloadToken={reloadToken}
          load={(page) => fetchAdultPerformers(page)}
          onError={setSetupError}
        >
          {(p) => (
            <EntityCard
              kind="performer"
              name={p.name}
              image={p.image}
              onSelect={() => setDrill({ kind: "performer", id: p.id, name: p.name })}
            />
          )}
        </PaginatedStrip>
      );
    }
    if (key.startsWith("newestrow:")) {
      const id = Number(key.slice("newestrow:".length));
      const row = newestRows().find((r) => r.id === id)!;
      return (
        <PaginatedStrip<AdultNewestReleaseItem>
          title={row.title}
          reloadToken={reloadToken}
          load={(page) => fetchAdultNewestRowItems(row.id, page)}
          onError={setSetupError}
        >
          {(item) =>
            row.rowType === "movie" || row.rowType === "scene" ? (
              <AdultCard
                item={toAdultDiscoverItem(item)}
                onGrab={setGrabTarget}
                onDetail={setDetailTarget}
              />
            ) : (
              <EntityCard
                kind={row.rowType === "studio" ? "studio" : "performer"}
                name={item.title}
                image={item.image}
              />
            )
          }
        </PaginatedStrip>
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
              <AdultCard item={item} onGrab={setGrabTarget} onDetail={setDetailTarget} />
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
            {(s) => <EntityCard kind="studio" name={s.name} image={s.image} />}
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
          {(p) => <EntityCard kind="performer" name={p.name} image={p.image} />}
        </PaginatedStrip>
      );
    }
    const feed = enabledAdultFeeds().find((f) => `rssfeed:${f.id}` === key)!;
    return <RssFeedRow feed={feed} reloadToken={reloadToken} onError={setSetupError} />;
  };

  const [addFeedOpen, setAddFeedOpen] = createSignal(false);

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
                    onMove={moveRow}
                    onToggleEnabled={(r) => void toggleRowEnabled(r)}
                    onDelete={(r) => void deleteRow(r)}
                  />
                  <Show when={editError()}>
                    <ErrorText>{editError()}</ErrorText>
                  </Show>
                </Show>
                <For each={visibleKeys()}>{(key) => renderRow(key)}</For>
                <div class="mt-6 flex justify-center">
                  <Button onClick={() => setAddFeedOpen(true)}>
                    + Add RSS feed
                  </Button>
                </div>
                <Show when={addFeedOpen()}>
                  <AddRssFeedModal
                    allowedTargets={["adult"]}
                    defaultTarget="adult"
                    onClose={() => setAddFeedOpen(false)}
                    onSaved={() => {
                      setAddFeedOpen(false);
                      setReloadToken((n) => n + 1);
                    }}
                  />
                </Show>
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
                    <AdultCard
                      item={item}
                      onGrab={setGrabTarget}
                      onDetail={setDetailTarget}
                    />
                  )}
                </PaginatedStrip>
              </Show>
            }
          >
            {(d) => (
              <div>
                <div class="mb-2 flex items-center gap-3">
                  <Button class="!py-1 text-xs" onClick={() => setDrill(null)}>
                    Back to browse
                  </Button>
                </div>
                <PaginatedStrip
                  title={d().name}
                  reloadToken={reloadToken}
                  load={(page) =>
                    d().kind === "studio"
                      ? fetchAdultStudioScenes(d().id, page)
                      : fetchAdultPerformerScenes(d().id, page)
                  }
                  onError={setSetupError}
                  containerClass="flex flex-wrap gap-3"
                >
                  {(item) => (
                    <AdultCard
                      item={item}
                      onGrab={setGrabTarget}
                      onDetail={setDetailTarget}
                    />
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
              when={(results()?.length ?? 0) > 0}
              fallback={<Muted>No scenes found.</Muted>}
            >
              <div class="flex flex-wrap gap-3">
                <For each={results()}>
                  {(item) => (
                    <AdultCard
                      item={item}
                      onGrab={setGrabTarget}
                      onDetail={setDetailTarget}
                    />
                  )}
                </For>
              </div>
            </Show>
          </Show>
        </section>
      </Show>

      <Show when={grabTarget()}>
        {(t) => <GrabDialog target={t()} onClose={() => setGrabTarget(null)} />}
      </Show>
      <Show when={detailTarget()}>
        {(t) => <DetailPopup target={t()} onClose={() => setDetailTarget(null)} />}
      </Show>
    </div>
  );
};
