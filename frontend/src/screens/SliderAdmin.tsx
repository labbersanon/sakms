// SliderAdmin — the admin UI for creating/editing/reordering/deleting custom
// Discover sliders (task #7; Seerr's CreateSlider/DiscoverSliderEdit/
// FilterSlideover equivalent). Lives as its own Settings section tab
// ("Sliders"), consuming task #5's confirmed CRUD endpoints/DTOs
// (src/api/discoverSliders.ts) — mirrors internal/discoversliders.Store's
// validation rules client-side only as a UX nicety (disabling incompatible
// target options, hiding filter-value inputs for fixed feeds); the backend
// re-validates everything regardless.
//
// No bulk actions (project convention): create/update/delete/enable-toggle
// each act on exactly one slider. Reordering is button-based (up/down), not
// drag-and-drop — simpler to implement/test and satisfies the task's
// "drag-OR-button-based" requirement. (This convention still holds HERE — slider
// CRUD is genuinely single-item. It is not absolute across the whole app: two
// bounded, documented bulk exceptions exist elsewhere — bulk-apply on
// Rename/Dedup/Purge review queues, and bulk-grab in Discover's opt-in Select
// mode — neither of which touches this screen.)

import {
  type Component,
  createEffect,
  createResource,
  createSignal,
  For,
  on,
  Show,
} from "solid-js";
import {
  FILTER_NEEDS_VALUE,
  FILTER_TYPES,
  FILTER_TYPE_LABELS,
  TARGETS,
  createSlider,
  deleteSlider,
  fetchGenres,
  fetchKeywords,
  fetchNetworks,
  fetchSliders,
  fetchStudios,
  reorderSliders,
  updateSlider,
  type FilterType,
  type Slider,
  type SliderTarget,
} from "../api/discoverSliders";
import {
  Button,
  Card,
  ErrorText,
  Muted,
  SaveStatus,
  inputClass,
  labelClass,
  useSaveStatus,
} from "../components/ui";

// targetsAllowedFor mirrors internal/discoversliders.resolveSlider's target
// restriction: studio is movie-catalog-only, network is TV-catalog-only —
// pairing either with the wrong single-catalog target is a permanent
// misconfiguration the backend rejects at resolve time (400). Restricting the
// picker here catches it before save instead of on the first Discover load.
function targetsAllowedFor(filterType: FilterType): SliderTarget[] {
  if (filterType === "studio") return ["movie", "mixed"];
  if (filterType === "network") return ["tv", "mixed"];
  return [...TARGETS];
}

// FilterValuePicker resolves a slider's filter_value: a select over a
// reference list for genre/studio/network, a search-then-pick flow for
// keyword (no fixed keyword list exists), or nothing for the three fixed
// feeds. value/onChange carry the raw stringified TMDB id (or "" when the
// filter type needs none) — the same shape SliderUpsertRequest.filterValue
// stores.
const FilterValuePicker: Component<{
  filterType: () => FilterType;
  target: () => SliderTarget;
  value: () => string;
  onChange: (v: string) => void;
}> = (props) => {
  const genreMode = () => (props.target() === "tv" ? "series" : "movies");
  const [genres] = createResource(
    () => (props.filterType() === "genre" ? genreMode() : undefined),
    fetchGenres,
  );
  const [studios] = createResource(
    () => (props.filterType() === "studio" ? true : undefined),
    fetchStudios,
  );
  const [networks] = createResource(
    () => (props.filterType() === "network" ? true : undefined),
    fetchNetworks,
  );

  // draft is the input's live value; submitted only changes on an explicit
  // Search click/Enter (same draft/submitted split MainstreamDiscover uses in
  // Discover.tsx) — createResource re-fires its fetcher on every change to
  // its source signal, so keying it on the raw keystroke-by-keystroke draft
  // would fire one TMDB-proxying request per character typed.
  const [keywordDraft, setKeywordDraft] = createSignal("");
  const [keywordSubmitted, setKeywordSubmitted] = createSignal("");
  const [keywordResults] = createResource(
    keywordSubmitted,
    (q) => (q.trim() ? fetchKeywords(q.trim()) : Promise.resolve([])),
    { initialValue: [] },
  );
  const submitKeywordSearch = () => setKeywordSubmitted(keywordDraft());

  return (
    <Show when={FILTER_NEEDS_VALUE[props.filterType()]}>
      <label class="mb-2 block">
        <span class={labelClass}>
          {FILTER_TYPE_LABELS[props.filterType()]} value
        </span>
        <Show when={props.filterType() === "genre"}>
          <select
            class={`${inputClass} mt-1`}
            aria-label="Genre"
            value={props.value()}
            onChange={(e) => props.onChange(e.currentTarget.value)}
          >
            <option value="">select a genre…</option>
            <For each={genres() ?? []}>
              {(g) => <option value={String(g.id)}>{g.name}</option>}
            </For>
          </select>
        </Show>
        <Show when={props.filterType() === "studio"}>
          <select
            class={`${inputClass} mt-1`}
            aria-label="Studio"
            value={props.value()}
            onChange={(e) => props.onChange(e.currentTarget.value)}
          >
            <option value="">select a studio…</option>
            <For each={studios() ?? []}>
              {(s) => <option value={String(s.id)}>{s.name}</option>}
            </For>
          </select>
        </Show>
        <Show when={props.filterType() === "network"}>
          <select
            class={`${inputClass} mt-1`}
            aria-label="Network"
            value={props.value()}
            onChange={(e) => props.onChange(e.currentTarget.value)}
          >
            <option value="">select a network…</option>
            <For each={networks() ?? []}>
              {(n) => <option value={String(n.id)}>{n.name}</option>}
            </For>
          </select>
        </Show>
        <Show when={props.filterType() === "keyword"}>
          <div class="mt-1 flex gap-2">
            <input
              type="text"
              class={inputClass}
              placeholder="search keywords…"
              aria-label="Keyword search"
              value={keywordDraft()}
              onInput={(e) => setKeywordDraft(e.currentTarget.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  submitKeywordSearch();
                }
              }}
            />
            <Button class="!px-3 !py-1 !text-xs" onClick={submitKeywordSearch}>
              Search
            </Button>
          </div>
          <Show when={(keywordResults() ?? []).length > 0}>
            <ul class="mt-1 flex flex-wrap gap-1">
              <For each={keywordResults()}>
                {(k) => (
                  <li>
                    <button
                      type="button"
                      class="rounded-md border border-border bg-surface-2 px-2 py-1 text-xs text-fg hover:opacity-90"
                      classList={{
                        "!bg-accent !text-accent-fg": props.value() === String(k.id),
                      }}
                      onClick={() => props.onChange(String(k.id))}
                    >
                      {k.name}
                    </button>
                  </li>
                )}
              </For>
            </ul>
          </Show>
          <Show when={props.value()}>
            <Muted class="mt-1">selected keyword id: {props.value()}</Muted>
          </Show>
        </Show>
      </label>
    </Show>
  );
};

// SliderForm creates a new slider (slider prop undefined) or edits an
// existing one (slider prop present) — the same form either way, following
// ConnectionRow's seed-from-props-at-mount pattern.
const SliderForm: Component<{
  slider?: Slider;
  onSaved: () => void;
  onCancel: () => void;
}> = (props) => {
  const [title, setTitle] = createSignal(props.slider?.title ?? "");
  const [filterType, setFilterType] = createSignal<FilterType>(
    (props.slider?.filterType as FilterType) ?? "upcoming",
  );
  const [target, setTarget] = createSignal<SliderTarget>(
    (props.slider?.target as SliderTarget) ?? "mixed",
  );
  const [filterValue, setFilterValue] = createSignal(
    props.slider?.filterValue ?? "",
  );
  const [enabled, setEnabled] = createSignal(props.slider?.enabled ?? true);
  const status = useSaveStatus();

  // Changing filter type can make the current target incompatible
  // (studio→movie-only, network→tv-only) — auto-correct rather than let the
  // operator save a combination the backend will reject at resolve time.
  createEffect(
    on(filterType, (ft) => {
      const allowed = targetsAllowedFor(ft);
      if (!allowed.includes(target())) setTarget(allowed[0]!);
    }),
  );

  // Clearing filterValue when the operator actually SWITCHES filter type
  // (defer: true — skip the initial run, which would otherwise wipe the
  // value this form just seeded from props.slider on mount) prevents a
  // stale id surviving a type switch — e.g. a studio id silently getting
  // saved as a network id after the operator picks "Network" without first
  // re-selecting a value. The backend would accept it as a valid int; this
  // is a data-quality guard, not a validation the API would catch.
  createEffect(
    on(filterType, () => setFilterValue(""), { defer: true }),
  );

  const save = async () => {
    if (!title().trim()) {
      status.failed(new Error("title is required"));
      return;
    }
    const needsValue = FILTER_NEEDS_VALUE[filterType()];
    if (needsValue && !filterValue().trim()) {
      status.failed(
        new Error(`select a ${FILTER_TYPE_LABELS[filterType()].toLowerCase()} value first`),
      );
      return;
    }
    const body = {
      title: title().trim(),
      filterType: filterType(),
      filterValue: needsValue ? filterValue() : "",
      target: target(),
      enabled: enabled(),
    };
    try {
      if (props.slider) {
        await updateSlider(props.slider.id, body);
      } else {
        await createSlider(body);
      }
      props.onSaved();
    } catch (e) {
      status.failed(e);
    }
  };

  return (
    <div class="mt-3 rounded-md border border-dashed border-border p-3">
      <form onSubmit={(e) => (e.preventDefault(), void save())}>
        <label class="mb-2 block">
          <span class={labelClass}>Title</span>
          <input
            type="text"
            class={`${inputClass} mt-1`}
            aria-label="Slider title"
            value={title()}
            onInput={(e) => setTitle(e.currentTarget.value)}
          />
        </label>
        <div class="grid gap-3 sm:grid-cols-2">
          <label class="block">
            <span class={labelClass}>Filter type</span>
            <select
              class={`${inputClass} mt-1`}
              aria-label="Filter type"
              value={filterType()}
              onChange={(e) => setFilterType(e.currentTarget.value as FilterType)}
            >
              <For each={FILTER_TYPES}>
                {(ft) => <option value={ft}>{FILTER_TYPE_LABELS[ft]}</option>}
              </For>
            </select>
          </label>
          <label class="block">
            <span class={labelClass}>Target</span>
            <select
              class={`${inputClass} mt-1`}
              aria-label="Target"
              value={target()}
              onChange={(e) => setTarget(e.currentTarget.value as SliderTarget)}
            >
              <For each={targetsAllowedFor(filterType())}>
                {(t) => <option value={t}>{t}</option>}
              </For>
            </select>
          </label>
        </div>
        <FilterValuePicker
          filterType={filterType}
          target={target}
          value={filterValue}
          onChange={setFilterValue}
        />
        <label class="mb-2 flex items-center gap-2">
          <input
            type="checkbox"
            aria-label="Slider enabled"
            checked={enabled()}
            onChange={(e) => setEnabled(e.currentTarget.checked)}
          />
          <span class="text-sm text-fg">Enabled</span>
        </label>
        <div class="mt-2 flex items-center gap-2">
          <Button variant="primary" type="submit">
            {props.slider ? "Save changes" : "Create slider"}
          </Button>
          <Button onClick={props.onCancel}>Cancel</Button>
          <SaveStatus text={status.status().text} error={status.status().error} />
        </div>
      </form>
    </div>
  );
};

// SliderRow is one existing slider's list entry: position controls, summary,
// an inline enabled toggle (immediate save, no separate form), Edit/Delete.
const SliderRow: Component<{
  slider: Slider;
  isFirst: boolean;
  isLast: boolean;
  editing: boolean;
  onMoveUp: () => void;
  onMoveDown: () => void;
  onEdit: () => void;
  onDelete: () => void;
  onToggleEnabled: () => void;
}> = (props) => (
  <li class="flex items-center gap-3 border-b border-border/60 py-2">
    <div class="flex flex-col gap-0.5">
      <button
        type="button"
        aria-label={`Move ${props.slider.title} up`}
        class="rounded border border-border px-1 text-xs text-fg disabled:opacity-30"
        disabled={props.isFirst}
        onClick={props.onMoveUp}
      >
        ▲
      </button>
      <button
        type="button"
        aria-label={`Move ${props.slider.title} down`}
        class="rounded border border-border px-1 text-xs text-fg disabled:opacity-30"
        disabled={props.isLast}
        onClick={props.onMoveDown}
      >
        ▼
      </button>
    </div>
    <div class="min-w-0 flex-1">
      <div class="truncate text-sm text-fg">{props.slider.title}</div>
      <div class="truncate text-xs text-muted">
        {FILTER_TYPE_LABELS[props.slider.filterType as FilterType] ??
          props.slider.filterType}
        {props.slider.filterValue ? ` · ${props.slider.filterValue}` : ""} ·{" "}
        {props.slider.target}
      </div>
    </div>
    <label class="flex items-center gap-1 text-xs text-muted">
      <input
        type="checkbox"
        aria-label={`${props.slider.title} enabled`}
        checked={props.slider.enabled}
        onChange={props.onToggleEnabled}
      />
      enabled
    </label>
    <div class="flex gap-1">
      <Button class="!px-2 !py-1 !text-xs" onClick={props.onEdit}>
        {props.editing ? "Editing…" : "Edit"}
      </Button>
      <Button class="!px-2 !py-1 !text-xs" onClick={props.onDelete}>
        Delete
      </Button>
    </div>
  </li>
);

// SliderAdminSection is the Settings "Sliders" tab's whole panel.
export const SliderAdminSection: Component = () => {
  const [sliders, { refetch }] = createResource(fetchSliders, {
    initialValue: [],
  });
  const [editing, setEditing] = createSignal<number | "new" | null>(null);
  const [listError, setListError] = createSignal("");

  const closeForm = () => setEditing(null);
  const afterSave = () => {
    closeForm();
    void refetch();
  };

  const move = async (id: number, direction: -1 | 1) => {
    const list = sliders() ?? [];
    const idx = list.findIndex((s) => s.id === id);
    const swapWith = idx + direction;
    if (idx < 0 || swapWith < 0 || swapWith >= list.length) return;
    const ids = list.map((s) => s.id);
    [ids[idx], ids[swapWith]] = [ids[swapWith]!, ids[idx]!];
    setListError("");
    try {
      await reorderSliders(ids);
      await refetch();
    } catch (e) {
      setListError((e as Error).message);
    }
  };

  const remove = async (slider: Slider) => {
    if (!confirm(`Delete the "${slider.title}" slider?`)) return;
    setListError("");
    try {
      await deleteSlider(slider.id);
      if (editing() === slider.id) closeForm();
      await refetch();
    } catch (e) {
      setListError((e as Error).message);
    }
  };

  const toggleEnabled = async (slider: Slider) => {
    setListError("");
    try {
      await updateSlider(slider.id, {
        title: slider.title,
        filterType: slider.filterType,
        filterValue: slider.filterValue ?? "",
        target: slider.target,
        enabled: !slider.enabled,
      });
      await refetch();
    } catch (e) {
      setListError((e as Error).message);
    }
  };

  const editingSlider = (): Slider | undefined => {
    const e = editing();
    if (e === null || e === "new") return undefined;
    return (sliders() ?? []).find((s) => s.id === e);
  };

  return (
    <Card title="Custom Discover sliders">
      <Muted class="mb-3">
        Admin-defined rows that appear on Discover alongside the built-in
        Trending/Popular rows — filter by genre, keyword, studio, network, or
        one of the fixed feeds, restrict to movies/TV/both, and control
        display order.
      </Muted>
      <Show when={sliders.error}>
        <ErrorText>{(sliders.error as Error)?.message}</ErrorText>
      </Show>
      <Show when={listError()}>
        <ErrorText>{listError()}</ErrorText>
      </Show>
      <Show
        when={(sliders() ?? []).length > 0}
        fallback={<Muted>No custom sliders yet.</Muted>}
      >
        <ul>
          <For each={sliders()}>
            {(slider, i) => (
              <SliderRow
                slider={slider}
                isFirst={i() === 0}
                isLast={i() === (sliders() ?? []).length - 1}
                editing={editing() === slider.id}
                onMoveUp={() => void move(slider.id, -1)}
                onMoveDown={() => void move(slider.id, 1)}
                onEdit={() => setEditing(slider.id)}
                onDelete={() => void remove(slider)}
                onToggleEnabled={() => void toggleEnabled(slider)}
              />
            )}
          </For>
        </ul>
      </Show>

      <Show
        when={editing() !== null}
        fallback={
          <div class="mt-3">
            <Button variant="primary" onClick={() => setEditing("new")}>
              + New slider
            </Button>
          </div>
        }
      >
        <SliderForm
          slider={editingSlider()}
          onSaved={afterSave}
          onCancel={closeForm}
        />
      </Show>
    </Card>
  );
};
