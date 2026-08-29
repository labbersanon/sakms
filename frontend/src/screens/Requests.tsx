// Requests — a cross-mode request-status WORKLIST (F4), not a fourth grab view.
//
// What it adds over the two existing, deliberately-narrow sibling tabs (all
// three live under the Queue sidebar entry):
//   - the Calendar tab's History view is a raw, per-mode grab log (one mode at
//     a time, read-only).
//   - the Downloads tab is the live download-client queue status.
// Neither rolls up state ACROSS modes, and neither surfaces what's still
// MISSING. The Requests tab does both: one row per title spanning Movies/Series/Adult,
// each tagged In Library / Pending (queued grab) / Pending Retry / Scheduled.
// Missing is NOT one of those tags — it is a Series-only missing-episode count
// that co-occurs with any of them. Pure derive-on-read (GET /api/requests).
//
// Status honesty: "Pending" is a queued grab awaiting first search/download;
// live transfer progress lives on the Downloads tab, so there is no
// "Downloading" filter option (rare Downloading badges still appear under All).
// Mode chips follow MODES order (All, Movies, Series, Adult). Below them: a
// row with Search (left) and Status dropdown (right). "Has Missing Episodes"
// stays a boolean chip under that row. Search is local to this tab.

import {
  type Component,
  createMemo,
  createResource,
  createSignal,
  For,
  Show,
} from "solid-js";
import {
  type ExcludeTitleRequest,
  type RequestStatusResponse,
  excludeTitle,
  excludeTitlesBatch,
  fetchRequests,
} from "../api/requests";
import type { DiscoverItem } from "../api/discover";
import { Button, ErrorText, FILTER_BAR_FIELDS_CLASS, MODES, Muted, SelectField } from "../components/ui";
import { type GrabTarget, GrabDialog } from "./discover/shared";
import { type DetailTarget, DetailPopup } from "./discover/DetailPopup";
import { useBulkSelection } from "./workflowHooks";
import { matchesQueueSearch, QueueSearchField } from "./queueSearch";

type RequestItem = RequestStatusResponse["items"][number];

const MODE_LABELS: Record<string, string> = {
  movies: "Movies",
  series: "Series",
  adult: "Adult",
};

// REQUEST_STATUS_ORDER is lifecycle order for the status dropdown. Presence
// is still data-derived (only statuses the backend emitted appear); this list
// only orders them and excludes "Downloading" (redundant with the Downloads
// Queue tab).
const REQUEST_STATUS_ORDER = [
  "Pending",
  "Pending Retry",
  "Scheduled",
  "In Library",
] as const;

// FilterChips is "All + one per distinct value" for the mode row.
const FilterChips: Component<{
  values: string[];
  selected: string | null;
  onSelect: (v: string | null) => void;
  labelOf?: (v: string) => string;
}> = (props) => (
  <div class="flex flex-wrap gap-1">
    <button
      type="button"
      class="rounded-md px-3 py-1 text-xs font-medium transition"
      classList={{
        "bg-accent text-accent-fg": props.selected === null,
        "bg-surface-2 text-muted hover:text-fg": props.selected !== null,
      }}
      onClick={() => props.onSelect(null)}
    >
      All
    </button>
    <For each={props.values}>
      {(v) => (
        <button
          type="button"
          class="rounded-md px-3 py-1 text-xs font-medium transition"
          classList={{
            "bg-accent text-accent-fg": props.selected === v,
            "bg-surface-2 text-muted hover:text-fg": props.selected !== v,
          }}
          onClick={() => props.onSelect(v)}
        >
          {props.labelOf ? props.labelOf(v) : v}
        </button>
      )}
    </For>
  </div>
);

function detailTargetFor(item: RequestItem): DetailTarget | null {
  if (item.mode === "adult" || !item.tmdbId) return null;
  const mode = item.mode === "series" ? "series" : "movies";
  const discoverItem: DiscoverItem = {
    id: item.tmdbId,
    title: item.title,
    posterPath: "",
    overview: "",
    releaseDate: "",
    voteAverage: 0,
    mediaType: mode === "series" ? "tv" : "movie",
  };
  return { mode, item: discoverItem };
}

function keyOf(item: RequestItem): string {
  return `${item.mode}:${item.tmdbId}:${item.title}`;
}

function excludeReqFor(item: RequestItem): ExcludeTitleRequest {
  return {
    mode: item.mode,
    tmdbId: item.tmdbId > 0 ? item.tmdbId : undefined,
    title: item.title,
  };
}

function retryDuration(ms: number): string {
  const hours = Math.round(ms / (60 * 60 * 1000));
  if (hours < 1) return "under an hour";
  if (hours < 24) return `${hours}h`;
  return `${Math.round(hours / 24)}d`;
}

function retryBlurb(item: RequestItem): string | null {
  const parts: string[] = [];
  if (item.retryAfter) {
    const ms = new Date(item.retryAfter).getTime() - Date.now();
    if (!Number.isNaN(ms)) {
      parts.push(ms <= 0 ? "Retry due" : `Retrying in ${retryDuration(ms)}`);
    }
  }
  if (item.retryReason) parts.push(item.retryReason);
  return parts.length > 0 ? parts.join(" — ") : null;
}

function scheduledBlurb(item: RequestItem): string | null {
  if (!item.holdUntil) return null;
  return `Held until ${item.holdUntil.slice(0, 10)} — its release date`;
}

function statusBlurb(item: RequestItem): string | null {
  if (item.status === "Scheduled") return scheduledBlurb(item);
  if (item.status === "Pending Retry") return retryBlurb(item);
  return null;
}

export const Requests: Component = () => {
  const [data, { refetch }] = createResource(fetchRequests);
  const [search, setSearch] = createSignal("");
  const [statusFilter, setStatusFilter] = createSignal<string | null>(null);
  const [modeFilter, setModeFilter] = createSignal<string | null>(null);
  const [missingOnly, setMissingOnly] = createSignal(false);
  const [grabTarget, setGrabTarget] = createSignal<GrabTarget | null>(null);
  const [detailTarget, setDetailTarget] = createSignal<DetailTarget | null>(null);
  const [actionError, setActionError] = createSignal<string | null>(null);
  const selection = useBulkSelection<string>();

  const items = () => data()?.items ?? [];

  const statuses = createMemo(() => {
    const present = new Set(items().map((i) => i.status).filter(Boolean));
    return REQUEST_STATUS_ORDER.filter((s) => present.has(s));
  });
  const modes = createMemo(() => {
    const present = new Set(items().map((i) => i.mode).filter(Boolean));
    return MODES.map((m) => m.id).filter((id) => present.has(id));
  });

  const filtered = () =>
    items().filter((i) => {
      if (statusFilter() !== null && i.status !== statusFilter()) return false;
      if (modeFilter() !== null && i.mode !== modeFilter()) return false;
      if (missingOnly() && i.missingCount <= 0) return false;
      return matchesQueueSearch(
        search(),
        i.title,
        i.status,
        i.mode,
        MODE_LABELS[i.mode],
      );
    });

  const openRow = (item: RequestItem) => {
    const target = detailTargetFor(item);
    if (target) setDetailTarget(target);
  };

  const filteredKeys = (): string[] => filtered().map(keyOf);
  const allSelected = (): boolean => {
    const keys = filteredKeys();
    return keys.length > 0 && keys.every((k) => selection.has(k));
  };
  const toggleSelectAll = (): void => {
    if (allSelected()) selection.clear();
    else selection.selectAll(filteredKeys());
  };

  const removeOne = async (item: RequestItem): Promise<void> => {
    if (
      !confirm(
        `Permanently exclude “${item.title}” from Requests? It will stop appearing here — you can still grab it manually from Discover if you change your mind.`,
      )
    )
      return;
    setActionError(null);
    try {
      await excludeTitle(excludeReqFor(item));
      selection.clear();
      await refetch();
    } catch (err) {
      setActionError((err as Error).message);
    }
  };

  const removeSelected = async (): Promise<void> => {
    const chosen = filtered().filter((i) => selection.has(keyOf(i)));
    if (chosen.length === 0) return;
    if (
      !confirm(
        `Permanently exclude ${chosen.length} selected title(s) from Requests? They will stop appearing here — you can still grab any of them manually from Discover if you change your mind.`,
      )
    )
      return;
    setActionError(null);
    try {
      await excludeTitlesBatch(chosen.map(excludeReqFor));
      selection.clear();
      await refetch();
    } catch (err) {
      setActionError((err as Error).message);
    }
  };

  return (
    <div>
      <Show when={data.error}>
        <ErrorText>{(data.error as Error)?.message}</ErrorText>
      </Show>

      <div class="mb-3 flex flex-col gap-2">
        <Show when={modes().length > 1}>
          <FilterChips
            values={modes()}
            selected={modeFilter()}
            onSelect={setModeFilter}
            labelOf={(m) => MODE_LABELS[m] ?? m}
          />
        </Show>
        <div class={FILTER_BAR_FIELDS_CLASS}>
          <QueueSearchField
            id="requests-search"
            value={search()}
            onInput={setSearch}
            placeholder="Search titles, status, mode…"
          />
          <SelectField
            id="requests-filter-status"
            label="Status"
            value={statusFilter() ?? ""}
            onChange={(v) => setStatusFilter(v === "" ? null : v)}
          >
            <option value="">All</option>
            <For each={statuses()}>{(s) => <option value={s}>{s}</option>}</For>
          </SelectField>
        </div>
        <div class="flex flex-wrap gap-1">
          <button
            type="button"
            class="rounded-md px-3 py-1 text-xs font-medium transition"
            classList={{
              "bg-accent text-accent-fg": missingOnly(),
              "bg-surface-2 text-muted hover:text-fg": !missingOnly(),
            }}
            onClick={() => setMissingOnly((v) => !v)}
          >
            Has Missing Episodes
          </button>
        </div>
      </div>

      <Show when={selection.size() > 0}>
        <div class="mb-3">
          <Button variant="primary" onClick={() => void removeSelected()}>
            Remove Selected ({selection.size()})
          </Button>
        </div>
      </Show>
      <Show when={actionError()}>
        <ErrorText>{actionError()}</ErrorText>
      </Show>

      <Show when={!data.loading} fallback={<Muted>Loading…</Muted>}>
        <Show
          when={filtered().length > 0}
          fallback={<Muted>No requests match this filter.</Muted>}
        >
          <label class="mb-2 flex items-center gap-2 text-xs text-muted">
            <input
              type="checkbox"
              aria-label="Select all"
              checked={allSelected()}
              onChange={toggleSelectAll}
            />
            Select all
          </label>
          <ul class="flex flex-col gap-2">
            <For each={filtered()}>
              {(item) => {
                const target = detailTargetFor(item);
                return (
                  <li
                    class="flex items-center gap-3 rounded-md border border-border bg-surface p-3"
                    classList={{
                      "cursor-pointer hover:border-accent": !!target,
                    }}
                    onClick={() => openRow(item)}
                  >
                    <input
                      type="checkbox"
                      aria-label={`Select ${item.title}`}
                      checked={selection.has(keyOf(item))}
                      onClick={(e) => e.stopPropagation()}
                      onChange={() => selection.toggle(keyOf(item))}
                    />
                    <div class="min-w-0 flex-1">
                      <div class="truncate text-sm text-fg" title={item.title}>
                        {item.title}
                      </div>
                      <div class="text-xs text-muted">
                        {MODE_LABELS[item.mode] ?? item.mode}
                        <Show when={item.missingCount > 0}>
                          {" · "}
                          {item.missingCount} missing
                        </Show>
                      </div>
                      <Show when={statusBlurb(item)}>
                        {(blurb) => <div class="text-xs text-warn">{blurb()}</div>}
                      </Show>
                    </div>
                    <span
                      class="rounded-full px-2 py-0.5 text-[11px]"
                      classList={{
                        "bg-warn/20 text-warn":
                          item.status === "Pending Retry" ||
                          item.status === "Pending",
                        "bg-surface-2 text-muted":
                          item.status !== "Pending Retry" &&
                          item.status !== "Pending",
                      }}
                    >
                      {item.status}
                    </span>
                    <Button
                      onClick={(e) => {
                        e.stopPropagation();
                        void removeOne(item);
                      }}
                    >
                      Remove
                    </Button>
                  </li>
                );
              }}
            </For>
          </ul>
        </Show>
      </Show>

      <Show when={grabTarget()}>
        {(t) => <GrabDialog target={t()} onClose={() => setGrabTarget(null)} />}
      </Show>
      <Show when={detailTarget()} keyed>
        {(t) => (
          <DetailPopup
            target={t}
            onClose={() => {
              setDetailTarget(null);
              void refetch();
            }}
            onSelectRecommendation={setDetailTarget}
            onGrab={setGrabTarget}
          />
        )}
      </Show>
    </div>
  );
};
