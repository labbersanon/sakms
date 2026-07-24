// MainstreamDiscover — the combined Movies+Series page and its cards: a search
// bar over four stacked, independently-paginated TMDB category rows plus a
// paginated "In your library" row of what's already tracked. Movies grab
// directly on click; Series first open a season/episode picker (the gating step,
// since no release exists to score until a specific episode/pack is chosen).
// Extracted from the original single-file Discover.tsx.
//
// Row order (Optional RSS Discover rows + inline row editor): the row block
// below is no longer a fixed hardcoded sequence — it's driven by a merged,
// operator-reorderable key list (see api/rowOrder.ts's mergeRowOrder) that
// interleaves the built-in rows above with admin-defined custom sliders and
// RSS feed rows. `editMode` (passed down from Discover/index.tsx's tab-bar
// Edit toggle) swaps the row list for RowEditor's reorder/enable/delete UI;
// the "+ Add RSS feed" tile at the bottom is always visible regardless of
// edit mode.

import {
  type Component,
  type JSX,
  createEffect,
  createResource,
  createSignal,
  on,
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
  PaginatedStrip,
  TextPoster,
  notConfiguredService,
} from "./shared";
import {
  type MainstreamFilters,
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

// SeasonEpisodePicker gates a Series grab: no release can be scored until a
// specific season (and optionally episode) is chosen. Submitting always marks
// the season as specified — that is what preserves Season-0/Specials (a bare
// season number can't distinguish "Season 0 picked" from "nothing picked").
// Exported (was module-private) so DetailPopup.tsx reuses the identical
// season/episode input as its own Series gating step, instead of a second
// hand-rolled one.
export const SeasonEpisodePicker: Component<{
  onSubmit: (season: number, episode: number) => void;
}> = (props) => {
  const [season, setSeason] = createSignal("");
  const [episode, setEpisode] = createSignal("");
  return (
    <form
      class="mt-1 flex items-center gap-1"
      onSubmit={(e) => {
        e.preventDefault();
        props.onSubmit(
          parseInt(season(), 10) || 0,
          parseInt(episode(), 10) || 0,
        );
      }}
    >
      <input
        class="w-12 rounded border border-border bg-bg px-1 py-0.5 text-xs text-fg outline-none focus:border-accent"
        placeholder="S"
        aria-label="Season"
        value={season()}
        onInput={(e) => setSeason(e.currentTarget.value)}
      />
      <input
        class="w-12 rounded border border-border bg-bg px-1 py-0.5 text-xs text-fg outline-none focus:border-accent"
        placeholder="E"
        aria-label="Episode"
        value={episode()}
        onInput={(e) => setEpisode(e.currentTarget.value)}
      />
      <button
        type="submit"
        class="rounded bg-accent px-2 py-0.5 text-xs font-medium text-accent-fg"
      >
        Go
      </button>
    </form>
  );
};

// GrabButton is the per-title grab affordance. Movies grab on click. Series
// first reveal the season/episode picker (the gating step) and only build a
// GrabTarget once the picker is submitted.
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
      <Show
        when={picking()}
        fallback={
          <Button class="w-full !py-1 text-xs" onClick={() => setPicking(true)}>
            Grab
          </Button>
        }
      >
        <SeasonEpisodePicker onSubmit={grabSeries} />
      </Show>
    </Show>
  );
};

// PosterCard is one Movies/Series title. Fixed width so a row scrolls
// horizontally. Clicking the card body (poster/title/meta — NOT the Grab
// button below, which stays as today's unchanged one-click quick-grab
// shortcut) opens DetailPopup via onDetail. The native title= overview
// tooltip is replaced by a CSS-only (group/group-hover) hover overlay over
// the poster — same information, richer presentation, no new Solid signal.
// Exported (was module-private) so DetailPopup's "More like this" recommendation
// rail and CalendarView reuse the identical card — same reason SeasonEpisodePicker
// was exported for DetailPopup — rather than each hand-rolling a parallel one-off
// (which would also miss the later F3 select-mode checkbox this card will gain).
export const PosterCard: Component<{
  mode: "movies" | "series";
  item: DiscoverItem;
  onGrab: (t: GrabTarget) => void;
  onDetail: (t: DetailTarget) => void;
}> = (props) => {
  const src = () => tmdbPoster(props.item.posterPath);
  return (
    <div class="w-[180px] shrink-0">
      <div
        class="group cursor-pointer"
        onClick={() => props.onDetail({ mode: props.mode, item: props.item })}
      >
        <div class="relative aspect-[2/3] overflow-hidden rounded-lg border border-border bg-surface">
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
      <div class="mt-1.5">
        <GrabButton mode={props.mode} item={props.item} onGrab={props.onGrab} />
      </div>
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
// fetch and the auto-grab path. The library caches no poster art, so the
// poster is resolved on demand by tmdbId (fetchTitlePoster) — one bounded call
// per rendered card, then routed through the image proxy exactly like every
// other card. A synthetic DiscoverItem (id = tmdbId) feeds GrabButton so a
// library card grabs through the identical GrabDialog/autoGrab path a Discover
// card does — Series still gets its season/episode picker.
const LibraryCard: Component<{
  mode: "movies" | "series";
  item: TrackedItem;
  onGrab: (t: GrabTarget) => void;
}> = (props) => {
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
  return (
    <div class="w-[180px] shrink-0" title={props.item.title}>
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
      <div class="mt-1.5">
        <GrabButton mode={props.mode} item={grabItem()} onGrab={props.onGrab} />
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
  reloadToken: () => number;
  onGrab: (t: GrabTarget) => void;
}> = (props) => {
  const [entries] = createResource(props.reloadToken, async () => {
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
          <LibraryCard mode={e.mode} item={e.item} onGrab={props.onGrab} />
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
  editMode?: () => boolean;
  // onFilteringChange lets the tab shell (index.tsx) disable its Edit toggle
  // while a filtered grid is up — reordering carousels has no meaning against
  // a filter result. The filter state lives here; the toggle button lives one
  // level up, so this is the minimal upward signal to gate it.
  onFilteringChange?: (active: boolean) => void;
}> = (props) => {
  const [grabTarget, setGrabTarget] = createSignal<GrabTarget | null>(null);
  const [detailTarget, setDetailTarget] = createSignal<DetailTarget | null>(null);
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
    DEFAULT_MAINSTREAM_FILTERS,
  );
  const filtering = () => !searching() && isMainstreamFilterActive(filters());

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
  // shape (empty genre set / null year/rating become "unset", i.e. not sent).
  const toFilterParams = (f: MainstreamFilters): DiscoverFilterParams => ({
    genreIds: f.genreIds.length ? f.genreIds : undefined,
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
  const enabledSliders = () => allSliders().filter((s) => s.enabled);

  const [feedsData] = createResource(reloadToken, () =>
    fetchRssFeeds().catch(() => [] as RssFeed[]),
  );
  const mainstreamFeeds = () =>
    (feedsData() ?? []).filter((f) => f.target === "movie" || f.target === "tv");
  const enabledFeeds = () => mainstreamFeeds().filter((f) => f.enabled);

  // knownKeys is every row this screen currently knows about, builtin +
  // dynamic (INCLUDING disabled dynamic rows — RowEditor needs to show and
  // re-enable them). Default order (an empty stored order, e.g. a fresh
  // install) matches the page's original hardcoded row sequence exactly.
  const knownKeys = () => [
    "trakt-watchlist",
    ...MAINSTREAM_ROWS.map((r) => r.key),
    ...allSliders().map((s) => `slider:${s.id}`),
    ...mainstreamFeeds().map((f) => `rssfeed:${f.id}`),
    "library",
  ];

  const { orderedKeys, moveRow, persistOrder, error: rowOrderError } =
    useRowOrder("mainstream", knownKeys);
  // rowActionError covers a toggle/delete's own mutation failure (updateSlider/
  // deleteRssFeed/etc.) — a distinct failure mode from useRowOrder's error
  // (a saveRowOrder persist failure) but shown in the same spot; editError
  // combines them so RowEditor's error line doesn't need two <Show> blocks.
  const [rowActionError, setRowActionError] = createSignal("");
  const editError = () => rowOrderError() || rowActionError();

  const descriptorFor = (key: string): RowDescriptor | undefined => {
    const builtinRow = MAINSTREAM_ROWS.find((r) => r.key === key);
    if (builtinRow) return { key, label: builtinRow.title, removable: false };
    if (MAINSTREAM_FIXED_LABELS[key]) {
      return { key, label: MAINSTREAM_FIXED_LABELS[key]!, removable: false };
    }
    if (key.startsWith("slider:")) {
      const id = Number(key.slice("slider:".length));
      const s = allSliders().find((s) => s.id === id);
      return s ? { key, label: s.title, removable: true, enabled: s.enabled } : undefined;
    }
    if (key.startsWith("rssfeed:")) {
      const id = Number(key.slice("rssfeed:".length));
      const f = mainstreamFeeds().find((f) => f.id === id);
      return f ? { key, label: f.title, removable: true, enabled: f.enabled } : undefined;
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
        const s = allSliders().find((s) => `slider:${s.id}` === row.key);
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
          feedUrl: f.feedUrl,
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
        return enabledSliders().some((s) => `slider:${s.id}` === key);
      }
      if (key.startsWith("rssfeed:")) {
        return enabledFeeds().some((f) => `rssfeed:${f.id}` === key);
      }
      return true;
    });

  const renderRow = (key: string): JSX.Element => {
    const builtinRow = MAINSTREAM_ROWS.find((r) => r.key === key);
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
    if (key === "library") return <LibraryRow reloadToken={reloadToken} onGrab={setGrabTarget} />;
    if (key.startsWith("slider:")) {
      const slider = enabledSliders().find((s) => `slider:${s.id}` === key)!;
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
          <MainstreamFilterSortBar value={filters} onChange={applyFilters} />
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
