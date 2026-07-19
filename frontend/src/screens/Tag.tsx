// Tag — direct CRUD on a tracked item's tags, ported verbatim from the
// vanilla-JS frontend (internal/web/static/index.html's renderTag). Layout, top
// to bottom: a mode tab bar (Movies/Series/Adult) over a table with one row per
// tracked item — Title / Tags (removable chips) / Add tag (an autocomplete
// input + Add button). A shared <datalist> feeds the input the mode's existing
// tag vocabulary.
//
// DELIBERATELY SIMPLER than Rename/Purge/Dedup: there is NO staged
// scan→propose→apply queue here — add and remove commit immediately, then the
// view refetches both vocab and tracked (mirroring the old renderTag's full
// re-render). This is verbatim old behavior, not a regression; the backend's
// addItemTagHandler/addSceneTagHandler are single, immediately-committed actions
// by design (see internal/api/tag.go).
//
// No-bulk invariant (Acceptance Criterion 6): the unit of action is ONE
// (item, tag) pair. Every chip's × removes one tag from one item; every Add
// assigns one tag to one item. There is no add-to-all / clear-all affordance
// anywhere — the risk is lower here than in the deleting workflows, but the
// invariant still holds and a test asserts it.
//
// CRITICAL per-mode endpoint routing (see src/api/tag.ts + internal/api/tag.go):
// Movies/Series hit the GENERIC item-tag routes; Adult hits its OWN DEDICATED
// scene-tag routes (the generic routes 400 for Adult). That branching lives
// entirely in the api/tag.ts helpers — this component just passes props.mode
// through, so it is genuinely mode-agnostic.
//
// Grid view (Movies/Series only — Adult always uses table):
// A "Grid" / "Table" toggle persists per-mode in localStorage. The grid shows
// poster cards (lazy-fetched via fetchTitlePoster) with a search filter. When a
// card is selected a DetailPanel slides in at w-72 showing genres/cast/tags.
// All tag mutations in the panel go through the same act() path as the table.

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
  ModeTabs,
  Muted,
  inputClass,
} from "../components/ui";
import { useWorkflowActions } from "./workflowHooks";

// ViewMode controls whether the tag list renders as a grid or table.
// Adult always uses "table"; Movies/Series default to "table" (localStorage-
// persisted per mode — absent/invalid value → "table" so tests without
// localStorage setup keep working).
type ViewMode = "table" | "grid";

// readViewMode reads the stored preference for one mode from localStorage.
// Returns "table" for any absent or invalid stored value.
function readViewMode(mode: Mode): ViewMode {
  try {
    const v = localStorage.getItem(`sakms.tag.viewmode.${mode}`);
    return v === "grid" ? "grid" : "table";
  } catch {
    return "table";
  }
}

// writeViewMode persists a view-mode preference for one mode.
function writeViewMode(mode: Mode, vm: ViewMode): void {
  try {
    localStorage.setItem(`sakms.tag.viewmode.${mode}`, vm);
  } catch {
    // storage errors are silent — the in-memory signal still works
  }
}

// PosterCard is one grid cell. It lazily fetches the item's TMDB poster and
// renders it; falls back to a grey tile with the first letter of the title when
// the item has no tmdbId or the fetch returns "". Selected state is indicated
// with an accent ring; unselected uses a transparent border.
const PosterCard: Component<{
  item: TrackedItem;
  mode: Exclude<Mode, "adult">;
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
  mode: Exclude<Mode, "adult">;
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

// TagView is one mode's tag editor. Keyed on props.mode so both resources
// refetch when the shell switches tabs. vocab + tracked load in parallel.
const TagView: Component<{ mode: Mode }> = (props) => {
  const [vocab, { refetch: refetchVocab }] = createResource(
    () => props.mode,
    (m) => fetchTagVocabulary(m),
  );
  const [tracked, { refetch: refetchTracked }] = createResource(
    () => props.mode,
    (m) => fetchTrackedItems(m),
  );
  // draft maps a tracked item id → the text currently typed in its add-tag
  // input. Cleared on a successful add and on mode switch so a stale draft never
  // leaks onto another mode's rows.
  const [draft, setDraft] = createSignal<Record<number, string>>({});

  // Grid view state — Movies/Series only. Adult always uses table.
  // Initialized from localStorage; each mode remembers its own preference.
  const [viewMode, setViewMode] = createSignal<ViewMode>(
    readViewMode(props.mode),
  );
  // selectedId is the item currently shown in the DetailPanel (grid view only).
  const [selectedId, setSelectedId] = createSignal<number | null>(null);
  // search filters the grid by title (client-side).
  const [search, setSearch] = createSignal("");
  // detailDraft is the add-tag input value in the DetailPanel.
  const [detailDraft, setDetailDraft] = createSignal("");

  const datalistId = () => `tag-vocab-${props.mode}`;

  // refresh re-pulls both vocab (a newly added label may be new to the mode)
  // and the tracked rows after any mutation — verbatim old renderTag behavior.
  const refresh = async () => {
    await Promise.all([refetchVocab(), refetchTracked()]);
  };

  // Tag has no scan button — omit scanFn. act wraps the shared error-capture
  // and mode-change cleanup; refetch calls refresh() which refetches both
  // resources in parallel (vocab + tracked), matching the old renderTag behavior.
  const { actionError, act } = useWorkflowActions(
    () => props.mode,
    {
      resetOnModeChange: () => {
        setDraft({});
        setSelectedId(null);
        setSearch("");
        setDetailDraft("");
        // Re-read per-mode localStorage preference when mode switches.
        setViewMode(readViewMode(props.mode));
      },
      refetch: refresh,
    },
  );

  const submitAdd = (item: TrackedItem) => {
    const label = (draft()[item.id] ?? "").trim();
    if (!label) return;
    void act(async () => {
      await addTag(props.mode, item.id, label);
      setDraft((prev) => ({ ...prev, [item.id]: "" }));
    });
  };

  const submitDetailAdd = (item: TrackedItem) => {
    const label = detailDraft().trim();
    if (!label) return;
    void act(async () => {
      await addTag(props.mode, item.id, label);
      setDetailDraft("");
    });
  };

  const loading = () => vocab.loading || tracked.loading;

  // filteredItems applies the grid search filter (case-insensitive title match).
  const filteredItems = () => {
    const q = search().trim().toLowerCase();
    const items = tracked() ?? [];
    if (!q) return items;
    return items.filter((item) =>
      item.title.toLowerCase().includes(q),
    );
  };

  // selectedItem resolves the currently-selected TrackedItem for the DetailPanel.
  const selectedItem = () => {
    const id = selectedId();
    if (id === null) return null;
    return (tracked() ?? []).find((item) => item.id === id) ?? null;
  };

  // setViewModeAndPersist updates the signal AND writes to localStorage.
  const setViewModeAndPersist = (vm: ViewMode) => {
    setViewMode(vm);
    writeViewMode(props.mode, vm);
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

      {/* Autocomplete source for every row's add-tag input. */}
      <datalist id={datalistId()}>
        <For each={vocab() ?? []}>
          {(t) => <option value={t.label} />}
        </For>
      </datalist>

      {/* Grid/Table toggle — Movies/Series only. Adult always uses table. */}
      <Show when={props.mode !== "adult"}>
        <div class="mb-3 flex items-center gap-1">
          <button
            type="button"
            class="rounded-md px-3 py-1.5 text-sm font-medium transition"
            classList={{
              "bg-accent text-accent-fg": viewMode() === "table",
              "bg-surface-2 text-muted hover:text-fg": viewMode() !== "table",
            }}
            onClick={() => setViewModeAndPersist("table")}
          >
            Table
          </button>
          <button
            type="button"
            class="rounded-md px-3 py-1.5 text-sm font-medium transition"
            classList={{
              "bg-accent text-accent-fg": viewMode() === "grid",
              "bg-surface-2 text-muted hover:text-fg": viewMode() !== "grid",
            }}
            onClick={() => setViewModeAndPersist("grid")}
          >
            Grid
          </button>
        </div>
      </Show>

      <Show when={!loading()} fallback={<Muted class="mt-4">Loading…</Muted>}>
        <Show
          when={tracked() && tracked()!.length > 0}
          fallback={<Muted class="mt-4">Nothing tracked yet.</Muted>}
        >
          {/* Grid view — Movies/Series only */}
          <Show when={props.mode !== "adult" && viewMode() === "grid"}>
            <div class="flex gap-4">
              {/* Left: search + card grid */}
              <div class="min-w-0 flex-1">
                <input
                  type="text"
                  class={`${inputClass} mb-3`}
                  placeholder="Search titles…"
                  value={search()}
                  onInput={(e) => {
                    setSearch(e.currentTarget.value);
                    setSelectedId(null);
                  }}
                />
                <div class="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
                  <For each={filteredItems()}>
                    {(item) => (
                      <PosterCard
                        item={item}
                        mode={props.mode as Exclude<Mode, "adult">}
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
              </div>

              {/* Right: detail panel (when a card is selected) */}
              <Show when={selectedItem()}>
                {(item) => (
                  <DetailPanel
                    item={item()}
                    mode={props.mode as Exclude<Mode, "adult">}
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

          {/* Table view — always shown for Adult; shown for Movies/Series when
              viewMode is "table". This block is the original unchanged JSX. */}
          <Show when={props.mode === "adult" || viewMode() === "table"}>
            <div class="mt-2 overflow-x-auto">
              <table class="w-full text-left text-sm">
                <thead>
                  <tr class="border-b border-border text-xs uppercase tracking-wide text-muted">
                    <th class="px-2 py-2 font-medium">Title</th>
                    <th class="px-2 py-2 font-medium">Tags</th>
                    <th class="px-2 py-2 font-medium">Add tag</th>
                  </tr>
                </thead>
                <tbody>
                  <For each={tracked()}>
                    {(item) => (
                      <tr class="border-b border-border/60 align-top">
                        <td class="px-2 py-2 text-fg">
                          {item.title}
                          <Show when={item.collectionName}>
                            <div class="text-xs text-muted">
                              {item.collectionName}
                            </div>
                          </Show>
                          <Show when={(item.genres ?? []).length > 0}>
                            <div class="mt-0.5 flex flex-wrap gap-1">
                              <For each={item.genres}>
                                {(g) => (
                                  <span class="rounded bg-surface-2 px-1.5 py-0.5 text-xs text-muted">
                                    {g}
                                  </span>
                                )}
                              </For>
                            </div>
                          </Show>
                        </td>
                        <td class="px-2 py-2">
                          <div class="flex flex-wrap gap-1">
                            <For each={item.tags ?? []}>
                              {(tag) => (
                                <span class="inline-flex items-center gap-1 rounded-full bg-surface-2 px-2 py-0.5 text-xs text-fg">
                                  {tag}
                                  <button
                                    type="button"
                                    class="text-muted hover:text-danger"
                                    aria-label={`Remove ${tag}`}
                                    onClick={() =>
                                      void act(() =>
                                        removeTag(
                                          props.mode,
                                          item.id,
                                          tag,
                                        ),
                                      )
                                    }
                                  >
                                    ×
                                  </button>
                                </span>
                              )}
                            </For>
                          </div>
                        </td>
                        <td class="px-2 py-2">
                          <div class="flex items-center gap-2">
                            <input
                              type="text"
                              class={`${inputClass} !w-40`}
                              list={datalistId()}
                              placeholder="tag label"
                              aria-label={`Add tag to ${item.title}`}
                              value={draft()[item.id] ?? ""}
                              onInput={(e) =>
                                setDraft((prev) => ({
                                  ...prev,
                                  [item.id]: e.currentTarget.value,
                                }))
                              }
                              onKeyDown={(e) => {
                                if (e.key === "Enter") {
                                  e.preventDefault();
                                  submitAdd(item);
                                }
                              }}
                            />
                            <Button onClick={() => submitAdd(item)}>Add</Button>
                          </div>
                        </td>
                      </tr>
                    )}
                  </For>
                </tbody>
              </table>
            </div>
          </Show>
        </Show>
      </Show>
    </div>
  );
};

// Tag is the mode-switching shell: tab bar (Movies/Series/Adult) over the
// matching mode's tag editor.
export const Tag: Component = () => {
  const [mode, setMode] = createSignal<Mode>("movies");
  return (
    <div>
      <ModeTabs current={mode} onSelect={setMode} />
      <TagView mode={mode()} />
    </div>
  );
};
