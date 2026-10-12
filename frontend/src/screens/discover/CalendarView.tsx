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

const WEEKDAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
const MONTH_NAMES = [
  "January", "February", "March", "April", "May", "June",
  "July", "August", "September", "October", "November", "December",
];

const pad2 = (n: number) => String(n).padStart(2, "0");

// dayKey is the YYYY-MM-DD bucket key for one day in a given year/month
// (month is 0-based, matching JS Date). Also the shape a DiscoverItem's
// releaseDate is sliced to for bucketing.
const dayKey = (year: number, month: number, day: number) =>
  `${year}-${pad2(month + 1)}-${pad2(day)}`;

// monthRange returns the inclusive [from, to] YYYY-MM-DD span covering the whole
// given month (month 0-based). `new Date(year, month + 1, 0)` is the last day of
// the month (day 0 of the next month rolls back one).
function monthRange(year: number, month: number): { from: string; to: string } {
  const lastDay = new Date(year, month + 1, 0).getDate();
  return { from: dayKey(year, month, 1), to: dayKey(year, month, lastDay) };
}

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

  // cells is the month's week-padded grid (4-6 weeks / 28-42 cells depending
  // on the month's length and starting weekday, NOT a fixed 6 weeks): leading
  // nulls pad to the first day's weekday, then day numbers, then trailing
  // nulls to fill out the last week.
  const cells = (): (number | null)[] => {
    const firstWeekday = new Date(year(), month(), 1).getDay();
    const daysInMonth = new Date(year(), month() + 1, 0).getDate();
    const out: (number | null)[] = [];
    for (let i = 0; i < firstWeekday; i++) out.push(null);
    for (let d = 1; d <= daysInMonth; d++) out.push(d);
    while (out.length % 7 !== 0) out.push(null);
    return out;
  };

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

      {/* min-w must fit 7 columns of a 220px PosterCard + each cell's own
          chrome, so a day's card never overflows into the next day. The cells
          below are border-box (Tailwind preflight), so a column needs
          220 + 12 (p-1.5, 6px/side) + 2 (1px border/side) = 234px, plus the 6
          gap-2 gutters: 7×234 + 6×8 = 1686px, hence 106rem (1696px).
          grid-cols-7 is minmax(0,1fr), which would otherwise shrink columns
          below the card's fixed width, and overflow-x-auto sits OUTSIDE this
          min-w so it cannot compensate — it only scrolls the whole grid on
          narrow viewports.
          Review if: PosterCard's w-[220px] or the cell's p-1.5/border changes. */}
      <div class="overflow-x-auto">
        <div class="min-w-[106rem]">
          <div class="grid grid-cols-7 gap-2">
            <For each={WEEKDAYS}>
              {(w) => (
                <div class="px-1 text-xs font-semibold uppercase tracking-wide text-muted">
                  {w}
                </div>
              )}
            </For>
          </div>
          <div class="mt-1 grid grid-cols-7 gap-2">
            <For each={cells()}>
              {(day) => (
                <div
                  class="min-h-[7rem] rounded-lg border border-border p-1.5"
                  classList={{ "bg-surface/40": day !== null, "bg-transparent": day === null }}
                >
                  <Show when={day !== null}>
                    <div class="mb-1 text-xs font-medium text-muted">{day}</div>
                    <div class="flex flex-col gap-2">
                      <For each={buckets().get(dayKey(year(), month(), day!)) ?? []}>
                        {(item) => (
                          <PosterCard
                            mode={props.mode}
                            item={item}
                            onGrab={props.onGrab}
                            onDetail={props.onDetail}
                          />
                        )}
                      </For>
                    </div>
                  </Show>
                </div>
              )}
            </For>
          </div>
        </div>
      </div>
    </div>
  );
};
