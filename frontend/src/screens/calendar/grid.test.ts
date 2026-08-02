// grid.test.ts — locks down grid.ts's month-geometry contract (pad2, dayKey,
// monthRange, cells). See grid.ts's header comment for why this file exists
// as a deliberate duplicate of ../discover/CalendarView.tsx's own private
// helpers rather than importing a shared module.

import { describe, expect, it } from "vitest";
import { cells, dayKey, monthRange, pad2 } from "./grid";

describe("pad2", () => {
  it("zero-pads a single-digit number to 2 digits", () => {
    expect(pad2(0)).toBe("00");
    expect(pad2(1)).toBe("01");
    expect(pad2(9)).toBe("09");
  });

  it("leaves a double-digit number unchanged", () => {
    expect(pad2(10)).toBe("10");
    expect(pad2(31)).toBe("31");
    expect(pad2(99)).toBe("99");
  });
});

describe("dayKey", () => {
  it("formats a YYYY-MM-DD key from a 0-based month", () => {
    expect(dayKey(2025, 0, 1)).toBe("2025-01-01");
    expect(dayKey(2025, 11, 25)).toBe("2025-12-25");
  });

  it("zero-pads single-digit month and day", () => {
    expect(dayKey(2025, 2, 5)).toBe("2025-03-05");
  });

  it("does not carry across a month/year boundary — it is a pure formatter", () => {
    // dayKey has no knowledge of "next month"; Dec 31 and the following
    // Jan 1 are two independent calls with two independent (year, month)
    // pairs, not an increment. This is the boundary case the month/day
    // rollover logic in monthRange/cells relies on being absent here.
    expect(dayKey(2025, 11, 31)).toBe("2025-12-31");
    expect(dayKey(2026, 0, 1)).toBe("2026-01-01");
  });
});

describe("monthRange", () => {
  it("spans the full month for a normal 31-day month", () => {
    expect(monthRange(2025, 0)).toEqual({ from: "2025-01-01", to: "2025-01-31" });
  });

  it("spans the full month for a 30-day month", () => {
    expect(monthRange(2025, 3)).toEqual({ from: "2025-04-01", to: "2025-04-30" });
  });

  it("resolves February to 29 days in a leap year", () => {
    expect(monthRange(2024, 1)).toEqual({ from: "2024-02-01", to: "2024-02-29" });
  });

  it("resolves February to 28 days in a non-leap year", () => {
    expect(monthRange(2025, 1)).toEqual({ from: "2025-02-01", to: "2025-02-28" });
  });

  it("resolves December (month rollover via new Date(year, month + 1, 0))", () => {
    expect(monthRange(2025, 11)).toEqual({ from: "2025-12-01", to: "2025-12-31" });
  });
});

describe("cells", () => {
  it("pads a month starting on Sunday with zero leading nulls (Jan 2023)", () => {
    const out = cells(2023, 0);
    expect(out.length).toBe(35); // 31 days, no leading padding, rounds to 35
    expect(out.slice(0, 5)).toEqual([1, 2, 3, 4, 5]);
    expect(out[30]).toBe(31);
    expect(out.slice(31)).toEqual([null, null, null, null]);
  });

  it("pads a month starting on Saturday with 6 leading nulls (Jul 2023)", () => {
    const out = cells(2023, 6);
    expect(out.length).toBe(42); // 6 leading + 31 days = 37, rounds to 42
    expect(out.slice(0, 6)).toEqual([null, null, null, null, null, null]);
    expect(out[6]).toBe(1);
    expect(out[36]).toBe(31);
    expect(out.slice(37)).toEqual([null, null, null, null, null]);
  });

  it("pads a leap-year February starting on Thursday (Feb 2024)", () => {
    const out = cells(2024, 1);
    expect(out.length).toBe(35); // 4 leading + 29 days = 33, rounds to 35
    expect(out.slice(0, 4)).toEqual([null, null, null, null]);
    expect(out[4]).toBe(1);
    expect(out[32]).toBe(29);
    expect(out.slice(33)).toEqual([null, null]);
  });

  it("pads a non-leap-year February starting on Saturday (Feb 2025)", () => {
    const out = cells(2025, 1);
    expect(out.length).toBe(35); // 6 leading + 28 days = 34, rounds to 35
    expect(out.slice(0, 6)).toEqual([null, null, null, null, null, null]);
    expect(out[6]).toBe(1);
    expect(out[33]).toBe(28);
    expect(out.slice(34)).toEqual([null]);
  });

  it("produces a correctly ordered grid for a 30-day month starting midweek (Apr 2025)", () => {
    const out = cells(2025, 3);
    expect(out.length).toBe(35); // 2 leading + 30 days = 32, rounds to 35
    expect(out.slice(0, 2)).toEqual([null, null]);
    // days appear in ascending, unbroken order right after the leading pad
    expect(out.slice(2, 32)).toEqual(
      Array.from({ length: 30 }, (_, i) => i + 1),
    );
    expect(out.slice(32)).toEqual([null, null, null]);
  });

  it("always returns a length that is a multiple of 7", () => {
    for (let m = 0; m < 12; m++) {
      expect(cells(2025, m).length % 7).toBe(0);
    }
  });
});
