// AdultDiscover merged Studios/Performers row tests (plan Task 6+7):
//
//   1. Drill routing — clicking a merged card opens a drill that fetches scenes
//      from the MERGED-scenes endpoint by the card's id-pair (both tpdbId AND
//      stashdbId threaded), never the old TPDB-only /performers/{id}/scenes path
//      and never a click-time fuzzy re-match (plan Q2/G8).
//   2. EntityCard divergent-name provenance surfacing — a namesDiverged card
//      renders BOTH "TPDB:" and "StashDB:" names as two genuinely-visible lines
//      (per-line line-clamp, NOT one shared single-line truncate that would clip
//      the second name at 200px — the whole point of surfacing both, plan Q3 /
//      Fold-in B).
//   3. EntityCard common-case regression guard — a non-diverged card renders its
//      single name in one `truncate` line exactly as before, with no source
//      labels.
//
// Renders the exported AdultDiscover directly (same convention as
// Adult.grab.test.tsx). "studios"/"performers" are always in knownKeys, so
// mocking the -merged endpoints is enough to get both rows on screen.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { AdultDiscover } from "./Adult";

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

type Call = { url: string; method: string };
type Override = (url: string) => Response | undefined;

const stubFetch = (override?: Override) => {
  const calls: Call[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push({ url, method: (init?.method ?? "GET").toUpperCase() });
    if (override) {
      const r = override(url);
      if (r) return r;
    }
    throw new Error(`unexpected fetch: ${url}`);
  });
  vi.stubGlobal("fetch", fn);
  return calls;
};

// mounts answers AdultDiscover's mount fetches with empties. The -merged checks
// MUST come before any plain /studios|/performers check, since
// "/api/modes/adult/studios" is a substring of ".../studios-merged" (advisor
// flag). Row-order/row-hidden/connections/feeds/newest-rows all empty so only
// the two merged rows render.
const mounts = (url: string): Response | undefined => {
  if (url.includes("/api/connections")) return jsonResponse([]);
  if (url.includes("/api/discover/row-order/adult"))
    return jsonResponse({ keys: [] });
  if (url.includes("/api/discover/row-hidden/adult"))
    return jsonResponse({ keys: [] });
  if (url.includes("/api/discover/rss-feeds")) return jsonResponse([]);
  if (url.includes("/api/modes/adult/newest-rows")) return jsonResponse([]);
  return undefined;
};

afterEach(() => vi.unstubAllGlobals());

describe("AdultDiscover — merged card drill routing", () => {
  it("opens the merged-scenes drill by the card's id-pair (both ids threaded)", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/performers-merged/scenes"))
        return jsonResponse([]);
      // Paired Perf is female, so only the Female strip lists it; the Male
      // strip is empty and the name resolves to a single card (a real
      // performer only ever appears in one gender's strip).
      if (url.includes("gender=male")) return jsonResponse([]);
      if (url.includes("/api/modes/adult/performers-merged"))
        return jsonResponse([
          {
            // Non-empty image so the card renders an <img>, not the TextPoster
            // fallback (which would repeat the name and give findByText two hits).
            name: "Paired Perf",
            image: "https://cdn.theporndb.net/performers/pp.jpg",
            source: "merged",
            tpdbId: "t1",
            stashdbId: "s1",
          },
        ]);
      if (url.includes("/api/modes/adult/studios-merged"))
        return jsonResponse([]);
      return mounts(url);
    });

    render(() => <AdultDiscover />);
    const name = await screen.findByText("Paired Perf");
    fireEvent.click(name.closest("button") as HTMLElement);

    await vi.waitFor(() =>
      expect(
        calls.some((c) => c.url.includes("/performers-merged/scenes")),
      ).toBe(true),
    );
    const drill = calls.find((c) =>
      c.url.includes("/performers-merged/scenes"),
    )!;
    // Both ids on the wire (browse-time pairing is authoritative), and NOT the
    // old TPDB-only /performers/{id}/scenes drill path.
    expect(drill.url).toContain("tpdbId=t1");
    expect(drill.url).toContain("stashdbId=s1");
    expect(
      calls.some((c) => /\/performers\/[^/]+\/scenes/.test(c.url)),
    ).toBe(false);
  });
});

describe("AdultDiscover — EntityCard divergent-name provenance surfacing", () => {
  it("renders BOTH labeled names on two line-clamped lines, not one shared truncate", async () => {
    stubFetch((url) => {
      // Divergent-name performer is female → only the Female strip renders it,
      // so "TPDB: TpdbName"/"StashDB: StashName" each resolve to one element.
      if (url.includes("gender=male")) return jsonResponse([]);
      if (url.includes("/api/modes/adult/performers-merged"))
        return jsonResponse([
          {
            name: "StashName",
            altName: "TpdbName",
            namesDiverged: true,
            image: "",
            source: "merged",
            tpdbId: "t1",
            stashdbId: "s1",
          },
        ]);
      if (url.includes("/api/modes/adult/studios-merged"))
        return jsonResponse([]);
      return mounts(url);
    });

    render(() => <AdultDiscover />);
    const tpdbLine = await screen.findByText("TPDB: TpdbName");
    const stashLine = await screen.findByText("StashDB: StashName");

    // Both names genuinely present as their OWN elements.
    expect(tpdbLine).toBeTruthy();
    expect(stashLine).toBeTruthy();
    // Each name sits on its own per-line line-clamp-1 row — the visibility
    // guarantee. A shared single-line `truncate` (the rejected naive approach)
    // would clip the second name at 200px, defeating provenance surfacing.
    expect(tpdbLine.className).toContain("line-clamp-1");
    expect(stashLine.className).toContain("line-clamp-1");
    expect(tpdbLine.className).not.toContain("truncate");
    expect(stashLine.className).not.toContain("truncate");
  });
});

describe("AdultDiscover — EntityCard non-diverged common-case regression", () => {
  it("renders a single truncate line with no source labels when not diverged", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/adult/studios-merged"))
        return jsonResponse([
          {
            // Non-empty image so the single name line is the only "SoloStudio"
            // in the DOM (the TextPoster fallback would repeat it).
            name: "SoloStudio",
            image: "https://cdn.theporndb.net/sites/solo.jpg",
            source: "tpdb",
            tpdbId: "t2",
          },
        ]);
      if (url.includes("/api/modes/adult/performers-merged"))
        return jsonResponse([]);
      return mounts(url);
    });

    render(() => <AdultDiscover />);
    const line = await screen.findByText("SoloStudio");

    // Unchanged common case: one truncate line, no "TPDB:"/"StashDB:" labels.
    expect(line.className).toContain("truncate");
    expect(line.className).not.toContain("line-clamp");
    expect(screen.queryByText(/TPDB:/)).toBeNull();
    expect(screen.queryByText(/StashDB:/)).toBeNull();
    // The card is a real drill button even for a single-source card.
    expect(line.closest("button")).toBeTruthy();
  });
});

describe("AdultDiscover — gender-split Performers rows (F2/F4)", () => {
  it("renders one Studios strip + Female/Male Performers strips (each with its gender arg), no ungendered Performers strip", async () => {
    // A local counting IntersectionObserver spy proves the merged strips opted
    // into infiniteScroll (each live sentinel constructs one); afterEach's
    // vi.unstubAllGlobals restores the setup's inert default.
    let observerCount = 0;
    vi.stubGlobal(
      "IntersectionObserver",
      class {
        constructor() {
          observerCount++;
        }
        observe() {}
        unobserve() {}
        disconnect() {}
        takeRecords() {
          return [];
        }
      },
    );

    // Empty-but-hasMore envelopes keep each strip advanceable (sentinel live)
    // without needing card fixtures — this test asserts structure, not content.
    const calls = stubFetch((url) => {
      if (url.includes("/api/modes/adult/performers-merged"))
        return jsonResponse({ items: [], hasMore: true });
      if (url.includes("/api/modes/adult/studios-merged"))
        return jsonResponse({ items: [], hasMore: true });
      return mounts(url);
    });

    render(() => <AdultDiscover />);

    // The Performers block is two gender-scoped strips; Studios stays single.
    expect(await screen.findByText("Female Performers")).toBeInTheDocument();
    expect(screen.getByText("Male Performers")).toBeInTheDocument();
    expect(screen.getByText("Studios")).toBeInTheDocument();
    // The old ungendered single "Performers" strip no longer exists.
    expect(screen.queryByText("Performers")).toBeNull();

    // Each Performers strip fetched its OWN gender leg; Studios fetched the
    // merged (gender-blind) endpoint.
    await vi.waitFor(() => {
      expect(
        calls.some(
          (c) =>
            c.url.includes("performers-merged") && c.url.includes("gender=female"),
        ),
      ).toBe(true);
      expect(
        calls.some(
          (c) =>
            c.url.includes("performers-merged") && c.url.includes("gender=male"),
        ),
      ).toBe(true);
      expect(calls.some((c) => c.url.includes("studios-merged"))).toBe(true);
    });

    // infiniteScroll on all three merged strips: no manual "Show more" button,
    // and each trailing-edge sentinel constructed an IntersectionObserver.
    expect(screen.queryByText("Show more")).toBeNull();
    await vi.waitFor(() => expect(observerCount).toBeGreaterThan(0));
  });
});
