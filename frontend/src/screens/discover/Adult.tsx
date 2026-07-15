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

import {
  type Component,
  createResource,
  createSignal,
  For,
  Show,
} from "solid-js";
import {
  type AdultDiscoverItem,
  type PerformerSummary,
  type StashBox,
  type StudioSummary,
  fetchAdultDiscover,
  fetchAdultPerformerScenes,
  fetchAdultPerformers,
  fetchAdultStudioScenes,
  fetchAdultStudios,
  fetchStashBoxPerformers,
  fetchStashBoxScenes,
  fetchStashBoxStudios,
  proxyImage,
} from "../../api/discover";
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
  TextPoster,
  notConfiguredService,
} from "./shared";
import { type DetailTarget, DetailPopup } from "./DetailPopup";

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
// doesn't carry (rating/slug; durationSeconds IS real now, threaded through
// from identify.MatchResult.RuntimeSeconds — see adultnewest.MatchedRelease.
// EntityDurationSeconds's doc comment for the live bug this fixes: a
// hardcoded 0 here silently failed to auto-qualify anything against Adult's
// bitrate-quality-floor scorer, since that scorer never re-fetches a real
// runtime the way Movies/Series do). rating: 0 renders no ★; slug: "" makes
// DetailPopup's TPDB external link fall through to undefined rather than a
// guaranteed-broken URL.
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
  const src = () => proxyImage(props.item.image);
  const subtitle = () =>
    [props.item.studio, yearOf(props.item.date), sourceLabel(props.item.source)]
      .filter(Boolean)
      .join(" · ");
  const grab = () =>
    props.onGrab({
      mode: "adult",
      label: props.item.title,
      request: {
        title: props.item.title,
        studio: props.item.studio,
        durationSeconds: props.item.durationSeconds,
      },
    });
  return (
    <div class="w-[200px] shrink-0">
      <div
        class="group cursor-pointer"
        onClick={() => props.onDetail({ mode: "adult", item: props.item })}
      >
        <div class="relative aspect-video overflow-hidden rounded-lg border border-border bg-surface">
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
  name: string;
  image: string;
  onSelect?: () => void;
}> = (props) => {
  const src = () => proxyImage(props.image);
  const artwork = () => (
    <>
      <div class="aspect-video overflow-hidden rounded-lg border border-border bg-surface">
        <Show when={src()} fallback={<TextPoster label={props.name} />}>
          <img
            src={src()}
            alt={props.name}
            loading="lazy"
            class="h-full w-full object-cover"
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
export const AdultDiscover: Component = () => {
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

  return (
    <div>
      <form
        class="mb-4 flex gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          // A new search takes precedence over any drill-down (Clear returns to
          // the rows, not back into a stale drill).
          setDrill(null);
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
              <>
                {/* Operator-defined "newest" rows — Prowlarr-backed matches
                    cached by the background scan, shown first as the
                    confirmed-downloadable value-add. movie/scene rows render
                    grab-able AdultCards (via the toAdultDiscoverItem adapter);
                    performer/studio rows render non-interactive EntityCards
                    (no drill-down endpoint for THIS pipeline's matched
                    entities — same reason STASH_BOX_ROWS omit onSelect). */}
                <For each={newestRows()}>
                  {(row) => (
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
                          <EntityCard name={item.title} image={item.image} />
                        )
                      }
                    </PaginatedStrip>
                  )}
                </For>
                <PaginatedStrip<StudioSummary>
                  title="Studios"
                  reloadToken={reloadToken}
                  load={(page) => fetchAdultStudios(page)}
                  onError={setSetupError}
                >
                  {(s) => (
                    <EntityCard
                      name={s.name}
                      image={s.image}
                      onSelect={() =>
                        setDrill({ kind: "studio", id: s.id, name: s.name })
                      }
                    />
                  )}
                </PaginatedStrip>
                <PaginatedStrip<PerformerSummary>
                  title="Performers"
                  reloadToken={reloadToken}
                  load={(page) => fetchAdultPerformers(page)}
                  onError={setSetupError}
                >
                  {(p) => (
                    <EntityCard
                      name={p.name}
                      image={p.image}
                      onSelect={() =>
                        setDrill({ kind: "performer", id: p.id, name: p.name })
                      }
                    />
                  )}
                </PaginatedStrip>

                {/* Optional stash-box sources — rendered only when
                    configured (no "Nothing here yet" placeholder otherwise).
                    Studios/Performers cards below deliberately omit onSelect
                    (see EntityCard's doc comment): there is no stash-box
                    scenes-by-entity drill-down endpoint, so these render as
                    plain, non-interactive tiles. */}
                <For each={STASH_BOX_ROWS}>
                  {(row) => (
                    <Show when={configuredServices().has(row.box)}>
                      <For each={row.sceneRows}>
                        {(sceneRow) => (
                          <PaginatedStrip
                            title={sceneRow.title}
                            reloadToken={reloadToken}
                            load={(page) =>
                              fetchStashBoxScenes(row.box, sceneRow.kind, page)
                            }
                            onError={setSetupError}
                          >
                            {(item) => (
                              <AdultCard
                                item={item}
                                onGrab={setGrabTarget}
                                onDetail={setDetailTarget}
                              />
                            )}
                          </PaginatedStrip>
                        )}
                      </For>
                      <PaginatedStrip<StudioSummary>
                        title={`${row.label} Studios`}
                        reloadToken={reloadToken}
                        load={(page) => fetchStashBoxStudios(row.box, page)}
                        onError={setSetupError}
                      >
                        {(s) => <EntityCard name={s.name} image={s.image} />}
                      </PaginatedStrip>
                      <PaginatedStrip<PerformerSummary>
                        title={`${row.label} Performers`}
                        reloadToken={reloadToken}
                        load={(page) => fetchStashBoxPerformers(row.box, page)}
                        onError={setSetupError}
                      >
                        {(p) => <EntityCard name={p.name} image={p.image} />}
                      </PaginatedStrip>
                    </Show>
                  )}
                </For>
              </>
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
