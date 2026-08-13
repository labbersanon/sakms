// Library — browse the tracked Movies/Series/Adult catalog. This is the one poster-grid
// browser in the app: it took over the grid half that used to live in Tag.tsx
// (PosterCard, DetailPanel, the client-side title search and the selection/detail
// wiring all moved here verbatim), leaving Tag as the tag-CRUD table it was
// originally documented to be.
//
// Layout, top to bottom: a Movies/Series/Adult tab bar over a filter row (title search
// | genre | quality tier | sort) and a poster grid; selecting a card slides a
// DetailPanel in at w-72 showing genres/cast/tags — plus, for Series only, the
// per-season monitoring panel (SeasonsPanel). Tag mutations in the panel go
// through the same addTag/removeTag + act() path Tag's table uses — only the
// entry point changed, not the mechanics.
//
// The mode and tier are ALSO seedable from the URL (/library?mode=…&tier=…),
// which is what the Dashboard's storage-allocation cells link into. That is a
// one-shot seed at mount, not a two-way binding — see the Library shell below.
//
// Filter and sort are CLIENT-SIDE by design (spec Non-Goal 4), over the already
// fetched GET /api/modes/{mode}/tracked payload — matching the precedent Tag's
// title search already set. The backend gains no query parameters. Added-date
// sort leans on createdAt being a fixed-width ISO-8601 UTC string, so a plain
// lexicographic compare is a correct chronological one (no Date parsing).

import {
  type Component,
  createEffect,
  createResource,
  createSignal,
  For,
  on,
  onCleanup,
  onMount,
  Show,
} from "solid-js";
import { useSearchParams } from "@solidjs/router";
import type { Mode } from "../api/discover";
import { fetchTitlePoster, tmdbPoster } from "../api/discover";
import {
  fetchSeasonStates,
  putAllSeasonsMonitored,
  putSeasonMonitored,
} from "../api/seasons";
import {
  type TrackedItem,
  addTag,
  fetchTagVocabulary,
  fetchTrackedItems,
  removeTag,
} from "../api/tag";
import {
  Button,
  // Card, // Claude 2026-08-13: only the commented-out AdultMoviesPlaceholder used this.
  ErrorText,
  // ModeTabs, // Claude 2026-08-13: only the commented-out legacy Library shell used this.
  Muted,
  ScreenTabs,
  SectionLockOverlay,
  Switch,
  useAdultEnabled,
  useSectionLock,
  inputClass,
  labelClass,
} from "../components/ui";
import {
  MediaCardShell,
  MediaBadge,
  MediaDetailShell,
  MediaFallbackTile,
  MediaGridSkeleton,
  MEDIA_POSTER_GRID_CLASS,
} from "../components/media";
import { ADULT_CONTENT_SECTION, sectionLabel } from "../api/sectionLock";
import {
  ADULT_MEDIA_TABS,
  MAINSTREAM_MEDIA_TABS,
  type AdultMediaTab,
  type MainstreamMediaTab,
} from "./mediaNav";
import { Modal } from "./discover/shared";
import { useWorkflowActions } from "./workflowHooks";

type PosterMode = Exclude<Mode, "adult">;

// SortKey selects the grid's ordering. "title" keeps the server's own
// ORDER BY title (no client-side re-sort — SQLite's collation is authoritative);
// "added" sorts by createdAt descending, newest first.
type SortKey = "title" | "added";

// TIER_VALUES is the closed quality-tier vocabulary TrackedItem.qualityTiers
// uses. Deliberately a fixed list rather than derived from the loaded items the
// way genreOptions is: a Dashboard deep link's incoming tier must render in the
// <select> immediately, and a derived list would have no matching <option>
// until the fetch resolved — silently snapping the control back to "All tiers"
// while the grid stayed filtered.
const TIER_VALUES = ["low", "medium", "high", "lossless", "unknown"];

const selectClass =
  "rounded-md border border-border bg-bg px-3 py-2 text-sm text-fg outline-none focus:border-accent";

// firstFrameSrc appends the #t=0.1 media fragment so the browser seeks just past
// the start and paints that frame as the still. Without the fragment a
// preload="metadata" <video> stays blank until playback begins.
const firstFrameSrc = (videoUrl: string) => `${videoUrl}#t=0.1`;

// PosterCard is one grid cell. It lazily fetches the item's TMDB poster and
// renders it; with no poster it falls back to the Adult scene's own video still
// (Adult scenes have no tmdbId, so that is their only artwork) and then to a
// grey tile with the first letter of the title. Selected state is indicated
// with an accent ring; unselected uses a transparent border.
const PosterCard: Component<{
  item: TrackedItem;
  mode: Mode;
  selected: boolean;
  onClick: () => void;
  // Claude 2026-08-13: poster frame ratio, default 2:3.
  // Reason: Library Scenes is 16:9 (2B); Mainstream and Adult Movies stay 2:3.
  // Threaded as a value so a future catalog poster does not bake the tab into
  // the card. Review if: Adult catalog artwork lands and 2B is revisited.
  posterAspect?: string;
}> = (props) => {
  // Key the resource on tmdbId — when absent, the source accessor returns
  // undefined and Solid skips the fetch entirely.
  const [posterPath] = createResource(
    () =>
      props.mode !== "adult" && props.item.tmdbId
        ? ({ mode: props.mode as PosterMode, tmdbId: props.item.tmdbId })
        : undefined,
    ({ mode, tmdbId }) => fetchTitlePoster(mode, tmdbId),
  );

  const posterUrl = () => {
    const path = posterPath();
    return path ? tmdbPoster(path) : "";
  };
  const adultVideoUrl = () =>
    props.mode === "adult" && !posterUrl() ? (props.item.videoUrl ?? "") : "";
  const [videoVisible, setVideoVisible] = createSignal(false);
  const [videoError, setVideoError] = createSignal(false);
  const showAdultVideo = () =>
    adultVideoUrl() && videoVisible() && !videoError();
  let posterBox: HTMLDivElement | undefined;

  // A grid of Adult cards would otherwise ask the server for every scene's
  // metadata at once, so the <video> is withheld until its tile is on screen.
  onMount(() => {
    if (props.mode !== "adult" || !props.item.videoUrl || !posterBox) return;
    if (typeof IntersectionObserver === "undefined") {
      setVideoVisible(true);
      return;
    }
    const observer = new IntersectionObserver((entries) => {
      if (entries.some((entry) => entry.isIntersecting)) {
        setVideoVisible(true);
        observer.disconnect();
      }
    });
    observer.observe(posterBox);
    onCleanup(() => observer.disconnect());
  });

  return (
    <MediaCardShell
      label={props.item.title}
      selected={props.selected}
      onClick={props.onClick}
    >
      <div
        ref={posterBox}
        class="relative w-full"
        style={{ "aspect-ratio": props.posterAspect ?? "2 / 3" }}
      >
        <Show
          when={posterUrl()}
          fallback={
            <Show
              when={showAdultVideo()}
              fallback={
                <MediaFallbackTile
                  title={props.item.title}
                  loading={!!adultVideoUrl() && !videoVisible()}
                  error={videoError()}
                />
              }
            >
              <video
                src={firstFrameSrc(adultVideoUrl())}
                muted
                preload="metadata"
                class="h-full w-full object-cover"
                playsinline
                onError={() => setVideoError(true)}
              />
            </Show>
          }
        >
          <img
            src={posterUrl()}
            alt=""
            loading="lazy"
            class="h-full w-full object-cover"
          />
        </Show>
      </div>
      {/* Card footer */}
      <div class="flex-1 p-2">
        <p class="line-clamp-2 text-xs font-medium text-fg leading-tight">
          {props.item.title}
        </p>
        <Show when={props.item.year}>
          <p class="mt-0.5 text-[11px] text-muted">{props.item.year}</p>
        </Show>
        <Show when={(props.item.genres ?? []).length > 0}>
          <div class="mt-1 flex flex-wrap gap-0.5">
            <For each={(props.item.genres ?? []).slice(0, 2)}>
              {(g) => <MediaBadge class="!rounded !px-1 !text-[10px]">{g}</MediaBadge>}
            </For>
          </div>
        </Show>
      </div>
    </MediaCardShell>
  );
};

// SEASON_UNMONITORED_COPY / SEASON_AUTOGRAB_COPY are the two required honesty
// lines for per-season monitoring. Each renders as its OWN paragraph and as a
// single unbroken text node — no <strong>, no nested span, no <br> — because
// interleaving an element child would split the sentence across nodes and make
// it unfindable by its own text.
const SEASON_UNMONITORED_COPY =
  "An unmonitored season is never searched automatically, no matter how long ago it aired.";
const SEASON_AUTOGRAB_COPY =
  "Monitoring a season does nothing until Settings → Download → Usenet → Enable auto-grab is on, and that takes effect on restart.";

// seasonLabel names season 0 "Specials". Season 0 is LISTED, not filtered out:
// the backend's season set includes it, so hiding it would let "All seasons"
// quietly touch a season no visible row accounts for.
const seasonLabel = (n: number) => (n === 0 ? "Specials" : `Season ${n}`);

// SeasonsPanel is the per-season monitoring surface inside DetailPanel, rendered
// for Series only (Movies has no seasons; Library has no Adult mode at all).
//
// It owns its own resource and refetch deliberately: the parent's post-mutation
// refresh only re-pulls the tag vocabulary and the tracked rows, neither of
// which carries season state. Keyed on seriesID so selecting a different card —
// which does NOT remount this component, since the parent's <Show> is
// non-keyed — re-fetches instead of showing the previous series' seasons.
//
// Every write re-pulls the list rather than flipping a local copy: un-monitoring
// is not a pure flag write server-side (it also cancels that season's queued
// air-date retries), so what the operator sees should be what actually landed.
const SeasonsPanel: Component<{ seriesID: number }> = (props) => {
  const [seasons, { refetch }] = createResource(
    () => props.seriesID,
    fetchSeasonStates,
  );
  const [busy, setBusy] = createSignal(false);
  const [writeError, setWriteError] = createSignal("");

  // Only the PUT is wrapped. A refetch that fails AFTER a successful write is
  // not a failed write, and reporting it as one would tell the operator their
  // un-monitor didn't land when it did (and, with it, the retry cancellation it
  // triggered server-side). The re-read's own failure surfaces through
  // seasons.error, which is rendered separately below.
  const write = async (fn: () => Promise<void>) => {
    setBusy(true);
    setWriteError("");
    try {
      await fn();
    } catch (e) {
      setWriteError((e as Error).message);
      setBusy(false);
      return;
    }
    try {
      await refetch();
    } finally {
      setBusy(false);
    }
  };

  // The seasons.error short-circuit is load-bearing, not defensive noise: a
  // Solid resource's accessor RE-THROWS after its fetcher rejects, so a bare
  // `seasons() ?? []` throws mid-render once the GET fails — the same failure
  // shape that once left Discover's GrabDialog stuck forever (see CLAUDE.md).
  // The error is rendered above instead.
  const rows = () => (seasons.error ? [] : (seasons() ?? []));
  // The bulk switch reads as on only when EVERY season is monitored, so a
  // partially-monitored series shows off and one click monitors the rest.
  const allMonitored = () => rows().length > 0 && rows().every((s) => s.monitored);

  return (
    <div class="mb-3 border-t border-border pt-3">
      <p class="mb-1 text-[11px] font-medium uppercase tracking-wide text-muted">
        Seasons
      </p>
      <Show when={seasons.error}>
        <ErrorText>{(seasons.error as Error)?.message}</ErrorText>
      </Show>
      <Show when={writeError()}>
        <ErrorText>{writeError()}</ErrorText>
      </Show>
      <Show
        when={rows().length > 0}
        fallback={
          <Show when={!seasons.loading && !seasons.error}>
            <Muted>No seasons tracked yet.</Muted>
          </Show>
        }
      >
        <div class="mb-1 flex items-center justify-between gap-2 pb-1">
          <span class="text-xs font-medium text-fg">All seasons</span>
          <Switch
            checked={allMonitored()}
            disabled={busy()}
            ariaLabel="Monitor all seasons"
            onChange={(next) =>
              void write(() => putAllSeasonsMonitored(props.seriesID, next))
            }
          />
        </div>
        <For each={rows()}>
          {(s) => (
            <div class="flex items-center justify-between gap-2 py-1">
              <div class="min-w-0">
                <p class="truncate text-xs text-fg">
                  {seasonLabel(s.seasonNumber)}
                </p>
                {/* episodeCount is every episode ROW the season has, on disk or
                    not — it is NOT an on-disk count. Labelled as the total, with
                    missing shown beside it, so downloaded reads as the
                    difference rather than being misreported. */}
                <p class="text-[11px] text-muted">
                  {`${s.episodeCount} episodes · ${s.missingCount} missing`}
                </p>
              </div>
              <Switch
                checked={s.monitored}
                disabled={busy()}
                ariaLabel={`Monitor ${seasonLabel(s.seasonNumber)}`}
                onChange={(next) =>
                  void write(() =>
                    putSeasonMonitored(props.seriesID, s.seasonNumber, next),
                  )
                }
              />
            </div>
          )}
        </For>
      </Show>
      <Muted class="mt-2">{SEASON_UNMONITORED_COPY}</Muted>
      <Muted class="mt-1">{SEASON_AUTOGRAB_COPY}</Muted>
    </div>
  );
};

// DetailPanel shows full metadata + editable tags for the selected item.
// Poster is lazy-fetched again (same endpoint, browser caches the image).
// Genres and cast are READ-ONLY. Tags are mutable via act() from the parent.
const DetailPanel: Component<{
  item: TrackedItem;
  mode: Mode;
  datalistId: string;
  draft: string;
  onDraftChange: (v: string) => void;
  onAdd: () => void;
  onRemoveTag: (tag: string) => void;
  posterAspect?: string;
}> = (props) => {
  const [posterPath] = createResource(
    () =>
      props.mode !== "adult" && props.item.tmdbId
        ? ({ mode: props.mode as PosterMode, tmdbId: props.item.tmdbId })
        : undefined,
    ({ mode, tmdbId }) => fetchTitlePoster(mode, tmdbId),
  );

  const posterUrl = () => {
    const path = posterPath();
    return path ? tmdbPoster(path) : "";
  };
  const adultVideoUrl = () =>
    props.mode === "adult" && !posterUrl() ? (props.item.videoUrl ?? "") : "";
  const [videoError, setVideoError] = createSignal(false);
  const showAdultVideo = () => adultVideoUrl() && !videoError();
  // Selecting another card does NOT remount this panel (the parent's <Show> is
  // non-keyed), so one unplayable scene would otherwise suppress the video
  // still for every scene selected after it.
  createEffect(on(() => props.item.id, () => setVideoError(false)));

  const media = () => {
    if (posterUrl()) {
      return (
        <img
          src={posterUrl()}
          alt=""
          loading="lazy"
          class="h-full w-full object-cover"
          style={{
            "aspect-ratio": props.posterAspect ?? "2 / 3",
            "object-position": "top",
          }}
        />
      );
    }
    if (!showAdultVideo()) return undefined;
    return (
        <video
          src={firstFrameSrc(adultVideoUrl())}
          muted
          preload="metadata"
          playsinline
          class="h-full w-full object-cover"
          style={{
            "aspect-ratio": props.posterAspect ?? "2 / 3",
            "object-position": "top",
          }}
          onError={() => setVideoError(true)}
        />
    );
  };

  return (
    <MediaDetailShell
      poster={media()}
      chromeless
      subtitle={
        <div class="flex flex-col gap-0.5">
          <Show when={props.item.year}>
            <span>{props.item.year}</span>
          </Show>
          <Show when={props.item.collectionName}>
            <span class="italic">{props.item.collectionName}</span>
          </Show>
        </div>
      }
    >
      {/* Genres — read-only */}
      <Show when={(props.item.genres ?? []).length > 0}>
        <div class="mb-3">
          <p class="mb-1 text-[11px] font-medium uppercase tracking-wide text-muted">Genres</p>
          <div class="flex flex-wrap gap-1">
            <For each={props.item.genres}>
              {(g) => (
                <span class="rounded bg-surface-2 px-1.5 py-0.5 text-xs text-muted">
                  {g}
                </span>
              )}
            </For>
          </div>
        </div>
      </Show>

      {/* Cast — read-only, first 5 */}
      <Show when={(props.item.cast ?? []).length > 0}>
        <div class="mb-3">
          <p class="mb-1 text-[11px] font-medium uppercase tracking-wide text-muted">Cast</p>
          <div class="flex flex-wrap gap-1">
            <For each={(props.item.cast ?? []).slice(0, 5)}>
              {(c) => (
                <span class="rounded bg-surface-2 px-1.5 py-0.5 text-xs text-muted">
                  {c}
                </span>
              )}
            </For>
          </div>
        </div>
      </Show>

      {/* Files — Movies primary + alternates (bitrate/codec/resolution) */}
      <Show when={props.mode === "movies" && (props.item.files ?? []).length > 0}>
        <div class="mb-3">
          <p class="mb-1 text-[11px] font-medium uppercase tracking-wide text-muted">Files</p>
          <ul class="space-y-2">
            <For each={props.item.files}>
              {(f) => {
                const height = f.height ?? 0;
                const width = f.width ?? 0;
                const bitrate = f.bitrate ?? 0;
                const res = height > 0 ? `${width || "?"}×${height}` : "";
                const bits =
                  bitrate > 0
                    ? `${(bitrate / 1_000_000).toFixed(bitrate >= 10_000_000 ? 0 : 1)} Mbps`
                    : "";
                const meta = [res, f.videoCodec, bits, f.qualityTier]
                  .filter(Boolean)
                  .join(" · ");
                const name = f.filePath.split("/").pop() || f.filePath;
                return (
                  <li class="rounded bg-surface-2 px-2 py-1.5 text-xs text-fg">
                    <p class="truncate font-medium">
                      {f.isPrimary ? "Primary" : "Alternate"}
                      {meta ? ` — ${meta}` : ""}
                    </p>
                    <p class="truncate text-muted" title={f.filePath}>
                      {name}
                    </p>
                  </li>
                );
              }}
            </For>
          </ul>
        </div>
      </Show>

      {/* Per-season monitoring — SERIES ONLY. Movies/Adult have no seasons, and
          the routes behind this panel carry a literal `series` path segment. */}
      <Show when={props.mode === "series"}>
        <SeasonsPanel seriesID={props.item.id} />
      </Show>

      {/* Tags — mutable */}
      <div>
        <p class="mb-1 text-[11px] font-medium uppercase tracking-wide text-muted">Tags</p>
        <div class="mb-2 flex flex-wrap gap-1">
          <For each={props.item.tags ?? []}>
            {(tag) => (
              <span class="inline-flex items-center gap-1 rounded-full bg-surface-2 px-2 py-0.5 text-xs text-fg">
                {tag}
                <button
                  type="button"
                  class="text-muted hover:text-danger"
                  aria-label={`Remove ${tag}`}
                  onClick={() => props.onRemoveTag(tag)}
                >
                  ×
                </button>
              </span>
            )}
          </For>
        </div>
        <div class="flex items-center gap-2">
          <input
            type="text"
            class={`${inputClass} !w-full`}
            list={props.datalistId}
            placeholder="tag label"
            aria-label={`Add tag to ${props.item.title}`}
            value={props.draft}
            onInput={(e) => props.onDraftChange(e.currentTarget.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                props.onAdd();
              }
            }}
          />
          <Button onClick={props.onAdd}>Add</Button>
        </div>
      </div>
    </MediaDetailShell>
  );
};

// LibraryView is one mode's catalog grid. Keyed on props.mode so both resources
// refetch when the shell switches tabs. vocab + tracked load in parallel — vocab
// only feeds the DetailPanel's add-tag autocomplete.
//
// Claude 2026-08-13: optional aspect is the Adult Library tab filter
// (horizontal Scenes / vertical Movies). Omitted = no query param so Mainstream
// and Tag-style callers stay unfiltered. posterAspect is the card/modal/skeleton
// frame (2B). Both go in the resource key and datalistId so a later
// single-instance refactor cannot skip refetch or collide ids.
const LibraryView: Component<{
  mode: Mode;
  initialTier?: string;
  aspect?: "vertical" | "horizontal";
  posterAspect?: string;
}> = (props) => {
  const [vocab, { refetch: refetchVocab }] = createResource(
    () => ({ mode: props.mode, aspect: props.aspect ?? "" }),
    ({ mode }) => fetchTagVocabulary(mode),
  );
  const [tracked, { refetch: refetchTracked }] = createResource(
    () => ({ mode: props.mode, aspect: props.aspect ?? "" }),
    ({ mode, aspect }) =>
      fetchTrackedItems(
        mode,
        aspect === "vertical" || aspect === "horizontal" ? aspect : undefined,
      ),
  );

  // selectedId is the item currently shown in the DetailPanel.
  const [selectedId, setSelectedId] = createSignal<number | null>(null);
  // search filters the grid by title (client-side).
  const [search, setSearch] = createSignal("");
  // genre filters the grid to one genre; "" means all genres.
  const [genre, setGenre] = createSignal("");
  // tier filters the grid to one quality tier; "" means all tiers. Seeded from
  // the shell's ?tier= deep link (a Dashboard storage cell), then plain local
  // state like every other filter here.
  const [tier, setTier] = createSignal(props.initialTier ?? "");
  // sort orders the grid; "title" keeps the server's order.
  const [sort, setSort] = createSignal<SortKey>("title");
  // detailDraft is the add-tag input value in the DetailPanel.
  const [detailDraft, setDetailDraft] = createSignal("");

  const datalistId = () =>
    `library-tag-vocab-${props.mode}${props.aspect ? `-${props.aspect}` : ""}`;

  // refresh re-pulls both vocab (a newly added label may be new to the mode) and
  // the tracked rows after any mutation — same post-mutation behavior as Tag.
  const refresh = async () => {
    await Promise.all([refetchVocab(), refetchTracked()]);
  };

  // Library has no scan button — omit scanFn. The mode-change reset clears the
  // selection, the search, the detail draft, the genre (a genre that exists
  // for Movies may not exist for Series, which would silently empty the grid)
  // AND the tier — including one that arrived as a ?tier= deep link, which is
  // scoped to the mode it was linked for. Sort is mode-independent, so it
  // survives a tab switch.
  const { actionError, act } = useWorkflowActions(
    () => props.mode,
    {
      resetOnModeChange: () => {
        setSelectedId(null);
        setSearch("");
        setDetailDraft("");
        setGenre("");
        setTier("");
      },
      refetch: refresh,
    },
  );

  const submitDetailAdd = (item: TrackedItem) => {
    const label = detailDraft().trim();
    if (!label) return;
    void act(async () => {
      await addTag(props.mode, item.id, label);
      setDetailDraft("");
    });
  };

  const loading = () =>
    (vocab.loading && vocab() === undefined) ||
    (tracked.loading && tracked() === undefined);

  // genreOptions is the de-duplicated, alphabetised union of every genre on the
  // CURRENTLY LOADED mode's items — built from tracked(), never from the already
  // filtered list, or picking a genre would collapse the dropdown to itself.
  const genreOptions = () => {
    const seen = new Set<string>();
    for (const item of tracked() ?? []) {
      for (const g of item.genres ?? []) seen.add(g);
    }
    return Array.from(seen).sort((a, b) => a.localeCompare(b));
  };

  // visibleItems is the whole client-side pipeline: title search → genre filter
  // → tier filter → sort. Items with no genres survive whenever no genre is
  // selected (the early return) — genre enrichment postdates some tracked rows,
  // and those must not vanish from an unfiltered grid.
  //
  // The tier predicate is plain string matching with no "unknown" special case:
  // the backend already folds an uncaptured quality_tier ('') to a literal
  // "unknown" entry in qualityTiers, so a Dashboard Unknown cell's drill-down
  // matches those rows the same way any other tier does. A series carries the
  // DISTINCT set of its episodes' tiers, so includes() is also what makes it
  // match if ANY episode qualifies.
  const visibleItems = () => {
    const q = search().trim().toLowerCase();
    const g = genre();
    const t = tier();
    let items = tracked() ?? [];
    if (q) items = items.filter((item) => item.title.toLowerCase().includes(q));
    if (g) items = items.filter((item) => (item.genres ?? []).includes(g));
    if (t) items = items.filter((item) => (item.qualityTiers ?? []).includes(t));
    if (sort() === "added") {
      // Copy before sorting — `items` may still be the resource's own array.
      // createdAt is fixed-width ISO-8601 UTC, so lexicographic descending IS
      // newest-first; "" (an absent value) is smallest, so it sorts last.
      items = [...items].sort((a, b) =>
        (b.createdAt ?? "").localeCompare(a.createdAt ?? ""),
      );
    }
    return items;
  };

  // selectedItem resolves the currently-selected TrackedItem for the DetailPanel.
  const selectedItem = () => {
    const id = selectedId();
    if (id === null) return null;
    return (tracked() ?? []).find((item) => item.id === id) ?? null;
  };

  return (
    <div>
      <Show when={actionError()}>
        <ErrorText>{actionError()}</ErrorText>
      </Show>
      <Show when={vocab.error}>
        <ErrorText>{(vocab.error as Error)?.message}</ErrorText>
      </Show>
      <Show when={tracked.error}>
        <ErrorText>{(tracked.error as Error)?.message}</ErrorText>
      </Show>

      {/* Autocomplete source for the detail panel's add-tag input. */}
      <datalist id={datalistId()}>
        <For each={vocab() ?? []}>
          {(t) => <option value={t.label} />}
        </For>
      </datalist>

      <Show
        when={!loading()}
        fallback={<MediaGridSkeleton aspect={props.posterAspect} />}
      >
        <Show
          when={tracked() && tracked()!.length > 0}
          fallback={
            <Muted class="mt-4">
              {props.aspect === "vertical"
                ? "No vertical-classified titles yet."
                : "Nothing tracked yet."}
            </Muted>
          }
        >
          {/* Claude 2026-08-13: frame matches Discover FilterSortBar.
              Reason: cinematic leftover chrome; no shared generic — Library
              filtering is a third distinct surface. Inner mb-3 dropped.
              Review if: FilterSortBar's frame class changes. */}
          <div class="mb-4 rounded-xl border border-border bg-surface p-4">
          <div class="flex flex-wrap items-end gap-3">
            <div class="min-w-0 flex-1">
              <label class={labelClass} for="library-search">
                Search
              </label>
              <input
                id="library-search"
                type="text"
                class={`${inputClass} mt-1`}
                placeholder="Search titles…"
                value={search()}
                onInput={(e) => {
                  setSearch(e.currentTarget.value);
                  setSelectedId(null);
                }}
              />
            </div>
            <div class="flex flex-col">
              <label class={labelClass} for="library-genre">
                Genre
              </label>
              <select
                id="library-genre"
                class={`mt-1 ${selectClass}`}
                value={genre()}
                onChange={(e) => {
                  setGenre(e.currentTarget.value);
                  setSelectedId(null);
                }}
              >
                <option value="">All genres</option>
                <For each={genreOptions()}>
                  {(g) => <option value={g}>{g}</option>}
                </For>
              </select>
            </div>
            {/* Quality tier — the visible, clearable control behind the
                Dashboard storage-allocation drill-down. Options are rendered
                from TIER_VALUES, a module-level const resolved before first
                render — unlike genreOptions() above, which is derived from the
                fetched items — so the seeded value still always has an option
                to bind to. */}
            <div class="flex flex-col">
              <label class={labelClass} for="library-tier">
                Quality tier
              </label>
              <select
                id="library-tier"
                class={`mt-1 ${selectClass}`}
                value={tier()}
                onChange={(e) => {
                  setTier(e.currentTarget.value);
                  setSelectedId(null);
                }}
              >
                <option value="">All tiers</option>
                <For each={TIER_VALUES}>
                  {(t) => (
                    <option value={t}>
                      {t.charAt(0).toUpperCase() + t.slice(1)}
                    </option>
                  )}
                </For>
              </select>
            </div>
            <div class="flex flex-col">
              <label class={labelClass} for="library-sort">
                Sort
              </label>
              <select
                id="library-sort"
                class={`mt-1 ${selectClass}`}
                value={sort()}
                onChange={(e) => setSort(e.currentTarget.value as SortKey)}
              >
                <option value="title">Title (A–Z)</option>
                <option value="added">Newest first</option>
              </select>
            </div>
          </div>
          </div>

          <div>
            <div class="min-w-0">
              {/* "nothing tracked" and "nothing matches the filters" are two
                  different states — only the first existed before filters did. */}
              <Show
                when={visibleItems().length > 0}
                fallback={
                  <Muted class="mt-4">No items match this search or genre.</Muted>
                }
              >
                <div class={MEDIA_POSTER_GRID_CLASS}>
                  <For each={visibleItems()}>
                    {(item) => (
                      <PosterCard
                        item={item}
                        mode={props.mode}
                        posterAspect={props.posterAspect}
                        selected={selectedId() === item.id}
                        onClick={() =>
                          setSelectedId((prev) =>
                            prev === item.id ? null : item.id,
                          )
                        }
                      />
                    )}
                  </For>
                </div>
              </Show>
            </div>

            <Show when={selectedItem()}>
              {(item) => (
                <Modal title={item().title} onClose={() => setSelectedId(null)}>
                  <DetailPanel
                    item={item()}
                    mode={props.mode}
                    posterAspect={props.posterAspect}
                    datalistId={datalistId()}
                    draft={detailDraft()}
                    onDraftChange={setDetailDraft}
                    onAdd={() => submitDetailAdd(item())}
                    onRemoveTag={(tag) =>
                      void act(() => removeTag(props.mode, item().id, tag))
                    }
                  />
                </Modal>
              )}
            </Show>
          </div>
        </Show>
      </Show>
    </div>
  );
};

// LibraryMainstream is the Mainstream child under the Library sidebar group.
// It owns only the Series/Movies top tabs; the Mainstream-vs-Adult split now
// belongs to AppShell navigation.
export const LibraryMainstream: Component = () => {
  const [params] = useSearchParams();
  const initialTab: MainstreamMediaTab =
    params.tab === "series" || params.mode === "series" ? "series" : "movies";
  // An unrecognized tier folds to "" so the <select> can never display a value
  // it isn't actually filtering by.
  const initialTier =
    typeof params.tier === "string" && TIER_VALUES.includes(params.tier)
      ? params.tier
      : "";
  const [tab, setTab] = createSignal<MainstreamMediaTab>(initialTab);
  return (
    <div>
      <ScreenTabs
        tabs={MAINSTREAM_MEDIA_TABS}
        current={tab}
        onSelect={(id) => setTab(id as MainstreamMediaTab)}
      />
      <LibraryView mode={tab()} initialTier={initialTier} />
    </div>
  );
};

export const LibraryAdult: Component = () => {
  const [params] = useSearchParams();
  const adultEnabled = useAdultEnabled();
  const lock = useSectionLock();
  const initialTab: AdultMediaTab = params.tab === "movies" ? "movies" : "scenes";
  const [tab, setTab] = createSignal<AdultMediaTab>(initialTab);

  return (
    <Show
      when={adultEnabled()}
      fallback={<Muted class="mt-4">Adult mode is disabled in Settings.</Muted>}
    >
      <ScreenTabs
        tabs={ADULT_MEDIA_TABS}
        current={tab}
        onSelect={(id) => setTab(id as AdultMediaTab)}
      />
      <Show
        when={!lock.isLocked(ADULT_CONTENT_SECTION)}
        fallback={<SectionLockOverlay label={sectionLabel(ADULT_CONTENT_SECTION)} />}
      >
        {/* Claude 2026-08-13: keyed remount per Adult tab.
            Reason: both tabs are mode=adult so useWorkflowActions will not
            reset search/genre/tier/selectedId; a Show swap without keyed
            leaked state and a stale selectedId closed the modal.
            aspect omitted is never passed here — Scenes always sends
            horizontal, Movies vertical — Mainstream still omits it.
            Review if: useWorkflowActions grows a composite reset key. */}
        <Show when={tab()} keyed>
          {(t) => (
            <LibraryView
              mode="adult"
              aspect={t === "movies" ? "vertical" : "horizontal"}
              posterAspect={t === "movies" ? "2 / 3" : "16 / 9"}
            />
          )}
        </Show>
      </Show>
    </Show>
  );
};

// Claude 2026-08-13: AdultMoviesPlaceholder retired — LibraryAdult now mounts
// LibraryView for both Scenes and Movies. Left commented so the empty-state
// copy is recoverable if the Movies tab is reverted to a scaffold.
// Reason: Slice 3 replaces the placeholder with ?aspect=vertical.
// Review if: production still has zero vertical rows and the empty copy
// ("No vertical-classified titles yet.") needs to change.
// const AdultMoviesPlaceholder: Component = () => (
//   <Card title="Adult Movies">
//     <Muted>
//       Adult Movies will use TPDB/catalog adult movie entities. The navigation
//       surface is in place; the async catalog enrichment/data surface will land in
//       a follow-up slice.
//     </Muted>
//   </Card>
// );

// Claude 2026-08-13: unrouted legacy Library shell. Tests mount
// LibraryMainstream / LibraryAdult. AppShell never imported this.
// Reason: Slice 1.5 — ModeTabs Movies/Series/Adult is not a production
// route; keeping it exported let 20 tests cover the wrong shell.
// Review if: a fallback deep-link to /library?mode= is restored.
// export const Library: Component = () => {
//   const [params] = useSearchParams();
//   const initialMode: Mode =
//     params.mode === "series" || params.mode === "adult" ? params.mode : "movies";
//   const initialTier =
//     typeof params.tier === "string" && TIER_VALUES.includes(params.tier)
//       ? params.tier
//       : "";
//   const [mode, setMode] = createSignal<Mode>(initialMode);
//   return (
//     <div>
//       <ModeTabs current={mode} onSelect={setMode} />
//       <LibraryView mode={mode()} initialTier={initialTier} />
//     </div>
//   );
// };
