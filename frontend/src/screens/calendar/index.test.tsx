// index.test.tsx — the Calendar screen's own suite (Wave 3 FE-CAL). It covers
// ONLY what index.tsx owns — the History/Upcoming switch, the grid/list toggle,
// the month navigation, and the two persisted keys. History.tsx and Upcoming.tsx
// have their own suites; nothing here re-tests their internals.
//
// DISCRIMINATORS, chosen so no assertion depends on a class name:
//   - History is mounted  ⟺  an "Adult" button exists. History renders a
//     three-mode ModeTabs (Movies/Series/Adult); Upcoming's selector is
//     Movies/Series only (plan §0.6), so "Adult" is unique to History.
//   - History is in LIST mode  ⟺  "No completed or imported grabs this month."
//     is on screen. That copy lives in the list branch's empty fallback only;
//     the grid branch renders weekday headers and no such text. With the stub
//     returning zero grabs, this is an exact grid/list discriminator.
//   - Upcoming is mounted  ⟺  it fetched /api/calendar/upcoming/movies.
//
// The month-navigation test asserts on Upcoming's FETCH URL rather than on the
// rendered month label, because the label is this component's own state while
// the URL is proof the value actually reached a child as a prop. It has to run
// on the Upcoming branch: History deliberately does NOT refetch on month change
// — it fetches /api/modes/{mode}/grabs unparameterized and filters client-side
// (History.tsx:133-139) — so there is no History request to assert against.
// Do not "fix" that by giving History month params.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { Calendar } from "./index";

const VIEW_KEY = "sakms.calendar.view";
const DISPLAY_KEY = "sakms.calendar.display";

const HISTORY_LIST_EMPTY = "No completed or imported grabs this month.";

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

// stubFetch answers every endpoint the two children hit at mount with an empty
// result, so each lands in its "nothing yet" state, and records the URLs so the
// month-navigation test can assert on real request traffic.
const stubFetch = (): string[] => {
  const urls: string[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    urls.push(url);
    if (url.includes("/api/settings/usenet-autograb-enabled")) {
      return jsonResponse({ enabled: false });
    }
    return jsonResponse([]);
  });
  vi.stubGlobal("fetch", fn);
  return urls;
};

const upcomingMovieCalls = (urls: string[]): string[] =>
  urls.filter((u) => u.startsWith("/api/calendar/upcoming/movies"));

beforeEach(() => {
  localStorage.clear();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  vi.useRealTimers();
  localStorage.clear();
});

describe("Calendar — view switch", () => {
  it("renders both switches with unambiguous labels and defaults to History", async () => {
    stubFetch();
    render(() => <Calendar />);

    // The four controls this component owns. Getting each by its exact
    // accessible name also proves no label collides with a child's own
    // selector (History's Movies/Series/Adult, Upcoming's Movies/Series).
    expect(screen.getByRole("button", { name: "History" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Upcoming" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Grid" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "List" })).toBeInTheDocument();

    // History is the default view: its three-mode ModeTabs is mounted.
    expect(await screen.findByRole("button", { name: "Adult" })).toBeInTheDocument();
  });

  it("switching to Upcoming mounts Upcoming and unmounts History", async () => {
    const urls = stubFetch();
    render(() => <Calendar />);
    await screen.findByRole("button", { name: "Adult" });

    fireEvent.click(screen.getByRole("button", { name: "Upcoming" }));

    await waitFor(() => expect(upcomingMovieCalls(urls).length).toBe(1));
    // History is gone, not merely hidden — <Show> unmounts it.
    expect(screen.queryByRole("button", { name: "Adult" })).toBeNull();
  });

  it("persists the selected view to localStorage", async () => {
    stubFetch();
    render(() => <Calendar />);
    fireEvent.click(screen.getByRole("button", { name: "Upcoming" }));
    await waitFor(() => expect(localStorage.getItem(VIEW_KEY)).toBe("upcoming"));
  });

  it("restores the persisted view across a remount", async () => {
    stubFetch();
    const first = render(() => <Calendar />);
    fireEvent.click(screen.getByRole("button", { name: "Upcoming" }));
    await waitFor(() => expect(localStorage.getItem(VIEW_KEY)).toBe("upcoming"));

    // Simulate a reload: tear the screen down completely, then mount a fresh
    // instance against the same storage.
    first.unmount();
    render(() => <Calendar />);

    // Upcoming, not the History default — so the persisted value was read at
    // mount rather than the component falling back.
    expect(screen.queryByRole("button", { name: "Adult" })).toBeNull();
    expect(screen.getByRole("button", { name: "Upcoming" })).toBeInTheDocument();
  });
});

describe("Calendar — grid/list display toggle", () => {
  it("switches History's layout and persists the choice", async () => {
    stubFetch();
    render(() => <Calendar />);
    await screen.findByRole("button", { name: "Adult" });

    // Grid is the default: the list branch's empty copy is absent.
    expect(screen.queryByText(HISTORY_LIST_EMPTY)).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "List" }));

    expect(await screen.findByText(HISTORY_LIST_EMPTY)).toBeInTheDocument();
    await waitFor(() => expect(localStorage.getItem(DISPLAY_KEY)).toBe("list"));

    // ...and back, so the toggle is proven bidirectional rather than one-way.
    fireEvent.click(screen.getByRole("button", { name: "Grid" }));
    await waitFor(() => expect(screen.queryByText(HISTORY_LIST_EMPTY)).toBeNull());
    expect(localStorage.getItem(DISPLAY_KEY)).toBe("grid");
  });

  it("restores the persisted display across a remount", async () => {
    localStorage.setItem(DISPLAY_KEY, "list");
    stubFetch();
    render(() => <Calendar />);
    expect(await screen.findByText(HISTORY_LIST_EMPTY)).toBeInTheDocument();
  });

  it("survives a round trip through the view switch, independently of it", async () => {
    const urls = stubFetch();
    render(() => <Calendar />);
    await screen.findByRole("button", { name: "Adult" });

    fireEvent.click(screen.getByRole("button", { name: "List" }));
    await screen.findByText(HISTORY_LIST_EMPTY);

    // Leave History and come back. The two keys are independent signals, so
    // changing the view must not reset the display (or vice versa).
    fireEvent.click(screen.getByRole("button", { name: "Upcoming" }));
    await waitFor(() => expect(upcomingMovieCalls(urls).length).toBe(1));
    expect(localStorage.getItem(DISPLAY_KEY)).toBe("list");

    fireEvent.click(screen.getByRole("button", { name: "History" }));
    expect(await screen.findByText(HISTORY_LIST_EMPTY)).toBeInTheDocument();
    expect(localStorage.getItem(VIEW_KEY)).toBe("history");
    expect(localStorage.getItem(DISPLAY_KEY)).toBe("list");
  });
});

describe("Calendar — corrupt persisted values", () => {
  it("falls back to History for display only, without rewriting storage", async () => {
    localStorage.setItem(VIEW_KEY, "not-a-real-view");
    stubFetch();
    render(() => <Calendar />);

    // Rendered as the default...
    expect(await screen.findByRole("button", { name: "Adult" })).toBeInTheDocument();
    // ...and storage is untouched until the operator actually picks a view,
    // mirroring Queue.tsx's sanitize-without-rewriting contract exactly.
    expect(localStorage.getItem(VIEW_KEY)).toBe("not-a-real-view");
  });

  it("falls back to grid for display only, without rewriting storage", async () => {
    localStorage.setItem(DISPLAY_KEY, "not-a-real-display");
    stubFetch();
    render(() => <Calendar />);

    await screen.findByRole("button", { name: "Adult" });
    // Grid, not list: the list branch's empty copy never appears.
    expect(screen.queryByText(HISTORY_LIST_EMPTY)).toBeNull();
    expect(localStorage.getItem(DISPLAY_KEY)).toBe("not-a-real-display");
  });

  it("does not crash when both keys are corrupt at once", async () => {
    localStorage.setItem(VIEW_KEY, "");
    localStorage.setItem(DISPLAY_KEY, "{}");
    stubFetch();
    render(() => <Calendar />);

    expect(await screen.findByRole("button", { name: "Adult" })).toBeInTheDocument();
    expect(screen.queryByText(HISTORY_LIST_EMPTY)).toBeNull();
    expect(localStorage.getItem(VIEW_KEY)).toBe("");
    expect(localStorage.getItem(DISPLAY_KEY)).toBe("{}");
  });
});

describe("Calendar — month navigation", () => {
  // Only Date is faked, so Vitest's timers stay real and waitFor still works.
  // A fixed December date makes the year-rollover assertion below exact rather
  // than dependent on when the suite happens to run.
  const freezeAt = (d: Date) => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(d);
  };

  it("shows the current month and steps forward and back", async () => {
    freezeAt(new Date(2026, 11, 15));
    stubFetch();
    render(() => <Calendar />);

    expect(screen.getByText("December 2026")).toBeInTheDocument();

    // Forward across the year boundary — step() normalizes overflow via Date,
    // so December + 1 must roll the YEAR, not produce a 13th month.
    fireEvent.click(screen.getByRole("button", { name: "Next month" }));
    expect(await screen.findByText("January 2027")).toBeInTheDocument();

    // ...and back across it again.
    fireEvent.click(screen.getByRole("button", { name: "Previous month" }));
    expect(await screen.findByText("December 2026")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Previous month" }));
    expect(await screen.findByText("November 2026")).toBeInTheDocument();
  });

  it("passes the navigated year/month down to the mounted child", async () => {
    freezeAt(new Date(2026, 11, 15));
    const urls = stubFetch();
    render(() => <Calendar />);

    fireEvent.click(screen.getByRole("button", { name: "Upcoming" }));

    // The first fetch covers the whole of the frozen current month.
    await waitFor(() => expect(upcomingMovieCalls(urls).length).toBe(1));
    expect(upcomingMovieCalls(urls)[0]).toContain("from=2026-12-01");
    expect(upcomingMovieCalls(urls)[0]).toContain("to=2026-12-31");

    fireEvent.click(screen.getByRole("button", { name: "Next month" }));

    // The child refetched with the NEW month — proof the prop propagated,
    // including the year rollover (January 2027, not 2026).
    await waitFor(() => expect(upcomingMovieCalls(urls).length).toBe(2));
    expect(upcomingMovieCalls(urls)[1]).toContain("from=2027-01-01");
    expect(upcomingMovieCalls(urls)[1]).toContain("to=2027-01-31");
  });
});
