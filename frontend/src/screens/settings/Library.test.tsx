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
import { QualityPrefsSection, SeriesNewSeasonDiscoverySection } from "./Library";
import { jsonResponse, noContent } from "../../testing/http";

const DISCOVERY_COPY =
  "With new-season discovery on, a brand-new season is monitored automatically — even if every existing season of that series is unmonitored.";


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

// ---- Rename Undo's per-mode depth, on the quality-prefs card ---------------
//
// QualityPrefsSection renders STANDALONE here for the same reason the section
// above does: outside a <SectionSave> provider, useSectionSaveItem's no-context
// branch keeps the card's own Save button, so the PUT can be driven directly
// instead of through the tab's batched button.
//
// undoDepth rides the EXISTING quality-prefs request rather than an endpoint of
// its own, so the assertion that matters is that the PUT still carries the
// other three fields alongside it — a save that dropped tier/maxResolution/
// protocol would look like a working undo-depth field and quietly reset the
// operator's search preferences.

const stubQualityFetch = (undoDepth: number) => {
  const calls: Call[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = (init?.method ?? "GET").toUpperCase();
    calls.push({
      url,
      method,
      body: init?.body ? JSON.parse(init.body as string) : undefined,
    });
    if (!url.includes("/quality-prefs"))
      throw new Error("unexpected fetch: " + url);
    if (method === "GET")
      return jsonResponse({
        tier: "medium",
        maxResolution: 1080,
        protocol: "usenet",
        undoDepth,
      });
    return noContent();
  });
  vi.stubGlobal("fetch", fn);
  return calls;
};

const depthField = () =>
  screen.getByLabelText("Undoable recent Applies") as HTMLInputElement;

describe("Settings → Library — quality prefs: Rename Undo depth", () => {
  it("loads the stored depth, dirty-tracks an edit, and PUTs it with the other prefs", async () => {
    const calls = stubQualityFetch(25);
    render(() => <QualityPrefsSection mode={() => "movies"} />);

    // Seeded from the GET, not from the 10 placeholder the signal starts at.
    await waitFor(() => expect(depthField().value).toBe("25"));

    fireEvent.input(depthField(), { target: { value: "3" } });
    fireEvent.click(screen.getByText("Save"));

    await waitFor(() =>
      expect(calls.some((c) => c.method === "PUT")).toBe(true),
    );
    expect(calls.find((c) => c.method === "PUT")!.body).toEqual({
      tier: "medium",
      maxResolution: 1080,
      protocol: "usenet",
      undoDepth: 3,
    });
  });

  // The backend rejects undoDepth outside 1..100 with a 400 for the WHOLE
  // quality-prefs request, so an out-of-range value would take tier /
  // maxResolution / protocol down with it. Three cases, each reaching the guard
  // by a different route, and NONE of them blocked by the input's own
  // min/max/step attributes:
  //   ""    — a cleared field; Number("") === 0, below the minimum.
  //   "150" — above the maximum; browsers let out-of-range values be typed.
  //   "5.5" — a FRACTION, and the one a bare range check misses entirely: it
  //           sits inside 1..100, so without Number.isInteger the Save button
  //           stays enabled and PUTs a float that Go's *int decode 400s.
  it.each([
    ["cleared", ""],
    ["above the maximum", "150"],
    ["fractional", "5.5"],
  ])("blocks Save while the depth is %s, and PUTs nothing", async (_label, value) => {
    // 25, deliberately NOT the component's own 10 placeholder — waiting on a
    // value the signal already holds before the GET resolves would let the
    // rest of this test run against unloaded tier/maxResolution/protocol.
    const calls = stubQualityFetch(25);
    render(() => <QualityPrefsSection mode={() => "movies"} />);
    await waitFor(() => expect(depthField().value).toBe("25"));

    const saveButton = screen.getByText("Save") as HTMLButtonElement;
    fireEvent.input(depthField(), { target: { value } });
    await waitFor(() => expect(saveButton.disabled).toBe(true));

    // A disabled button ignores clicks at the DOM level — this confirms the
    // whole card's save really cannot fire, not just that it looks greyed out.
    fireEvent.click(saveButton);
    expect(calls.some((c) => c.method === "PUT")).toBe(false);

    // Correcting the value re-enables it and the save goes through intact.
    fireEvent.input(depthField(), { target: { value: "7" } });
    await waitFor(() => expect(saveButton.disabled).toBe(false));
    fireEvent.click(saveButton);
    await waitFor(() =>
      expect(calls.some((c) => c.method === "PUT")).toBe(true),
    );
    expect(calls.find((c) => c.method === "PUT")!.body).toEqual({
      tier: "medium",
      maxResolution: 1080,
      protocol: "usenet",
      undoDepth: 7,
    });
  });

  it("carries the help text explaining what the depth controls", async () => {
    stubQualityFetch(10);
    render(() => <QualityPrefsSection mode={() => "movies"} />);

    expect(
      await screen.findByText(
        /how many recent Applies stay undoable before the oldest is pruned/,
      ),
    ).toBeInTheDocument();
  });
});
