// Library — browse the tracked Movies/Series catalog. This is the one poster-grid
// browser in the app: it took over the grid half that used to live in Tag.tsx
// (PosterCard, DetailPanel, the client-side title search and the selection/detail
// wiring all moved here verbatim), leaving Tag as the tag-CRUD table it was
// originally documented to be.
//
// Layout, top to bottom: a Movies/Series tab bar over a filter row (title search
// | genre | sort) and a poster grid; selecting a card slides a DetailPanel in at
// w-72 showing genres/cast/tags. Tag mutations in the panel go through the same
// addTag/removeTag + act() path Tag's table uses — only the entry point changed,
// not the mechanics.
//
// ADULT IS DELIBERATELY EXCLUDED (spec Non-Goal 1). Adult keeps its own table
// view at /tag with all three mode tabs; this screen uses ScreenTabs over a local
// Movies|Series TabDef set rather than ModeTabs/MODES, both of which would
// re-introduce Adult. Adult's wire response carries no createdAt either, so there
// is nothing here for it to sort by.
//
// Filter and sort are CLIENT-SIDE by design (spec Non-Goal 4), over the already
// fetched GET /api/modes/{mode}/tracked payload — matching the precedent Tag's
// title search already set. The backend gains no query parameters. Added-date
// sort leans on createdAt being a fixed-width ISO-8601 UTC string, so a plain
// lexicographic compare is a correct chronological one (no Date parsing).

import {
  type Component,
  createResource,
  createSignal,
  For,
  Show,
} from "solid-js";
import type { Mode } from "../api/discover";
import { fetchTitlePoster, tmdbPoster } from "../api/discover";
import {
  type TrackedItem,
  addTag,
  fetchTagVocabulary,
  fetchTrackedItems,
  removeTag,
} from "../api/tag";
import {
  Button,
  ErrorText,
  Muted,
  ScreenTabs,
  type TabDef,
  inputClass,
  labelClass,
} from "../components/ui";
import { useWorkflowActions } from "./workflowHooks";

// LibraryMode is the subset of Mode this screen browses. Adult is absent by
// design, so PosterCard/DetailPanel's Exclude<Mode, "adult"> prop is satisfied
// without a cast.
type LibraryMode = Exclude<Mode, "adult">;

// LIBRARY_MODES is Library's own tab set, deliberately NOT ui.tsx's shared MODES
// (which includes Adult). Kept local so a future mode added to MODES can never
// silently appear here.
const LIBRARY_MODES: TabDef[] = [
  { id: "movies", label: "Movies" },
  { id: "series", label: "Series" },
];

// SortKey selects the grid's ordering. "title" keeps the server's own
// ORDER BY title (no client-side re-sort — SQLite's collation is authoritative);
// "added" sorts by createdAt descending, newest first.
type SortKey = "title" | "added";

const selectClass =
  "rounded-md border border-border bg-bg px-3 py-2 text-sm text-fg outline-none focus:border-accent";

// PosterCard is one grid cell. It lazily fetches the item's TMDB poster and
// renders it; falls back to a grey tile with the first letter of the title when
// the item has no tmdbId or the fetch returns "". Selected state is indicated
// with an accent ring; unselected uses a transparent border.
const PosterCard: Component<{
  item: TrackedItem;
  mode: LibraryMode;
  selected: boolean;
  onClick: () => void;
}> = (props) => {
  // Key the resource on tmdbId — when absent, the source accessor returns
  // undefined and Solid skips the fetch entirely.
  const [posterPath] = createResource(
    () => props.item.tmdbId,
    (id) => fetchTitlePoster(props.mode, id),
  );

  const posterUrl = () => {
    const path = posterPath();
    return path ? tmdbPoster(path) : "";
  };

  return (
    <button
      type="button"
      class="flex w-full flex-col overflow-hidden rounded-lg border-2 bg-surface text-left transition focus:outline-none focus:ring-2 focus:ring-accent"
      classList={{
        "border-accent": props.selected,
        "border-transparent": !props.selected,
      }}
      aria-pressed={props.selected}
      aria-label={props.item.title}
      onClick={props.onClick}
    >
      {/* Poster area — 2:3 aspect ratio */}
      <div class="relative w-full" style="aspect-ratio: 2/3">
        <Show
          when={posterUrl()}
          fallback={
            <div class="flex h-full w-full items-center justify-center bg-surface-2 text-2xl font-bold text-muted">
              {props.item.title.charAt(0).toUpperCase()}
            </div>
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
              {(g) => (
                <span class="rounded bg-surface-2 px-1 py-0.5 text-[10px] text-muted">
                  {g}
                </span>
              )}
            </For>
          </div>
        </Show>
      </div>
    </button>
  );
};

// DetailPanel shows full metadata + editable tags for the selected item.
// Poster is lazy-fetched again (same endpoint, browser caches the image).
// Genres and cast are READ-ONLY. Tags are mutable via act() from the parent.
const DetailPanel: Component<{
  item: TrackedItem;
  mode: LibraryMode;
  datalistId: string;
  draft: string;
  onDraftChange: (v: string) => void;
  onAdd: () => void;
  onRemoveTag: (tag: string) => void;
  onClose: () => void;
}> = (props) => {
  const [posterPath] = createResource(
    () => props.item.tmdbId,
    (id) => fetchTitlePoster(props.mode, id),
  );

  const posterUrl = () => {
    const path = posterPath();
    return path ? tmdbPoster(path) : "";
  };

  return (
    <div class="flex w-72 flex-shrink-0 flex-col rounded-xl border border-border bg-surface p-4 overflow-y-auto">
      {/* Header row */}
      <div class="mb-3 flex items-start justify-between gap-2">
        <div class="min-w-0 flex-1">
          <p class="truncate text-sm font-semibold text-fg">{props.item.title}</p>
          <Show when={props.item.year}>
            <p class="text-xs text-muted">{props.item.year}</p>
          </Show>
          <Show when={props.item.collectionName}>
            <p class="mt-0.5 truncate text-xs text-muted italic">
              {props.item.collectionName}
            </p>
          </Show>
        </div>
        <button
          type="button"
          class="flex-shrink-0 rounded p-1 text-muted hover:text-fg"
          aria-label="Close detail panel"
          onClick={props.onClose}
        >
          {/* Inline × SVG — no icon library */}
          <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
            <path d="M2 2l10 10M12 2L2 12" stroke="currentColor" stroke-width="2" stroke-linecap="round"/>
          </svg>
        </button>
      </div>

      {/* Small poster */}
      <Show when={posterUrl()}>
        <img
          src={posterUrl()}
          alt=""
          loading="lazy"
          class="mb-3 w-full rounded object-cover"
          style="aspect-ratio: 2/3; max-height: 10rem; object-position: top"
        />
      </Show>

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

      {/* Tags — mutable */}
      <div class="flex-1">
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
    </div>
  );
};

// LibraryView is one mode's catalog grid. Keyed on props.mode so both resources
// refetch when the shell switches tabs. vocab + tracked load in parallel — vocab
// only feeds the DetailPanel's add-tag autocomplete.
const LibraryView: Component<{ mode: LibraryMode }> = (props) => {
  const [vocab, { refetch: refetchVocab }] = createResource(
    () => props.mode,
    (m) => fetchTagVocabulary(m),
  );
  const [tracked, { refetch: refetchTracked }] = createResource(
    () => props.mode,
    (m) => fetchTrackedItems(m),
  );

  // selectedId is the item currently shown in the DetailPanel.
  const [selectedId, setSelectedId] = createSignal<number | null>(null);
  // search filters the grid by title (client-side).
  const [search, setSearch] = createSignal("");
  // genre filters the grid to one genre; "" means all genres.
  const [genre, setGenre] = createSignal("");
  // sort orders the grid; "title" keeps the server's order.
  const [sort, setSort] = createSignal<SortKey>("title");
  // detailDraft is the add-tag input value in the DetailPanel.
  const [detailDraft, setDetailDraft] = createSignal("");

  const datalistId = () => `library-tag-vocab-${props.mode}`;

  // refresh re-pulls both vocab (a newly added label may be new to the mode) and
  // the tracked rows after any mutation — same post-mutation behavior as Tag.
  const refresh = async () => {
    await Promise.all([refetchVocab(), refetchTracked()]);
  };

  // Library has no scan button — omit scanFn. The mode-change reset clears the
  // selection, the search, the detail draft AND the genre (a genre that exists
  // for Movies may not exist for Series, which would silently empty the grid).
  // Sort is mode-independent, so it survives a tab switch.
  const { actionError, act } = useWorkflowActions(
    () => props.mode,
    {
      resetOnModeChange: () => {
        setSelectedId(null);
        setSearch("");
        setDetailDraft("");
        setGenre("");
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

  const loading = () => vocab.loading || tracked.loading;

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
  // → sort. Items with no genres survive whenever no genre is selected (the
  // early return) — genre enrichment postdates some tracked rows, and those must
  // not vanish from an unfiltered grid.
  const visibleItems = () => {
    const q = search().trim().toLowerCase();
    const g = genre();
    let items = tracked() ?? [];
    if (q) items = items.filter((item) => item.title.toLowerCase().includes(q));
    if (g) items = items.filter((item) => (item.genres ?? []).includes(g));
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

      <Show when={!loading()} fallback={<Muted class="mt-4">Loading…</Muted>}>
        <Show
          when={tracked() && tracked()!.length > 0}
          fallback={<Muted class="mt-4">Nothing tracked yet.</Muted>}
        >
          {/* Filter row — search | genre | sort. Inside the empty-state gate
              because the genre options are derived from the loaded items. */}
          <div class="mb-3 flex flex-wrap items-end gap-3">
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

          <div class="flex gap-4">
            {/* Left: card grid */}
            <div class="min-w-0 flex-1">
              {/* "nothing tracked" and "nothing matches the filters" are two
                  different states — only the first existed before filters did. */}
              <Show
                when={visibleItems().length > 0}
                fallback={
                  <Muted class="mt-4">No items match this search or genre.</Muted>
                }
              >
                <div class="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
                  <For each={visibleItems()}>
                    {(item) => (
                      <PosterCard
                        item={item}
                        mode={props.mode}
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

            {/* Right: detail panel (when a card is selected) */}
            <Show when={selectedItem()}>
              {(item) => (
                <DetailPanel
                  item={item()}
                  mode={props.mode}
                  datalistId={datalistId()}
                  draft={detailDraft()}
                  onDraftChange={setDetailDraft}
                  onAdd={() => submitDetailAdd(item())}
                  onRemoveTag={(tag) =>
                    void act(() => removeTag(props.mode, item().id, tag))
                  }
                  onClose={() => setSelectedId(null)}
                />
              )}
            </Show>
          </div>
        </Show>
      </Show>
    </div>
  );
};

// Library is the mode-switching shell: a Movies/Series tab bar (never Adult)
// over the matching mode's catalog grid.
export const Library: Component = () => {
  const [mode, setMode] = createSignal<LibraryMode>("movies");
  return (
    <div>
      <ScreenTabs
        tabs={LIBRARY_MODES}
        current={mode}
        onSelect={(id) => setMode(id as LibraryMode)}
      />
      <LibraryView mode={mode()} />
    </div>
  );
};
