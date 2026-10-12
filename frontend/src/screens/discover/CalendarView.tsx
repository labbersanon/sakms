// CalendarView — Mainstream Discover's month-grid sub-view (F2). A monthly grid
// with prev/next-month navigation; each visible month's date range is fetched
// from GET /api/modes/{mode}/discover/calendar for the active Movies/Series tab
// (not both). DiscoverItem[] is bucketed by releaseDate into day cells. Each
// item renders as the SAME PosterCard the carousels/grid use — a click opens
// DetailPopup and Grab works identically, and F3 select-mode checkboxes land
// on it for free. Edit mode is disabled while Calendar is active (no rows to
// reorder); Mainstream.tsx owns that gating.
//
// Claude 2026-10-11: fetch only the active tab's mode.
// Reason: Mainstream split Movies/Series into tabs; the calendar still merged
//   both TMDB windows, so the Series tab's month grid was movie releases.
// Troubleshooting: Discover → Series → Calendar shows films, not TV premieres.
// Review if: a combined Movies+Series calendar tab is added back on purpose.
//
// Claude 2026-10-12: month chrome lives in calendar/MonthGrid.tsx.
// Reason: Queue History/Upcoming must match this grid (220px posters, 106rem).
// Troubleshooting: Queue calendars were text cells / a narrower scroller.
// Review if: Discover calendar drops PosterCard or the shared MonthGrid.
//
// v1 scope is Movies releases or TV first-air premieres; a per-episode air-date
// calendar is a documented follow-up (heavier, per-episode TMDB queries).

import {
  type Component,
  createMemo,
  createResource,
  createSignal,
  For,
  Show,
} from "solid-js";
import { type DiscoverItem, fetchDiscoverCalendar } from "../../api/discover";
import { Button, Muted } from "../../components/ui";
import type { GrabTarget } from "./shared";
import type { DetailTarget } from "./DetailPopup";
import { PosterCard } from "./Mainstream";
import { MonthGrid } from "../calendar/MonthGrid";
import { monthRange } from "../calendar/grid";

const MONTH_NAMES = [
  "January", "February", "March", "April", "May", "June",
  "July", "August", "September", "October", "November", "December",
];

export const CalendarView: Component<{
  mode: "movies" | "series";
  onGrab: (t: GrabTarget) => void;
  onDetail: (t: DetailTarget) => void;
}> = (props) => {
  const now = new Date();
  const [year, setYear] = createSignal(now.getFullYear());
  const [month, setMonth] = createSignal(now.getMonth());

  const step = (delta: number) => {
    // Normalize month over/underflow into the adjacent year via a single Date.
    const d = new Date(year(), month() + delta, 1);
    setYear(d.getFullYear());
    setMonth(d.getMonth());
  };

  // Fetch (and refetch on month or Movies/Series tab change) this tab's month.
  // A fetch failure degrades to [] so a TMDB blip never blanks the grid chrome.
  const [items] = createResource(
    () => ({ mode: props.mode, ...monthRange(year(), month()) }),
    ({ mode, from, to }) =>
      fetchDiscoverCalendar(mode, from, to).catch(() => [] as DiscoverItem[]),
  );

  // buckets keys the fetched items by their YYYY-MM-DD release day. Memoized
  // (not a plain derived signal) since <For> reads it once per cell per
  // render — a plain function would rebuild the whole Map ~30-40 times per
  // render pass instead of once per items() change.
  const buckets = createMemo(() => {
    const map = new Map<string, DiscoverItem[]>();
    for (const it of items() ?? []) {
      const key = (it.releaseDate ?? "").slice(0, 10);
      if (!key) continue;
      const list = map.get(key);
      if (list) list.push(it);
      else map.set(key, [it]);
    }
    return map;
  });

  return (
    <div>
      <div class="mb-3 flex items-center gap-3">
        <Button class="!px-3 !py-1" onClick={() => step(-1)} aria-label="Previous month">
          ‹
        </Button>
        <div class="min-w-[10rem] text-center text-sm font-semibold text-fg">
          {MONTH_NAMES[month()]} {year()}
        </div>
        <Button class="!px-3 !py-1" onClick={() => step(1)} aria-label="Next month">
          ›
        </Button>
        <Show when={items.loading}>
          <Muted class="!text-xs">Loading…</Muted>
        </Show>
      </div>

      <MonthGrid year={year()} month={month()}>
        {(_day, key) => (
          <For each={buckets().get(key) ?? []}>
            {(item) => (
              <PosterCard
                mode={props.mode}
                item={item}
                onGrab={props.onGrab}
                onDetail={props.onDetail}
              />
            )}
          </For>
        )}
      </MonthGrid>
    </div>
  );
};
