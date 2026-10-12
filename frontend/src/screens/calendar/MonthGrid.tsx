// MonthGrid — the Discover calendar's month chrome, shared by Queue History
// and Upcoming so every calendar scrolls and sizes the same way.
//
// Claude 2026-10-12: extract Discover CalendarView's 7-column poster grid.
// Reason: Queue calendars used a squeezed 7-col grid (History) or a 70rem
//   cell-width grid (Upcoming); Discover uses 220px PosterCards in a 106rem
//   scroller. The operator asked every calendar to match Discover.
// Troubleshooting: Queue → Calendar History/Upcoming looked like a text
//   agenda, not Discover → Calendar.
// Review if: PosterCard's w-[220px] / MEDIA_CAROUSEL_POSTER_CLASS or the
//   cell's p-1.5/border changes — then update min-w-[106rem] here too.
// Related files: discover/CalendarView.tsx, History.tsx, Upcoming.tsx
// Context: see CalendarPosterCard.tsx

import { type Component, type JSX, For, Show } from "solid-js";
import { cells, dayKey } from "./grid";

const WEEKDAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

export const MonthGrid: Component<{
  year: number;
  month: number;
  children: (day: number, key: string) => JSX.Element;
}> = (props) => (
  <div class="overflow-x-auto">
    {/* min-w must fit 7 columns of a 220px PosterCard + each cell's own
        chrome, so a day's card never overflows into the next day. The cells
        below are border-box (Tailwind preflight), so a column needs
        220 + 12 (p-1.5, 6px/side) + 2 (1px border/side) = 234px, plus the 6
        gap-2 gutters: 7×234 + 6×8 = 1686px, hence 106rem (1696px).
        grid-cols-7 is minmax(0,1fr), which would otherwise shrink columns
        below the card's fixed width, and overflow-x-auto sits OUTSIDE this
        min-w so it cannot compensate — it only scrolls the whole grid on
        narrow viewports. */}
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
        <For each={cells(props.year, props.month)}>
          {(day) => (
            <div
              class="min-h-[7rem] rounded-lg border border-border p-1.5"
              classList={{
                "bg-surface/40": day !== null,
                "bg-transparent": day === null,
              }}
            >
              <Show when={day !== null}>
                <div class="mb-1 text-xs font-medium text-muted">{day}</div>
                <div class="flex flex-col gap-2">
                  {props.children(day!, dayKey(props.year, props.month, day!))}
                </div>
              </Show>
            </div>
          )}
        </For>
      </div>
    </div>
  </div>
);
