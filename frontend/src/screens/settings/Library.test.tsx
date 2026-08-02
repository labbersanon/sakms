// Settings → Library tests, scoped to the Series-only new-season discovery
// toggle. The other panels in this file (root folder, quality prefs, naming,
// kids path) are already covered from the composed screen in Settings.test.tsx.
//
// The section renders STANDALONE here, outside a <SectionSave> provider, which
// is exactly what useSectionSaveItem's no-context branch is for: it returns
// false, so the card keeps its own Save button and this test can drive the PUT
// directly instead of reaching through the tab's batched button.
//
// The copy string is hard-coded rather than imported from Library.tsx: importing
// the const would prove the file is self-consistent, not that the required
// sentence ships. The dash in it is an em dash (U+2014).

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@solidjs/testing-library";
import { SeriesNewSeasonDiscoverySection } from "./Library";

const DISCOVERY_COPY =
  "With new-season discovery on, a brand-new season is monitored automatically — even if every existing season of that series is unmonitored.";

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

const noContent = (): Response => new Response(null, { status: 204 });

type Call = { url: string; method: string; body: unknown };

// stubFetch answers the card's one mount GET with `enabled` and records every
// call. get() replaces the GET response wholesale for the load-failure case.
const stubFetch = (enabled: boolean, get?: () => Response) => {
  const calls: Call[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = (init?.method ?? "GET").toUpperCase();
    calls.push({
      url,
      method,
      body: init?.body ? JSON.parse(init.body as string) : undefined,
    });
    if (!url.includes("/api/settings/series-new-season-discovery"))
      throw new Error("unexpected fetch: " + url);
    if (method === "GET") return get ? get() : jsonResponse({ enabled });
    return noContent();
  });
  vi.stubGlobal("fetch", fn);
  return calls;
};

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const toggle = () => screen.getByLabelText("Enable new-season discovery") as HTMLInputElement;

describe("Settings → Library — new-season discovery toggle", () => {
  it("seeds from the stored value and round-trips a flip to the PUT", async () => {
    const calls = stubFetch(false);
    render(() => <SeriesNewSeasonDiscoverySection />);

    // Off is the default and the stored value here, so the toggle must not
    // render checked before the GET resolves either.
    await waitFor(() => expect(toggle()).not.toBeChecked());
    expect(
      calls.some(
        (c) =>
          c.method === "GET" &&
          c.url === "/api/settings/series-new-season-discovery",
      ),
    ).toBe(true);

    fireEvent.click(toggle());
    expect(toggle()).toBeChecked();
    fireEvent.click(screen.getByText("Save"));

    await waitFor(() => expect(calls.some((c) => c.method === "PUT")).toBe(true));
    const put = calls.find((c) => c.method === "PUT")!;
    expect(put.url).toBe("/api/settings/series-new-season-discovery");
    expect(put.body).toEqual({ enabled: true });
    await waitFor(() => expect(screen.getByText("saved")).toBeInTheDocument());
  });

  it("seeds ON when the stored value is on, and can be turned back off", async () => {
    const calls = stubFetch(true);
    render(() => <SeriesNewSeasonDiscoverySection />);

    await waitFor(() => expect(toggle()).toBeChecked());

    fireEvent.click(toggle());
    fireEvent.click(screen.getByText("Save"));

    await waitFor(() => expect(calls.some((c) => c.method === "PUT")).toBe(true));
    expect(calls.find((c) => c.method === "PUT")!.body).toEqual({
      enabled: false,
    });
  });

  it("carries the required new-season discovery copy", async () => {
    stubFetch(false);
    render(() => <SeriesNewSeasonDiscoverySection />);

    expect(await screen.findByText(DISCOVERY_COPY)).toBeInTheDocument();
  });

  it("shows the honest default (off, disabled) when the GET fails", async () => {
    stubFetch(false, () => new Response("boom", { status: 500 }));
    render(() => <SeriesNewSeasonDiscoverySection />);

    await waitFor(() => expect(toggle()).toBeDisabled());
    expect(toggle()).not.toBeChecked();
    expect(screen.getByText(/Discovery is off/)).toBeInTheDocument();
  });
});
