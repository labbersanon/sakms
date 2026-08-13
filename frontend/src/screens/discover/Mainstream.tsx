// MainstreamDiscover — the combined Movies+Series page and its cards: a search
// bar over four stacked, independently-paginated TMDB category rows plus a
// paginated "In your library" row of what's already tracked. Every card grabs
// the same way as of 2026-08-02: its body click opens DetailPopup, which for
// Series shows the season/episode picker first (the gating step, since no
// release exists to score until a specific episode/pack is chosen). The inline
// per-card Grab button these cards used to carry is gone.
// Extracted from the original single-file Discover.tsx.
//
// Row order (Optional RSS Discover rows + inline row editor): the row block
// below is no longer a fixed hardcoded sequence — it's driven by a merged,
// operator-reorderable key list (see api/rowOrder.ts's mergeRowOrder) that
// interleaves the built-in rows above with admin-defined custom sliders and
// RSS feed rows. `editMode` (passed down from Discover/index.tsx's tab-bar
// Edit toggle) swaps the row list for RowEditor's drag-reorder UI: built-in
// (structural) rows get a Show/Hide toggle, dynamic (entity) sliders/RSS feeds
// get an enable-toggle + Delete. The "+ Add RSS feed" tile at the bottom is
// always visible regardless of edit mode.

import {
  type Component,
  type JSX,
  createEffect,
  createResource,
  createSignal,
  on,
  onCleanup,
  For,
  Show,
} from "solid-js";
import {
  type DiscoverItem,
  type DiscoverCategory,
  type DiscoverFilterParams,
  fetchDiscover,
  fetchDiscoverFiltered,
  fetchTitlePoster,
  fetchTmdbSearch,
  tmdbPoster,
} from "../../api/discover";
import { type TrackedItem, fetchTrackedItems } from "../../api/tag";
import { Button, ErrorText, Muted, yearOf } from "../../components/ui";
import {
  type GrabTarget,
  ConfigureConnectionModal,
  GrabDialog,
  Modal,
  PaginatedStrip,
  SearchReleasePicker,
  SelectCheckbox,
  TextPoster,
  notConfiguredService,
} from "./shared";
import { type EpisodePick, SeasonEpisodePicker } from "./SeasonEpisodePicker";
import { useSelection } from "./selection";
import {
  type MainstreamFilters,
  type MainstreamContentType,
  DEFAULT_MAINSTREAM_FILTERS,
  MainstreamFilterSortBar,
  isMainstreamFilterActive,
} from "./FilterSortBar";
import { Carousel } from "../../components/Carousel";
import {
  type Slider,
  deleteSlider,
  fetchDiscoverSliders,
  fetchSliderItems,
  updateSlider,
} from "../../api/discoverSliders";
import {
  type RssFeed,
  deleteRssFeed,
  fetchRssFeeds,
  updateRssFeed,
} from "../../api/rssFeeds";
import { TraktWatchlistRow } from "../../components/TraktWatchlistRow";
import { type DetailTarget, DetailPopup } from "./DetailPopup";
import { RssFeedRow } from "./RssFeedRows";
import { RowEditor, type RowDescriptor } from "./RowEditor";
import { AddRssFeedModal } from "./AddRssFeedModal";
import { useRowOrder } from "./useRowOrder";
import { CalendarView } from "./CalendarView";

// MainstreamView selects the page's top-level presentation: the default stacked
// carousel/search/filter "rows" view, or the F2 month "calendar" view. Kept a
// simple string union (not a tab set) since it's a binary in-page toggle, not a
// registered ScreenTabs surface.
type MainstreamView = "rows" | "calendar";

// ModedTitle is the mode a merged card belongs to — the per-item mode a
// combined (movies+series) row/grid MUST carry so each card grabs via its own
// path: a Series card first opens the season/episode picker, a Movies card
// grabs directly. Passing one fixed mode across a mixed row would silently
// route a series through the movie grab path, breaking auto-grab.
type ModedTitle = { mode: "movies" | "series"; item: DiscoverItem };

// MAINSTREAM_ROWS is the fixed set of TMDB category rows the Mainstream page
// stacks: both modes × both categories. Each row paginates independently.
// `key` is this row's stable identity in the Discover row-order feature (see
// RowEditor.tsx/api/rowOrder.ts's mergeRowOrder) — never renamed once
// shipped, since a stored row-order value references it by this string.
const MAINSTREAM_ROWS: {
  key: string;
  title: string;
  mode: "movies" | "series";
  category: DiscoverCategory;
}[] = [
  { key: "trending-movies", title: "Trending Movies", mode: "movies", category: "trending" },
  { key: "trending-shows", title: "Trending Shows", mode: "series", category: "trending" },
  { key: "popular-movies", title: "Popular Movies", mode: "movies", category: "popular" },
  { key: "popular-shows", title: "Popular Shows", mode: "series", category: "popular" },
  { key: "upcoming-movies", title: "Upcoming Movies", mode: "movies", category: "upcoming" },
  { key: "upcoming-shows", title: "Upcoming Shows", mode: "series", category: "upcoming" },
];

// MAINSTREAM_FIXED_LABELS covers the two built-in rows that aren't part of
// MAINSTREAM_ROWS (the Trakt watchlist row and the existing-library row) —
// used by descriptorFor to label them in RowEditor.
const MAINSTREAM_FIXED_LABELS: Record<string, string> = {
  "trakt-watchlist": "Trakt Watchlist",
  library: "In your library",
};

// GrabButton is the per-title grab affordance. Movies grab on click. Series
// first open the season/episode picker (the gating step) and only build a
// GrabTarget once a season or episode is picked.
//
// The picker opens in a Modal rather than inline: it is a two-level poster grid
// (SeasonEpisodePicker.tsx) and a grid does not fit a card's narrow column
// (TraktWatchlistRow's cards, the only ones left rendering this, are w-36).
// No `seasons` prop is passed — OMITTING it is what puts the picker in
// self-fetching mode, which is what these card call sites want, since a card
// holds only a tmdbId and never the detail bundle. Passing `seasons` at all,
// even as undefined, would permanently hand loading control to this caller.
// (DetailPopup is the one site that DOES pass it — it already has the data.)
//
// Claude 2026-08-02: this component used to be the picker's mount point for
// five of the six surfaces (the category rows' PosterCard, the "In your
// library" LibraryCard, the Trakt watchlist row, the "More like this" rail and
// CalendarView). All of those lost their inline Grab button in the Discover
// card cleanup, so TraktWatchlistRow.tsx is now its SOLE consumer — and the
// only card-level auto-grab affordance left in Discover.
// Reason: kept (not deleted) because Trakt is out of that cleanup's scope; the
// card-body click → DetailPopup replaced this everywhere else.
// Review if: TraktWatchlistRow stops rendering it — then this whole component
// is dead and should go with it.
export const GrabButton: Component<{
  mode: "movies" | "series";
  item: DiscoverItem;
  onGrab: (t: GrabTarget) => void;
}> = (props) => {
  const [picking, setPicking] = createSignal(false);

  const grabMovie = () =>
    props.onGrab({
      mode: "movies",
      label: props.item.title,
      request: { title: props.item.title, tmdbId: props.item.id },
    });

  // grabSeries is UNCHANGED from the free-text era, seasonSpecified: true
  // included — only the surface that produces (season, episode) was replaced.
  // setPicking(false) must stay FIRST: it unmounts the picker Modal before the
  // GrabDialog Modal opens, and two stacked modals would make every Close/title
  // query in this tree ambiguous.
  const grabSeries = (season: number, episode: number) => {
    setPicking(false);
    const suffix = `S${season}${episode ? "E" + episode : ""}`;
    props.onGrab({
      mode: "series",
      label: `${props.item.title} ${suffix}`,
      request: {
        title: props.item.title,
        tmdbId: props.item.id,
        seasonNumber: season,
        episodeNumber: episode,
        seasonSpecified: true,
      },
    });
  };

  return (
    <Show
      when={props.mode === "series"}
      fallback={
        <Button class="w-full !py-1 text-xs" onClick={grabMovie}>
          Grab
        </Button>
      }
    >
      <Button class="w-full !py-1 text-xs" onClick={() => setPicking(true)}>
        Grab
      </Button>
      <Show when={picking()}>
        <Modal title={props.item.title} onClose={() => setPicking(false)}>
          <SeasonEpisodePicker
            tmdbId={props.item.id}
            selectionMode="single"
            onSubmit={grabSeries}
          />
        </Modal>
      </Show>
    </Show>
  );
};

// PosterCard is one Movies/Series title. Fixed width so a row scrolls
// horizontally. Clicking the card body (poster/title/meta) opens DetailPopup
// via onDetail — as of 2026-08-02 that is this card's ONLY grab path; the
// inline one-click Grab button it used to carry below the meta line was
// removed (see the slot below). The native title= overview
// tooltip is replaced by a CSS-only (group/group-hover) hover overlay over
// the poster — same information, richer presentation, no new Solid signal.
// Exported (was module-private) so DetailPopup's "More like this" recommendation
// rail and CalendarView reuse the identical card rather than each hand-rolling a
// parallel one-off (which would also miss the later F3 select-mode checkbox this
// card will gain).
// SeriesSeasonSelect is the Series-card select-mode UI: a "Choose seasons/
// episodes" button opening the season/episode grid picker in multi mode, plus a
// compact chip list of everything the operator has surfaced on this card.
// Selection is season-level OR episode-level (`S4` / `S4E7`); selecting a whole
// series at once remains explicitly OUT of v1 scope.
//
// REGISTRATION LIVES ON THE CHIP, NEVER ON THE MODAL'S TILE. buildBatch submits
// only keys still register()ed by a currently-rendered component; a modal closes
// and unmounts its tiles, so registering there would silently orphan-drop every
// selection at submit time while every other test stayed green. The chips are
// what stay on screen, so they are what register.
const SeriesSeasonSelect: Component<{ item: DiscoverItem }> = (props) => {
  const selection = useSelection();
  const [picking, setPicking] = createSignal(false);

  const keyFor = (p: EpisodePick) =>
    `series:${props.item.id}:S${p.season}${p.episode ? `E${p.episode}` : ""}`;
  const labelFor = (p: EpisodePick) =>
    p.episode ? `S${p.season}E${p.episode}` : `Season ${p.season}`;
  const targetFor = (p: EpisodePick): GrabTarget => ({
    mode: "series",
    label: `${props.item.title} S${p.season}${p.episode ? `E${p.episode}` : ""}`,
    request: {
      title: props.item.title,
      tmdbId: props.item.id,
      seasonNumber: p.season,
      // An episode pick carries its episode number so the batch dispatches a
      // single episode; a whole-season pick omits it, exactly as before.
      ...(p.episode ? { episodeNumber: p.episode } : {}),
      seasonSpecified: true,
    },
  });

  // picks is what this card has SURFACED, which is deliberately not the same as
  // what is selected: a chip persists once added and flips ✓/+ as it is toggled,
  // so re-checking something never means reopening the picker. It can also go
  // unchecked without this card doing anything, when another card's mutually-
  // exclusive pick clears it (see toggle below).
  const [picks, setPicks] = createSignal<EpisodePick[]>([]);
  const surface = (p: EpisodePick) =>
    setPicks((prev) =>
      prev.some((q) => q.season === p.season && q.episode === p.episode)
        ? prev
        : [...prev, p],
    );

  // toggle is where the whole-season/episode mutual exclusion is enforced —
  // dispatching `S4` alongside `S4E7` would grab a season pack plus a duplicate
  // single episode of that same content, which nothing downstream catches (the
  // two are different releases, so the download-client GID dedup sees no
  // conflict) and which lands a duplicate on disk.
  //
  // It runs against the STORE's full key set, not this component's local picks,
  // and that is load-bearing: the same title can render in two rows at once
  // (Trending AND Popular), giving two SeriesSeasonSelect instances independent
  // local state over one shared selection. A card-local check would let a season
  // picked here and an episode of it picked there both survive into buildBatch.
  const toggle = (p: EpisodePick) => {
    if (!selection) return;
    const key = keyFor(p);
    if (selection.has(key)) {
      selection.toggle(key);
      return;
    }
    if (p.episode === 0) {
      // A whole season clears every episode of that season, on any card.
      const prefix = `series:${props.item.id}:S${p.season}E`;
      for (const k of selection.keys()) {
        if (k.startsWith(prefix)) selection.toggle(k);
      }
    } else {
      // An episode clears its own season's whole-season key, on any card.
      const seasonKey = `series:${props.item.id}:S${p.season}`;
      if (selection.has(seasonKey)) selection.toggle(seasonKey);
    }
    selection.toggle(key);
  };

  // pick is what a picker tile activation runs: surface a chip for it (so it
  // has somewhere to register from) and toggle it on.
  const pick = (p: EpisodePick) => {
    surface(p);
    toggle(p);
  };

  return (
    <div class="mt-1.5">
      <For each={picks()}>
        {(p) => {
          // Register this pick's live target while its chip is mounted; the
          // cleanup deregisters it on unmount, so a no-longer-shown pick becomes
          // an orphan and is dropped at submit time rather than grabbed.
          createEffect(() => {
            if (!selection) return;
            const cleanup = selection.register(keyFor(p), targetFor(p));
            onCleanup(cleanup);
          });
          const checked = () => selection?.has(keyFor(p)) ?? false;
          return (
            <button
              type="button"
              class="mb-1 mr-1 inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-xs"
              classList={{
                "border-accent bg-accent text-accent-fg": checked(),
                "border-border text-muted": !checked(),
              }}
              onClick={() => toggle(p)}
            >
              <span>{checked() ? "✓" : "+"}</span> {labelFor(p)}
            </button>
          );
        }}
      </For>
      <Button class="w-full !py-1 text-xs" onClick={() => setPicking(true)}>
        Choose seasons/episodes
      </Button>
      <Show when={picking()}>
        <Modal title={props.item.title} onClose={() => setPicking(false)}>
          <SeasonEpisodePicker
            tmdbId={props.item.id}
            selectionMode="multi"
            isSelected={(p) => selection?.has(keyFor(p)) ?? false}
            onToggle={pick}
          />
        </Modal>
      </Show>
    </div>
  );
};

export const PosterCard: Component<{
  mode: "movies" | "series";
  item: DiscoverItem;
  // onGrab is no longer read by this card (its inline Grab button was removed
  // 2026-08-02) but stays on the prop contract: unwinding it fans out to six
  // files and eight call sites for no behavioral gain. See
  // .omc/plans/autopilot-impl-discover-card-cleanup.md §0.5. Made optional
  // (2026-08-02) so callers no longer have to invent a no-op for a prop nothing
  // reads.
  // Review if: this slot is still empty at the next Discover cleanup pass.
  onGrab?: (t: GrabTarget) => void;
  onDetail: (t: DetailTarget) => void;
  // onOpenReleases, when passed (only by the catalog-Search result grid),
  // reroutes the card's primary click to open the release picker instead of
  // DetailPopup. It used to ALSO suppress the one-click Grab button (M3 —
  // searched cards have no auto-grab shortcut); that half is moot since
  // 2026-08-02, when no card renders one. Browse-row call sites omit it, so
  // their click→DetailPopup behavior is unchanged.
  onOpenReleases?: () => void;
}> = (props) => {
  const selection = useSelection();
  const inSelect = () => selection?.selectMode() ?? false;
  const src = () => tmdbPoster(props.item.posterPath);
  const movieKey = () => `movies:${props.item.id}`;
  const movieTarget = (): GrabTarget => ({
    mode: "movies",
    label: props.item.title,
    request: { title: props.item.title, tmdbId: props.item.id },
  });
  // A Movie is one selectable item and register()s directly while in
  // select-mode; a Series registers per-season inside SeriesSeasonSelect
  // (a whole series is never one selectable item — see that component).
  createEffect(() => {
    if (!selection || !inSelect() || props.mode !== "movies") return;
    const cleanup = selection.register(movieKey(), movieTarget());
    onCleanup(cleanup);
  });
  const movieChecked = () =>
    props.mode === "movies" && (selection?.has(movieKey()) ?? false);
  // In select-mode the card BODY toggles selection instead of opening the
  // DetailPopup (movies only; a series toggles via its season chips). Outside
  // select-mode the click-to-open-popup behavior is unchanged.
  const onBody = () => {
    if (inSelect()) {
      if (props.mode === "movies") selection?.toggle(movieKey());
      return;
    }
    if (props.onOpenReleases) {
      props.onOpenReleases();
      return;
    }
    props.onDetail({ mode: props.mode, item: props.item });
  };
  return (
    <div class="w-[220px] shrink-0">
      <div class="group cursor-pointer" onClick={onBody}>
        <div class="relative aspect-[2/3] overflow-hidden rounded-lg border border-border bg-surface">
          <Show when={inSelect() && props.mode === "movies"}>
            <SelectCheckbox checked={movieChecked()} />
          </Show>
          <Show when={src()} fallback={<TextPoster label={props.item.title} />}>
            <img
              src={src()}
              alt={props.item.title}
              loading="lazy"
              class="h-full w-full object-cover"
            />
          </Show>
          <Show when={props.item.overview}>
            <div class="absolute inset-0 flex items-end bg-black/70 p-2 opacity-0 transition-opacity group-hover:opacity-100">
              <p class="line-clamp-5 text-xs text-white">{props.item.overview}</p>
            </div>
          </Show>
        </div>
        <div class="mt-1.5 truncate text-sm text-fg" title={props.item.title}>
          {props.item.title}
        </div>
        <div class="flex items-center gap-2 text-xs text-muted">
          <span>{yearOf(props.item.releaseDate) ?? "—"}</span>
          <Show when={props.item.voteAverage > 0}>
            <span>★ {props.item.voteAverage.toFixed(1)}</span>
          </Show>
        </div>
      </div>
      {/* Claude 2026-08-02: the inline Grab button was removed from this slot;
          the card body's click → DetailPopup is now the grab path for Movies
          and Series alike. Select-mode's season/episode chips are the ONLY
          thing this slot still renders.
          Reason: the outer `!onOpenReleases || inSelect()` guard existed solely
          to suppress that button on catalog-Search cards (M3), and the
          `insideModal && series` guard solely to stop the button's picker Modal
          nesting inside DetailPopup's. With the button gone both are vacuous —
          `inSelect() && series` already implies the first, and the second's
          branch is unreachable because every card body that opens DetailPopup
          early-returns while select-mode is on, so inSelect() is false inside
          that modal. The collapsed form is equivalent GIVEN that invariant —
          select-mode is never active while DetailPopup is open (see CLAUDE.md's
          CORRECTED 2026-08-02 (later, same day) note under Staged-for-approval
          for the full two-directional statement) — not "logically identical"
          as a bare mathematical claim: the old condition also carried an
          `insideModal`-specific clause the new one doesn't, it is just dead
          under that invariant.
          Review if: a non-select-mode affordance is ever added back to this
          slot, or a card body ever opens DetailPopup without the select-mode
          early return (that would re-create the nested-modal hazard the
          removed `insideModal` prop guarded). */}
      <Show when={inSelect() && props.mode === "series"}>
        <SeriesSeasonSelect item={props.item} />
      </Show>
    </div>
  );
};

// PaginatedRow is one TMDB category strip (fixed mode + category) with a
// "Show more" that APPENDS the next TMDB page rather than replacing the row —
// the accumulator (items) only ever grows. It reloads from page 1 whenever
// reloadToken changes (the setup-modal "I just configured TMDB, refetch"
// signal). Fetch errors are reported up via onError so the parent can raise
// the not-configured setup modal once for the whole page, not per row.
const PaginatedRow: Component<{
  title: string;
  mode: "movies" | "series";
  category: DiscoverCategory;
  reloadToken: () => number;
  onGrab: (t: GrabTarget) => void;
  onDetail: (t: DetailTarget) => void;
  onError: (err: unknown) => void;
}> = (props) => {
  const [items, setItems] = createSignal<DiscoverItem[]>([]);
  const [page, setPage] = createSignal(0);
  const [loading, setLoading] = createSignal(false);
  const [exhausted, setExhausted] = createSignal(false);

  const load = async (reset: boolean) => {
    const next = reset ? 1 : page() + 1;
    setLoading(true);
    try {
      const batch = await fetchDiscover(props.mode, props.category, next);
      setItems((prev) => (reset ? batch : [...prev, ...batch]));
      setPage(next);
      if (batch.length === 0) setExhausted(true);
    } catch (e) {
      props.onError(e);
    } finally {
      setLoading(false);
    }
  };

  // Initial load AND reload-on-token in one effect (on() runs immediately by
  // default, so no separate onMount is needed).
  createEffect(
    on(props.reloadToken, () => {
      setItems([]);
      setPage(0);
      setExhausted(false);
      void load(true);
    }),
  );

  return (
    <Carousel
      title={props.title}
      items={items()}
      renderItem={(item) => (
        <PosterCard
          mode={props.mode}
          item={item}
          onGrab={props.onGrab}
          onDetail={props.onDetail}
        />
      )}
      onLoadMore={() => void load(false)}
      hasMore={!exhausted()}
      loading={loading()}
    />
  );
};

// LibraryCard is one owned-library title on the existing-library row. Its mode
// is per-item (the row mixes movies+series), which drives both the lazy poster
// fetch and which mode DetailPopup opens in. The library caches no poster art, so the
// poster is resolved on demand by tmdbId (fetchTitlePoster) — one bounded call
// per rendered card, then routed through the image proxy exactly like every
// other card. A synthetic DiscoverItem (id = tmdbId) feeds DetailPopup so a
// library card grabs through the identical popup path a Discover card does —
// Series still gets its season/episode picker, now as the popup's inline
// gating step rather than a modal off an inline Grab button (removed
// 2026-08-02). The card is click-inert when tmdbId is 0 — see openDetail.
const LibraryCard: Component<{
  mode: "movies" | "series";
  item: TrackedItem;
  // onGrab is no longer read by this card (its inline Grab button was removed
  // 2026-08-02) but stays on the prop contract — see PosterCard's identical
  // note and .omc/plans/autopilot-impl-discover-card-cleanup.md §0.5. Made
  // optional (2026-08-02) so callers no longer have to invent a no-op for a
  // prop nothing reads.
  onGrab?: (t: GrabTarget) => void;
  onDetail: (t: DetailTarget) => void;
}> = (props) => {
  const selection = useSelection();
  const inSelect = () => selection?.selectMode() ?? false;
  const tmdbId = () => props.item.tmdbId ?? 0;
  const [poster] = createResource(tmdbId, (id) =>
    id ? fetchTitlePoster(props.mode, id).catch(() => "") : Promise.resolve(""),
  );
  const src = () => tmdbPoster(poster() ?? "");
  const grabItem = (): DiscoverItem => ({
    id: tmdbId(),
    title: props.item.title,
    posterPath: poster() ?? "",
    overview: "",
    releaseDate: props.item.year ? String(props.item.year) : "",
    voteAverage: 0,
    mediaType: props.mode === "series" ? "tv" : "movie",
  });

  // A tracked item with no TMDB id (tmdbId() === 0) has nothing DetailPopup can
  // resolve — opening it would fire fetchTitleDetail/fetchTrailer/
  // fetchAvailabilityPreview against id 0, three requests that cannot succeed
  // and that the popup itself does not guard (it reads item.id unconditionally).
  // Such a card stays click-inert, exactly as every LibraryCard was before
  // 2026-08-02. The select-mode half mirrors PosterCard/AdultCard: this card is
  // not selectable (it registers no key and shows no checkbox), so while
  // select-mode is on its body does nothing at all rather than opening a
  // grab-capable popup mid-bulk-select — which is also what keeps DetailPopup's
  // recommendation rail free of a nested picker Modal.
  const clickable = () => tmdbId() > 0 && !inSelect();
  const openDetail = () => {
    if (!clickable()) return;
    props.onDetail({ mode: props.mode, item: grabItem() });
  };

  return (
    <div class="w-[220px] shrink-0" title={props.item.title}>
      <div classList={{ "cursor-pointer": clickable() }} onClick={openDetail}>
        <div class="aspect-[2/3] overflow-hidden rounded-lg border border-border bg-surface">
          <Show when={src()} fallback={<TextPoster label={props.item.title} />}>
            <img
              src={src()}
              alt={props.item.title}
              loading="lazy"
              class="h-full w-full object-cover"
            />
          </Show>
        </div>
        <div class="mt-1.5 truncate text-sm text-fg" title={props.item.title}>
          {props.item.title}
        </div>
        <div class="flex items-center gap-2 text-xs text-muted">
          <span>{props.item.year || "—"}</span>
        </div>
      </div>
    </div>
  );
};

// LIBRARY_PAGE_SIZE bounds how many library cards render (and therefore how many
// per-card poster fetches fire) at once, mirroring the category rows' "Show
// more" paging. Without this the whole tracked set mounts in one shot, firing a
// poster fetch per card — a real fan-out on a large library.
const LIBRARY_PAGE_SIZE = 20;

// LibraryRow surfaces what's already tracked, movies + series merged into one
// strip (each card tagged with its own mode). The full tracked set is fetched
// once (it's the operator's own bounded library, not TMDB's infinite catalog),
// but only one page's worth is rendered at a time behind a "Show more" — the
// same paging shape PaginatedRow uses — so DOM size and concurrent per-card
// poster fetches stay bounded. Reloads on reloadToken alongside the category
// rows; the visible count resets to one page on every reload.
const LibraryRow: Component<{
  mode?: "movies" | "series";
  reloadToken: () => number;
  onGrab: (t: GrabTarget) => void;
  onDetail: (t: DetailTarget) => void;
}> = (props) => {
  const [entries] = createResource(props.reloadToken, async () => {
    if (props.mode) {
      const items = await fetchTrackedItems(props.mode).catch(() => [] as TrackedItem[]);
      return items.map((item) => ({ mode: props.mode!, item }));
    }
    const [movies, series] = await Promise.all([
      fetchTrackedItems("movies").catch(() => [] as TrackedItem[]),
      fetchTrackedItems("series").catch(() => [] as TrackedItem[]),
    ]);
    return [
      ...movies.map((item) => ({ mode: "movies" as const, item })),
      ...series.map((item) => ({ mode: "series" as const, item })),
    ];
  });

  const [visible, setVisible] = createSignal(LIBRARY_PAGE_SIZE);
  createEffect(on(props.reloadToken, () => setVisible(LIBRARY_PAGE_SIZE)));

  const shown = () => (entries() ?? []).slice(0, visible());
  const hasMore = () => (entries()?.length ?? 0) > visible();

  return (
    <Show when={(entries()?.length ?? 0) > 0}>
      <Carousel
        title="In your library"
        items={shown()}
        renderItem={(e) => (
          <LibraryCard
            mode={e.mode}
            item={e.item}
            onGrab={props.onGrab}
            onDetail={props.onDetail}
          />
        )}
        onLoadMore={() => setVisible((n) => n + LIBRARY_PAGE_SIZE)}
        hasMore={hasMore()}
      />
    </Show>
  );
};

// sliderItemMode picks the per-item grab mode for one SliderRow card. A
// movie/tv-targeted slider is unambiguous; a "mixed" slider (both movies and
// series in one row) falls back to the item's own mediaType, the same
// per-item-mode pattern ModedTitle/LibraryRow already use for merged rows.
function sliderItemMode(
  target: Slider["target"],
  item: DiscoverItem,
): "movies" | "series" {
  if (target === "movie") return "movies";
  if (target === "tv") return "series";
  return item.mediaType === "tv" ? "series" : "movies";
}

// SliderRow is one admin-defined custom slider, paginated the same way
// PaginatedRow is (GET /api/discover/sliders/{id}/resolve, see
// src/api/discoverSliders.ts). A fetch failure bubbles to onError so it
// raises the same setup modal a built-in row's failure would (sliders are
// TMDB-sourced too).
const SliderRow: Component<{
  slider: Slider;
  reloadToken: () => number;
  onGrab: (t: GrabTarget) => void;
  onDetail: (t: DetailTarget) => void;
  onError: (err: unknown) => void;
}> = (props) => {
  const [items, setItems] = createSignal<DiscoverItem[]>([]);
  const [page, setPage] = createSignal(0);
  const [loading, setLoading] = createSignal(false);
  const [exhausted, setExhausted] = createSignal(false);

  const load = async (reset: boolean) => {
    const next = reset ? 1 : page() + 1;
    setLoading(true);
    try {
      const batch = await fetchSliderItems(props.slider.id, next);
      setItems((prev) => (reset ? batch : [...prev, ...batch]));
      setPage(next);
      if (batch.length === 0) setExhausted(true);
    } catch (e) {
      props.onError(e);
    } finally {
      setLoading(false);
    }
  };

  createEffect(
    on(props.reloadToken, () => {
      setItems([]);
      setPage(0);
      setExhausted(false);
      void load(true);
    }),
  );

  return (
    <Carousel
      title={props.slider.title}
      items={items()}
      renderItem={(item) => (
        <PosterCard
          mode={sliderItemMode(props.slider.target, item)}
          item={item}
          onGrab={props.onGrab}
          onDetail={props.onDetail}
        />
      )}
      onLoadMore={() => void load(false)}
      hasMore={!exhausted()}
      loading={loading()}
    />
  );
};

// MainstreamDiscover is the combined Movies+Series page: a search bar over four
// stacked TMDB category rows plus the existing-library row. Searching replaces
// the rows with one merged (movies+series) result grid; clearing restores the
// rows. It owns the single grab dialog for every card (rows, library, search)
// and the not-configured setup modal, raised once when any row's fetch reports
// TMDB missing. editMode (from Discover/index.tsx's tab-bar Edit toggle) swaps
// the row block for RowEditor's reorder/enable/delete UI.
export const MainstreamDiscover: Component<{
  contentType?: MainstreamContentType;
  editMode?: () => boolean;
  // onFilteringChange lets the tab shell (index.tsx) disable its Edit toggle
  // while a filtered grid is up — reordering carousels has no meaning against
  // a filter result. The filter state lives here; the toggle button lives one
  // level up, so this is the minimal upward signal to gate it.
  onFilteringChange?: (active: boolean) => void;
}> = (props) => {
  const [grabTarget, setGrabTarget] = createSignal<GrabTarget | null>(null);
  const [detailTarget, setDetailTarget] = createSignal<DetailTarget | null>(null);
  // releasePicker is the catalog-Search release picker for a clicked searched
  // card (Movies/Series two-step: it fetches the one bounded Prowlarr search on
  // open). Browse rows never set it — they open DetailPopup instead.
  const [releasePicker, setReleasePicker] = createSignal<
    { mode: "movies" | "series"; title: string; tmdbId: number } | null
  >(null);
  const [setupError, setSetupError] = createSignal<unknown>(null);
  const [dismissedSetup, setDismissedSetup] = createSignal(false);
  const [reloadToken, setReloadToken] = createSignal(0);

  // Search: draft is the input value, submitted is the committed query. A
  // non-empty submitted query swaps the rows for the merged result grid.
  const [draft, setDraft] = createSignal("");
  const [submitted, setSubmitted] = createSignal("");
  const searching = () => submitted().trim().length > 0;

  // Filters: the ad-hoc filter/sort bar's state. filtering() is true only when
  // a real filter/non-default sort is set AND no search is running (search and
  // filters are mutually exclusive views). When filtering, a single filtered
  // grid replaces the carousels.
  const [filters, setFilters] = createSignal<MainstreamFilters>(
    {
      ...DEFAULT_MAINSTREAM_FILTERS,
      contentType: props.contentType ?? DEFAULT_MAINSTREAM_FILTERS.contentType,
    },
  );
  const filtering = () => !searching() && isMainstreamFilterActive(filters());
  createEffect(
    on(
      () => props.contentType,
      (contentType) => {
        if (!contentType) return;
        setDraft("");
        setSubmitted("");
        setFilters({ ...DEFAULT_MAINSTREAM_FILTERS, contentType });
      },
      { defer: true },
    ),
  );

  // view toggles the whole page between the default rows and the F2 calendar.
  // Calendar has no reorderable rows, so — like an active filter — it must
  // disable the shell's Edit toggle: onFilteringChange is the existing upward
  // signal that gates Edit (index.tsx), so calendar reuses it (Edit is disabled
  // whenever a filter is active OR calendar is showing), rather than adding a
  // second parallel prop for the same effect.
  const [view, setView] = createSignal<MainstreamView>("rows");
  createEffect(() =>
    props.onFilteringChange?.(filtering() || view() === "calendar"),
  );

  // selectView switches the top-level view; entering calendar clears any active
  // search (calendar is its own view, not a rows-mode activity) so returning to
  // rows lands back on the carousels, not a stale search result.
  const selectView = (v: MainstreamView) => {
    if (v === "calendar") clearSearch();
    setView(v);
  };

  // toFilterParams maps the bar's filter state onto the API's optional-param
  // shape (null genre/year/rating become "unset", i.e. not sent). The single
  // selected genreId is sent as a 1-element genreIds array — the backend still
  // takes the array param and comma-splits/OR-joins it, so no backend change.
  const toFilterParams = (f: MainstreamFilters): DiscoverFilterParams => ({
    genreIds: f.genreId != null ? [f.genreId] : undefined,
    year: f.year ?? undefined,
    minRating: f.minRating ?? undefined,
    sortBy: f.sortBy,
  });

  // Changing any filter clears an active search (mutual exclusivity); the bar
  // itself only renders when not searching, but a search could be committed in
  // the same tick, so clear defensively.
  const applyFilters = (f: MainstreamFilters) => {
    clearSearch();
    setFilters(f);
  };

  const [results] = createResource(
    () => (searching() ? submitted().trim() : null),
    async (q): Promise<ModedTitle[]> => {
      // A search error is surfaced the same way a category row's is: hand it to
      // setSetupError so a "tmdb isn't configured yet" failure raises the same
      // setup modal (the render's notConfiguredService gate decides modal vs.
      // plain error), instead of being swallowed into an empty "No results
      // found". Reusing the row plumbing keeps one detection path, not two.
      try {
        if (props.contentType) {
          const items = await fetchTmdbSearch(props.contentType, q);
          return items.map((item) => ({ mode: props.contentType!, item }));
        }
        const [movies, series] = await Promise.all([
          fetchTmdbSearch("movies", q),
          fetchTmdbSearch("series", q),
        ]);
        return [
          ...movies.map((item) => ({ mode: "movies" as const, item })),
          ...series.map((item) => ({ mode: "series" as const, item })),
        ];
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

  // --- Discover row order: built-in rows above + custom sliders + RSS feed
  // rows, fully interleavable via Edit mode (RowEditor). ---
  const [slidersData] = createResource(reloadToken, () =>
    fetchDiscoverSliders().catch(() => [] as Slider[]),
  );
  const allSliders = () => slidersData() ?? [];
  const slidersForTab = () =>
    allSliders().filter((s) => {
      if (!props.contentType) return true;
      const target = props.contentType === "movies" ? "movie" : "tv";
      return s.target === target || s.target === "mixed";
    });
  const enabledSlidersForTab = () => slidersForTab().filter((s) => s.enabled);

  const [feedsData] = createResource(reloadToken, () =>
    fetchRssFeeds().catch(() => [] as RssFeed[]),
  );
  const mainstreamFeeds = () =>
    (feedsData() ?? []).filter((f) => {
      if (props.contentType) {
        return f.target === (props.contentType === "movies" ? "movie" : "tv");
      }
      return f.target === "movie" || f.target === "tv";
    });
  const enabledFeeds = () => mainstreamFeeds().filter((f) => f.enabled);

  // knownKeys is every row this screen currently knows about, builtin +
  // dynamic (INCLUDING disabled dynamic rows — RowEditor needs to show and
  // re-enable them). Default order (an empty stored order, e.g. a fresh
  // install) matches the page's original hardcoded row sequence exactly.
  const knownKeys = () => [
    "trakt-watchlist",
    ...MAINSTREAM_ROWS.filter((r) => !props.contentType || r.mode === props.contentType).map((r) => r.key),
    ...slidersForTab().map((s) => `slider:${s.id}`),
    ...mainstreamFeeds().map((f) => `rssfeed:${f.id}`),
    "library",
  ];

  const { orderedKeys, persistOrder, isHidden, toggleHidden, error: rowOrderError } =
    useRowOrder("mainstream", knownKeys);
  // rowActionError covers a toggle/delete's own mutation failure (updateSlider/
  // deleteRssFeed/etc.) — a distinct failure mode from useRowOrder's error
  // (a saveRowOrder persist failure) but shown in the same spot; editError
  // combines them so RowEditor's error line doesn't need two <Show> blocks.
  const [rowActionError, setRowActionError] = createSignal("");
  const editError = () => rowOrderError() || rowActionError();

  const descriptorFor = (key: string): RowDescriptor | undefined => {
    const builtinRow = MAINSTREAM_ROWS.find(
      (r) => r.key === key && (!props.contentType || r.mode === props.contentType),
    );
    if (builtinRow) {
      return { key, label: builtinRow.title, kind: "structural", hidden: isHidden(key) };
    }
    if (MAINSTREAM_FIXED_LABELS[key]) {
      return {
        key,
        label: MAINSTREAM_FIXED_LABELS[key]!,
        kind: "structural",
        hidden: isHidden(key),
      };
    }
    if (key.startsWith("slider:")) {
      const id = Number(key.slice("slider:".length));
        const s = slidersForTab().find((s) => s.id === id);
      return s ? { key, label: s.title, kind: "entity", enabled: s.enabled } : undefined;
    }
    if (key.startsWith("rssfeed:")) {
      const id = Number(key.slice("rssfeed:".length));
      const f = mainstreamFeeds().find((f) => f.id === id);
      return f ? { key, label: f.title, kind: "entity", enabled: f.enabled } : undefined;
    }
    return undefined;
  };

  const rowDescriptors = (): RowDescriptor[] =>
    orderedKeys()
      .map(descriptorFor)
      .filter((d): d is RowDescriptor => d !== undefined);

  const toggleRowEnabled = async (row: RowDescriptor) => {
    try {
      if (row.key.startsWith("slider:")) {
        const s = slidersForTab().find((s) => `slider:${s.id}` === row.key);
        if (!s) return;
        await updateSlider(s.id, {
          title: s.title,
          filterType: s.filterType,
          filterValue: s.filterValue ?? "",
          target: s.target,
          enabled: !s.enabled,
        });
      } else if (row.key.startsWith("rssfeed:")) {
        const f = mainstreamFeeds().find((f) => `rssfeed:${f.id}` === row.key);
        if (!f) return;
        await updateRssFeed(f.id, {
          title: f.title,
          // feedUrl is masked on read (f.feedUrl is "" now, not the real URL) —
          // send null to PRESERVE the stored encrypted URL. Re-sending f.feedUrl
          // would post "" and the backend rejects it as ErrFeedURLRequired.
          feedUrl: null,
          target: f.target,
          protocol: f.protocol,
          enabled: !f.enabled,
        });
      }
      setReloadToken((n) => n + 1);
    } catch (e) {
      setRowActionError((e as Error).message);
    }
  };

  const deleteRow = async (row: RowDescriptor) => {
    if (!confirm(`Delete "${row.label}"?`)) return;
    try {
      if (row.key.startsWith("slider:")) {
        await deleteSlider(Number(row.key.slice("slider:".length)));
      } else if (row.key.startsWith("rssfeed:")) {
        await deleteRssFeed(Number(row.key.slice("rssfeed:".length)));
      }
      persistOrder(orderedKeys().filter((k) => k !== row.key));
      setReloadToken((n) => n + 1);
    } catch (e) {
      setRowActionError((e as Error).message);
    }
  };

  // visibleKeys is orderedKeys filtered to what actually renders: builtins
  // always show (their own empty-state handles "nothing yet"); a dynamic row
  // shows only when enabled — same client-side filter the pre-row-order
  // SliderRows block already applied.
  const visibleKeys = () =>
    orderedKeys().filter((key) => {
      if (key.startsWith("slider:")) {
        return enabledSlidersForTab().some((s) => `slider:${s.id}` === key);
      }
      if (key.startsWith("rssfeed:")) {
        return enabledFeeds().some((f) => `rssfeed:${f.id}` === key);
      }
      // structural rows (built-ins, Trakt, library): shown unless hidden.
      return !isHidden(key);
    });

  const renderRow = (key: string): JSX.Element => {
    const builtinRow = MAINSTREAM_ROWS.find(
      (r) => r.key === key && (!props.contentType || r.mode === props.contentType),
    );
    if (builtinRow) {
      return (
        <PaginatedRow
          title={builtinRow.title}
          mode={builtinRow.mode}
          category={builtinRow.category}
          reloadToken={reloadToken}
          onGrab={setGrabTarget}
          onDetail={setDetailTarget}
          onError={setSetupError}
        />
      );
    }
    if (key === "trakt-watchlist") return <TraktWatchlistRow onGrab={setGrabTarget} />;
    if (key === "library")
      return (
        <LibraryRow
          mode={props.contentType}
          reloadToken={reloadToken}
          onGrab={setGrabTarget}
          onDetail={setDetailTarget}
        />
      );
    if (key.startsWith("slider:")) {
      const slider = enabledSlidersForTab().find((s) => `slider:${s.id}` === key)!;
      return (
        <SliderRow
          slider={slider}
          reloadToken={reloadToken}
          onGrab={setGrabTarget}
          onDetail={setDetailTarget}
          onError={setSetupError}
        />
      );
    }
    const feed = enabledFeeds().find((f) => `rssfeed:${f.id}` === key)!;
    return <RssFeedRow feed={feed} reloadToken={reloadToken} onError={setSetupError} />;
  };

  const [addFeedOpen, setAddFeedOpen] = createSignal(false);

  return (
    <div>
      {/* Rows | Calendar view toggle. Lives in the filter-bar area inside the
          Mainstream tab (not a third top-level Discover tab) — the same
          avoid-a-degenerate-tab reasoning as index.tsx's Adult-disabled block. */}
      <div class="mb-3 flex items-center gap-1">
        <For each={["rows", "calendar"] as MainstreamView[]}>
          {(v) => (
            <button
              type="button"
              class="rounded-md px-3 py-1.5 text-sm font-medium transition"
              classList={{
                "bg-accent text-accent-fg": view() === v,
                "bg-surface-2 text-muted hover:text-fg": view() !== v,
              }}
              onClick={() => selectView(v)}
            >
              {v === "rows" ? "Rows" : "Calendar"}
            </button>
          )}
        </For>
      </div>

      <Show when={view() !== "calendar"}>
        <form
          class="mb-4 flex gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            // A search takes over the view — reset any active filter so clearing
            // the search returns to the carousels, not into a stale filter grid.
            setFilters(DEFAULT_MAINSTREAM_FILTERS);
            setSubmitted(draft());
          }}
        >
          <input
            class="w-full max-w-sm rounded-md border border-border bg-bg px-3 py-2 text-sm text-fg outline-none focus:border-accent"
            placeholder="Search movies & shows…"
            value={draft()}
            onInput={(e) => setDraft(e.currentTarget.value)}
          />
          <Show when={searching()}>
            <Button onClick={clearSearch}>Clear</Button>
          </Show>
        </form>

        <Show when={!searching()}>
          <MainstreamFilterSortBar
            value={filters}
            onChange={applyFilters}
            lockedContentType={props.contentType}
          />
        </Show>
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
        when={view() !== "calendar"}
        fallback={
          <CalendarView onGrab={setGrabTarget} onDetail={setDetailTarget} />
        }
      >
      <Show
        when={searching()}
        fallback={
          <Show
            when={filtering()}
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
            <div class="mt-6 flex justify-center">
              <Button onClick={() => setAddFeedOpen(true)}>+ Add RSS feed</Button>
            </div>
            <Show when={addFeedOpen()}>
              <AddRssFeedModal
                allowedTargets={["movie", "tv"]}
                defaultTarget="movie"
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
            <PaginatedStrip<DiscoverItem>
              title="Filtered results"
              reloadToken={() => JSON.stringify(filters())}
              load={(page) =>
                fetchDiscoverFiltered(
                  filters().contentType,
                  toFilterParams(filters()),
                  page,
                )
              }
              onError={setSetupError}
              containerClass="flex flex-wrap gap-3"
            >
              {(item) => (
                <PosterCard
                  mode={filters().contentType}
                  item={item}
                  onGrab={setGrabTarget}
                  onDetail={setDetailTarget}
                />
              )}
            </PaginatedStrip>
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
              fallback={<Muted>No results found.</Muted>}
            >
              <div class="flex flex-wrap gap-3">
                <For each={results()}>
                  {(e) => (
                    <PosterCard
                      mode={e.mode}
                      item={e.item}
                      onGrab={setGrabTarget}
                      onDetail={setDetailTarget}
                      onOpenReleases={() =>
                        setReleasePicker({
                          mode: e.mode,
                          title: e.item.title,
                          tmdbId: e.item.id,
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
      </Show>

      <Show when={grabTarget()}>
        {(t) => <GrabDialog target={t()} onClose={() => setGrabTarget(null)} />}
      </Show>
      <Show when={releasePicker()}>
        {(rp) => (
          <SearchReleasePicker
            mode={rp().mode}
            title={rp().title}
            tmdbId={rp().tmdbId}
            onClose={() => setReleasePicker(null)}
          />
        )}
      </Show>
      {/* keyed: a "More like this" click swaps detailTarget from one truthy
          target to another. Without keyed, Solid updates props.target on the
          SAME DetailPopup instance, leaving its component-local signals
          (resolution/tier/protocol/grabbed/seasonEpisode) stale from the prior
          title while only the keyed resources refetch. keyed remounts the popup
          so every one of those resets to the newly-targeted title. */}
      <Show when={detailTarget()} keyed>
        {(t) => (
          <DetailPopup
            target={t}
            onClose={() => setDetailTarget(null)}
            onSelectRecommendation={setDetailTarget}
            onGrab={setGrabTarget}
          />
        )}
      </Show>
    </div>
  );
};
