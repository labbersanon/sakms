// Requests — a cross-mode request-status WORKLIST (F4), not a fourth grab view.
//
// What it adds over the two existing, deliberately-narrow screens:
//   - /grabs     is a raw, per-mode grab log (one mode at a time, read-only).
//   - /downloads is the live download-client queue status.
// Neither rolls up state ACROSS modes, and neither surfaces what's still
// MISSING. /requests does both: one row per title spanning Movies/Series/Adult,
// each tagged In Library (tracked) / Downloading (an active grab) / Missing
// (Series episodes TMDB knows about but that aren't on disk yet), with a
// missing-episode count. It is pure derive-on-read aggregation (GET /api/requests)
// — no new persisted table, no grab affordance on already-in-library rows.
//
// Status honesty (single-operator model): there is no approval queue, so
// "Requested" collapses into Downloading (a grab IS the request), and Missing is
// Series-only in v1 (Movies/Adult don't track not-owned titles) — the backend
// documents both; this screen just renders whatever statuses it returns.

import {
  type Component,
  createMemo,
  createResource,
  createSignal,
  For,
  Show,
} from "solid-js";
import { type RequestStatusResponse, fetchRequests } from "../api/requests";
import type { DiscoverItem } from "../api/discover";
import { ErrorText, Muted } from "../components/ui";
import { type GrabTarget, GrabDialog } from "./discover/shared";
import { type DetailTarget, DetailPopup } from "./discover/DetailPopup";

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

export const Requests: Component = () => {
  const [data, { refetch }] = createResource(fetchRequests);
  const [statusFilter, setStatusFilter] = createSignal<string | null>(null);
  const [modeFilter, setModeFilter] = createSignal<string | null>(null);
  const [grabTarget, setGrabTarget] = createSignal<GrabTarget | null>(null);
  const [detailTarget, setDetailTarget] = createSignal<DetailTarget | null>(null);

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
        (modeFilter() === null || i.mode === modeFilter()),
    );

  const openRow = (item: RequestItem) => {
    const target = detailTargetFor(item);
    if (target) setDetailTarget(target);
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
      </div>

      <Show when={!data.loading} fallback={<Muted>Loading…</Muted>}>
        <Show
          when={filtered().length > 0}
          fallback={<Muted>No requests match this filter.</Muted>}
        >
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
                    </div>
                    <span class="rounded-full bg-surface-2 px-2 py-0.5 text-[11px] text-muted">
                      {item.status}
                    </span>
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
