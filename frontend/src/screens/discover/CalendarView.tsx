// CalendarView — Mainstream Discover's month-grid sub-view (F2). A monthly grid
// with prev/next-month navigation; each visible month's date range is fetched
// from GET /api/modes/{mode}/discover/calendar (Movies release dates + TV
// premieres, both modes merged since Mainstream combines them), and the returned
// DiscoverItem[] is bucketed by releaseDate into day cells. Each item renders as
// the SAME PosterCard the carousels/grid use (not a bespoke one-off card) — so a
// click opens the existing DetailPopup and the Grab affordance works identically,
// and so the later F3 select-mode checkbox lands on it for free. Edit mode is
// disabled while Calendar is active (there are no rows to reorder); the parent
// (Mainstream.tsx) owns that gating.
//
// v1 scope is Movies releases + TV first-air premieres; a per-episode air-date
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

// cardMode derives a merged calendar item's grab mode from its TMDB mediaType,
// the same per-item-mode pattern the combined library/search grids already use.
const cardMode = (item: DiscoverItem): "movies" | "series" =>
  item.mediaType === "tv" ? "series" : "movies";

export const CalendarView: Component<{
  onGrab: (t: GrabTarget) => void;
  onDetail: (t: DetailTarget) => void;
}> = (props) => {
  const now = new Date();
  const [year, setYear] = createSignal(now.getFullYear());
  const [month, setMonth] = createSignal(now.getMonth());

  const monthKey = () => `${year()}-${pad2(month() + 1)}`;

  const step = (delta: number) => {
    // Normalize month over/underflow into the adjacent year via a single Date.
    const d = new Date(year(), month() + delta, 1);
    setYear(d.getFullYear());
    setMonth(d.getMonth());
  };

  // Fetch (and refetch on month change) the whole visible month's range for
  // BOTH modes, merged — Mainstream is a combined Movies+Series page. A
  // per-mode failure degrades to [] so one missing source never blanks the grid.
  const [items] = createResource(monthKey, async () => {
    const { from, to } = monthRange(year(), month());
    const [movies, series] = await Promise.all([
      fetchDiscoverCalendar("movies", from, to).catch(() => [] as DiscoverItem[]),
      fetchDiscoverCalendar("series", from, to).catch(() => [] as DiscoverItem[]),
    ]);
    return [...movies, ...series];
  });

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

      {/* min-w must fit 7 columns of a 180px PosterCard + its cell padding
          (~192px each) so a day's card never overflows into the next day —
          grid-cols-7 is minmax(0,1fr), which would otherwise shrink columns
          below the card's fixed width. overflow-x-auto scrolls it on narrow
          viewports. */}
      <div class="overflow-x-auto">
        <div class="min-w-[92rem]">
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
                            mode={cardMode(item)}
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
