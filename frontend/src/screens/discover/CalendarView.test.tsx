// CalendarView tests: the month grid buckets fetched items onto their release
// day, and prev/next-month navigation refetches a different date range. Mirrors
// this repo's Discover test conventions (stubGlobal("fetch") + jsonResponse).

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import type { DiscoverItem } from "@dto";
import { CalendarView } from "./CalendarView";

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

const pad2 = (n: number) => String(n).padStart(2, "0");

const movie = (over: Partial<DiscoverItem> = {}): DiscoverItem => ({
  id: 1,
  title: "Cal Movie",
  posterPath: "",
  overview: "",
  releaseDate: "2024-01-01",
  voteAverage: 0,
  mediaType: "movie",
  ...over,
});

type Call = { url: string };
const stubFetch = (handler: (url: string) => Response) => {
  const calls: Call[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    calls.push({ url });
    return handler(url);
  });
  vi.stubGlobal("fetch", fn);
  return calls;
};

const calendarCalls = (calls: Call[]) =>
  calls.filter((c) => c.url.includes("/discover/calendar"));

const fromParam = (url: string) =>
  new URL(url, "http://x").searchParams.get("from");

afterEach(() => vi.unstubAllGlobals());

describe("CalendarView", () => {
  it("buckets a fetched item onto its release day in the current month", async () => {
    const now = new Date();
    const day = `${now.getFullYear()}-${pad2(now.getMonth() + 1)}-15`;
    const item = movie({ title: "Bucketed Movie", releaseDate: day });

    stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover/calendar"))
        return jsonResponse([item]);
      if (url.includes("/api/modes/series/discover/calendar"))
        return jsonResponse([]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <CalendarView onGrab={() => {}} onDetail={() => {}} />);

    // The item renders (bucketed into the day-15 cell). Empty posterPath means
    // the title shows in both the TextPoster fallback and the card title.
    expect((await screen.findAllByText("Bucketed Movie")).length).toBeGreaterThan(0);
    // Day-15 cell exists (the number label).
    expect(screen.getByText("15")).toBeInTheDocument();
  });

  it("refetches a different date range on next-month navigation", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/discover/calendar")) return jsonResponse([]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <CalendarView onGrab={() => {}} onDetail={() => {}} />);

    // Initial month fetched (both modes → at least one calendar call).
    await vi.waitFor(() => expect(calendarCalls(calls).length).toBeGreaterThan(0));
    const firstFrom = fromParam(calendarCalls(calls)[0]!.url);
    const beforeCount = calendarCalls(calls).length;

    fireEvent.click(screen.getByLabelText("Next month"));

    // A new range is fetched, with a different `from` than the first month.
    await vi.waitFor(() =>
      expect(calendarCalls(calls).length).toBeGreaterThan(beforeCount),
    );
    const laterFrom = fromParam(
      calendarCalls(calls)[calendarCalls(calls).length - 1]!.url,
    );
    expect(laterFrom).not.toBe(firstFrom);
  });
});
