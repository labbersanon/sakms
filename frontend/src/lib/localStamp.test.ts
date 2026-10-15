import { describe, expect, it } from "vitest";
import { formatLocalStamp } from "./localStamp";

describe("formatLocalStamp", () => {
  it("formats an ISO instant using local getFullYear/getHours parts", () => {
    const iso = "2026-10-12T08:13:00.000Z";
    const d = new Date(iso);
    const pad = (n: number) => String(n).padStart(2, "0");
    const want =
      `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ` +
      `${pad(d.getHours())}:${pad(d.getMinutes())}`;
    expect(formatLocalStamp(iso)).toBe(want);
  });

  it("returns empty for blank input and falls back for unparseable strings", () => {
    expect(formatLocalStamp("")).toBe("");
    expect(formatLocalStamp("not-a-date")).toBe("not-a-date");
  });
});
