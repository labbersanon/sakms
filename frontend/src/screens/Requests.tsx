// Requests — a cross-mode request-status WORKLIST (F4), not a fourth grab view.
//
// What it adds over the two existing, deliberately-narrow sibling tabs (all
// three live under the Queue sidebar entry):
//   - the Grabs tab is a raw, per-mode grab log (one mode at a time, read-only).
//   - the Downloads tab is the live download-client queue status.
// Neither rolls up state ACROSS modes, and neither surfaces what's still
// MISSING. The Requests tab does both: one row per title spanning Movies/Series/Adult,
// each tagged In Library (tracked) / Downloading (an active grab) / Pending Retry
// (an auto-grab awaiting re-search). Missing is NOT one of those tags — it is a
// Series-only missing-episode count that co-occurs with any of them (episodes TMDB
// knows about that aren't on disk yet). It is pure derive-on-read aggregation
// (GET /api/requests) — no new persisted table, no grab affordance on
// already-in-library rows.
//
// Status honesty (single-operator model): there is no approval queue, so
// "Requested" collapses into Downloading (a grab IS the request), and the
// missing-episode count is Series-only (Movies/Adult don't track not-owned
// titles) — the backend documents both. The status chips render whatever statuses
// the backend returns; the "Has Missing Episodes" chip is the one hand-written,
// non-data-derived filter on this screen, and it intersects with the status/mode
// chips rather than acting as a fourth status.

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
import { Button, ErrorText, Muted } from "../components/ui";
import { type GrabTarget, GrabDialog } from "./discover/shared";
import { type DetailTarget, DetailPopup } from "./discover/DetailPopup";
import { useBulkSelection } from "./workflowHooks";

// RequestItem is one row of the response's Items array — the generated DTO's
// element type (RequestStatusResponse.items[number]) rather than a re-declared
// shape, so field names stay generated, never hand-duplicated.
type RequestItem = RequestStatusResponse["items"][number];

const MODE_LABELS: Record<string, string> = {
  movies: "Movies",
  series: "Series",
  adult: "Adult",
};

// FilterChips is a small "All + one per distinct value" chip row. Values are
// derived from the data so the chips always match whatever the backend emits
// (status strings and mode set), rather than hardcoding labels that could drift
// from the server's own values.
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

// detailTargetFor synthesizes a DiscoverItem (id = tmdbId) so a Movies/Series
// row can open the existing DetailPopup — the same synthetic-item pattern the
// Mainstream library row already uses. Adult rows have no TMDB id, so they don't
// open the popup (returns null; the row stays non-clickable).
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

// keyOf is the selection key for a row — a synthetic mode:tmdbId:title string,
// because a row's identity is that tuple (Adult rows all carry tmdbId 0, so the
// bare id can't distinguish them). The generic useBulkSelection<string> holds
// these keys; excludeReqFor maps a row back to its exclude body.
function keyOf(item: RequestItem): string {
  return `${item.mode}:${item.tmdbId}:${item.title}`;
}

// excludeReqFor builds one ExcludeTitleRequest from a row: TMDBID only when the
// row actually has one (>0), Title always present — covers Movies/Series (by
// tmdbId) and Adult (by title) in one shape, matching the DTO's "at least one of
// TMDBID/Title" contract.
function excludeReqFor(item: RequestItem): ExcludeTitleRequest {
  return {
    mode: item.mode,
    tmdbId: item.tmdbId > 0 ? item.tmdbId : undefined,
    title: item.title,
  };
}

// retryDuration renders a millisecond delta as a short human span ("6h",
// "2d") for the retry countdown line — never a raw ISO timestamp.
function retryDuration(ms: number): string {
  const hours = Math.round(ms / (60 * 60 * 1000));
  if (hours < 1) return "under an hour";
  if (hours < 24) return `${hours}h`;
  return `${Math.round(hours / 24)}d`;
}

// retryBlurb turns a Pending Retry row's retryAfter/retryReason into one
// human-readable line, e.g. "Retrying in 6h — no candidate cleared the
// quality floor". The backend's RetryAfter (a UTC sqliteTimeLayout string)
// and RetryReason are independent fields — a freshly parked row can have a
// reason with no scheduled time yet, or vice versa — so each is optional
// here too. Returns null when there's nothing to show.
function retryBlurb(item: RequestItem): string | null {
  if (item.status !== "Pending Retry") return null;
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

export const Requests: Component = () => {
  const [data, { refetch }] = createResource(fetchRequests);
  const [statusFilter, setStatusFilter] = createSignal<string | null>(null);
  const [modeFilter, setModeFilter] = createSignal<string | null>(null);
  const [missingOnly, setMissingOnly] = createSignal(false);
  const [grabTarget, setGrabTarget] = createSignal<GrabTarget | null>(null);
  const [detailTarget, setDetailTarget] = createSignal<DetailTarget | null>(null);
  const [actionError, setActionError] = createSignal<string | null>(null);
  const selection = useBulkSelection<string>();

  const items = () => data()?.items ?? [];

  const statuses = createMemo(() =>
    [...new Set(items().map((i) => i.status))].filter(Boolean).sort(),
  );
  const modes = createMemo(() =>
    [...new Set(items().map((i) => i.mode))].filter(Boolean).sort(),
  );

  const filtered = () =>
    items().filter(
      (i) =>
        (statusFilter() === null || i.status === statusFilter()) &&
        (modeFilter() === null || i.mode === modeFilter()) &&
        (!missingOnly() || i.missingCount > 0),
    );

  const openRow = (item: RequestItem) => {
    const target = detailTargetFor(item);
    if (target) setDetailTarget(target);
  };

  // Select-all acts on the currently-filtered rows (only what's on screen),
  // mirroring Rename's pendingIds select-all.
  const filteredKeys = (): string[] => filtered().map(keyOf);
  const allSelected = (): boolean => {
    const keys = filteredKeys();
    return keys.length > 0 && keys.every((k) => selection.has(k));
  };
  const toggleSelectAll = (): void => {
    if (allSelected()) selection.clear();
    else selection.selectAll(filteredKeys());
  };

  // removeOne excludes a single row after the destructive-action confirm, then
  // refetches so the now-suppressed title leaves the list (GET /api/requests
  // filters excluded titles server-side).
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

  // removeSelected excludes every checked row in one exclude-batch call, behind
  // the same confirm, then clears the selection and refetches.
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
        <FilterChips
          values={statuses()}
          selected={statusFilter()}
          onSelect={setStatusFilter}
        />
        <Show when={modes().length > 1}>
          <FilterChips
            values={modes()}
            selected={modeFilter()}
            onSelect={setModeFilter}
            labelOf={(m) => MODE_LABELS[m] ?? m}
          />
        </Show>
        {/* A standalone boolean toggle, deliberately NOT a FilterChips instance:
            FilterChips always renders its own "All" button, and its semantics are
            "All + one-of-N", not on/off. Rendered unconditionally (never gated on
            the data) so it can't flicker during a refetch or strand an active
            filter with no affordance to clear it. The predicate is missingCount > 0
            alone — Series-only-ness is a backend guarantee (Movies/Adult are
            constructed without a MissingCount), and a mode clause here would mask
            a violation of it rather than surface one. */}
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
                      <Show when={retryBlurb(item)}>
                        {(blurb) => <div class="text-xs text-warn">{blurb()}</div>}
                      </Show>
                    </div>
                    <span
                      class="rounded-full px-2 py-0.5 text-[11px]"
                      classList={{
                        "bg-warn/20 text-warn": item.status === "Pending Retry",
                        "bg-surface-2 text-muted": item.status !== "Pending Retry",
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
      {/* keyed for the same reason as Mainstream's: a "More like this" click
          swaps detailTarget from one truthy target to another, so the popup must
          remount to reset its component-local selector/grab signals. */}
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
