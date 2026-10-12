// grid.ts — month-grid geometry helpers: pad2, dayKey, monthRange, cells.
//
// Claude 2026-10-12: Discover CalendarView now imports these helpers (and
//   MonthGrid.tsx) instead of keeping a private copy.
// Reason: every calendar was asked to match Discover's month grid; two
//   copies of cells/dayKey would drift the poster scroller.
// Troubleshooting: Queue History/Upcoming used a different cell geometry
//   and chrome than Discover → Calendar.
// Review if: a calendar is added that must NOT share Discover's 7-day pad.
//
// Data sources stay independent (DiscoverItem vs Grab vs upcoming entries).
// Only geometry and chrome are shared — see MonthGrid.tsx.

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
