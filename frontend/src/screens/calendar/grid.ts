// grid.ts — month-grid geometry helpers for the History/Upcoming calendar
// screen: pad2, dayKey, monthRange, cells.
//
// This is a DELIBERATE, DOCUMENTED DUPLICATE of the near-identical private
// helpers in ../discover/CalendarView.tsx (pad2/dayKey/monthRange/cells
// there, same names, same shapes). It is NOT an oversight and a future
// deslop/simplify pass must not "fix" it by hoisting a shared module.
//
// Per §0.3 of .omc/plans/autopilot-impl-calendar-grabs-requests.md: the two
// callers are genuinely independent screens serving different purposes —
// CalendarView.tsx buckets Discover's TMDB release-date browsing grid
// (Movies+Series merged, poster-first DiscoverItem rows), this screen
// buckets History's grabs.Grab rows (by createdAt, no poster) and Upcoming's
// Series-episode/Movies-release rows — three different data sources feeding
// two independent screens with no common carrier type. This repo's
// no-premature-abstraction convention (see CLAUDE.md's "Established
// engineering conventions" and Queue.tsx's documented relationship to
// Organize.tsx: "deliberately a near-line-for-line mirror ... not a shared
// abstraction extracted from the two") treats two callers with different
// purposes as insufficient justification for a shared helper, especially
// one that would couple this Queue-embedded Calendar screen to an unrelated
// Discover screen. Keep this file's implementation in sync in SPIRIT with
// CalendarView.tsx's conventions (month is 0-based, cells pads with `null`
// rather than adjacent-month day numbers) but do not import from it.

export const pad2 = (n: number): string => String(n).padStart(2, "0");

// dayKey is the YYYY-MM-DD bucket key for one day in a given year/month
// (month is 0-based, matching JS Date).
export const dayKey = (year: number, month: number, day: number): string =>
  `${year}-${pad2(month + 1)}-${pad2(day)}`;

// monthRange returns the inclusive [from, to] YYYY-MM-DD span covering the
// whole given month (month 0-based). `new Date(year, month + 1, 0)` is the
// last day of the month (day 0 of the next month rolls back one) — this
// also naturally handles leap years and every month-length variation since
// it asks the JS Date engine rather than hardcoding day counts.
export function monthRange(
  year: number,
  month: number,
): { from: string; to: string } {
  const lastDay = new Date(year, month + 1, 0).getDate();
  return { from: dayKey(year, month, 1), to: dayKey(year, month, lastDay) };
}

// cells is the month's week-padded grid (4-6 weeks / 28-42 cells depending
// on the month's length and starting weekday, NOT a fixed 6 weeks): leading
// nulls pad to the first day's weekday, then day numbers 1..daysInMonth,
// then trailing nulls to fill out the last week to a multiple of 7.
export function cells(year: number, month: number): (number | null)[] {
  const firstWeekday = new Date(year, month, 1).getDay();
  const daysInMonth = new Date(year, month + 1, 0).getDate();
  const out: (number | null)[] = [];
  for (let i = 0; i < firstWeekday; i++) out.push(null);
  for (let d = 1; d <= daysInMonth; d++) out.push(d);
  while (out.length % 7 !== 0) out.push(null);
  return out;
}
