// FE-5 — the Adult overlay is SUB-TAB-SCOPED, never whole-screen.
//
// This is AC6's closing clause, and it is the one property in this feature
// that a plausible-looking implementation gets wrong: raising the overlay at
// the Discover screen level would hide Mainstream Discover too, which is
// explicitly forbidden. Locking `adult-content` must leave every Mainstream
// row rendering exactly as before.
//
// The complementary hiding decision (ModeTabs drops the Adult tab entirely on
// the five workflow screens) is deliberately NOT the treatment here: Discover
// keeps the Adult tab visible and puts a PIN box behind it, because there is a
// real unlock affordance to offer and a vanished tab would be
// indistinguishable from Adult mode being switched off.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { DiscoverAdult, DiscoverMainstream } from "../Discover";
import {
  AdultModeContext,
  SectionLockContext,
  type SectionLockControl,
} from "../../components/ui";
import { ADULT_CONTENT_SECTION } from "../../api/sectionLock";
import { jsonResponse } from "../../testing/http";


// adultLockedControl is the exact scenario under test: ONLY adult-content is
// locked. `discover` itself is not, so the screen mounts normally.
const adultLockedControl: SectionLockControl = {
  isLocked: (s) => s === ADULT_CONTENT_SECTION,
  lockedSections: () => [ADULT_CONTENT_SECTION],
  pinSet: () => true,
  unlocked: () => false,
  enforcementAvailable: () => true,
  refetch: () => {},
};

// defaults answer the background fetches MainstreamDiscover fires on mount
// with empties, except the one row this test asserts on.
const defaults = (url: string): Response | null => {
  if (url.includes("/api/connections")) return jsonResponse([]);
  if (url.includes("/newest-rows")) return jsonResponse([]);
  if (url.includes("/discover/detail")) return jsonResponse({ seasons: [] });
  if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
    return jsonResponse([
      {
        id: 1,
        title: "Mainstream Title",
        posterPath: "/p.jpg",
        overview: "",
        releaseDate: "2024-01-01",
        voteAverage: 7,
        mediaType: "movie",
      },
    ]);
  if (url.includes("/discover/genres"))
    return jsonResponse([{ id: 10759, name: "Action & Adventure" }]);
  if (url.includes("/discover")) return jsonResponse([]);
  if (url.includes("/tracked")) return jsonResponse([]);
  if (url.includes("/poster")) return jsonResponse({ posterPath: "" });
  if (url.includes("/api/trakt/status"))
    return jsonResponse({ configured: false, linked: false });
  if (url.includes("/studios")) return jsonResponse([]);
  if (url.includes("/performers")) return jsonResponse([]);
  return null;
};

const renderLockedMainstream = () => {
  const seen: string[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    seen.push(url);
    const d = defaults(url);
    if (d) return d;
    throw new Error("unexpected fetch: " + url);
  });
  vi.stubGlobal("fetch", fetchMock);
  render(() => (
    <AdultModeContext.Provider value={{ enabled: () => true, refetch: () => {} }}>
      <SectionLockContext.Provider value={adultLockedControl}>
        <DiscoverMainstream />
      </SectionLockContext.Provider>
    </AdultModeContext.Provider>
  ));
  return seen;
};

const renderLockedAdult = () => {
  const seen: string[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    seen.push(url);
    const d = defaults(url);
    if (d) return d;
    throw new Error("unexpected fetch: " + url);
  });
  vi.stubGlobal("fetch", fetchMock);
  render(() => (
    <AdultModeContext.Provider value={{ enabled: () => true, refetch: () => {} }}>
      <SectionLockContext.Provider value={adultLockedControl}>
        <DiscoverAdult />
      </SectionLockContext.Provider>
    </AdultModeContext.Provider>
  ));
  return seen;
};

afterEach(() => vi.unstubAllGlobals());

describe("FE-5 — Adult overlay is sub-tab-scoped", () => {
  it("Mainstream Discover still renders when only adult-content is locked", async () => {
    renderLockedMainstream();
    fireEvent.click(await screen.findByRole("button", { name: "Movies" }));
    expect(await screen.findByText("Trending Movies")).toBeInTheDocument();
    expect(await screen.findByText("Mainstream Title")).toBeInTheDocument();
    expect(screen.queryByLabelText("Section PIN")).toBeNull();
  });

  it("the Adult route shows the PIN overlay and does not mount AdultDiscover", async () => {
    const seen = renderLockedAdult();
    expect(
      await screen.findByText("Adult content is locked"),
    ).toBeInTheDocument();
    expect(screen.getByLabelText("Section PIN")).toBeInTheDocument();
    expect(seen.some((u) => u.includes("/api/modes/adult/"))).toBe(false);
  });
});
