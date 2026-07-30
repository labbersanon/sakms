// AdultDiscover RSS-derived newestrow: Performers/Studios tests (US-6):
//
//   1. Dynamic gender-split — a Performer row renders one PaginatedStrip PER
//      DISCOVERED gender value (fetchPerformerGenders), not a hardcoded
//      Female/Male pair. Tested against a 3-gender fixture to prove the count
//      is driven by the data, not a fixed set.
//   2. Studio rows stay a single, non-gender-split strip, with the
//      drill-down wired via EntityCard's onSelect.
//   3. Clicking a newestrow Performer/Studio card opens the EXISTING
//      scene-list drill-down shell (not a new component), which calls
//      fetchNewestEntityScenes and renders it as a real paginated strip: page 1
//      (pool-only) items show immediately with a "Show more" control always
//      available (per the {items, hasMore} envelope contract — page 1's
//      hasMore is always true, even for an empty pool), and clicking "Show
//      more" fetches page 2 (Prowlarr-triggered), appends its items, and hides
//      "Show more" afterward (page 2's hasMore is always false).
//
// Renders the exported AdultDiscover directly (same convention as
// Adult.grab.test.tsx / Adult.rssfeed.test.tsx).

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { AdultDiscover } from "./Adult";

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

type Call = { url: string };
type Override = (url: string) => Response | undefined;

const stubFetch = (override?: Override) => {
  const calls: Call[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    calls.push({ url });
    if (override) {
      const r = override(url);
      if (r) return r;
    }
    throw new Error(`unexpected fetch: ${url}`);
  });
  vi.stubGlobal("fetch", fn);
  return calls;
};

// mounts answers AdultDiscover's mount fetches with empties, so each test
// only special-cases what it asserts on.
const mounts = (url: string): Response | undefined => {
  if (url.includes("/api/connections")) return jsonResponse([]);
  if (url.includes("/api/discover/row-order/adult"))
    return jsonResponse({ keys: [] });
  if (url.includes("/api/discover/row-hidden/adult"))
    return jsonResponse({ keys: [] });
  if (url.includes("/api/discover/rss-feeds")) return jsonResponse([]);
  return undefined;
};

afterEach(() => vi.unstubAllGlobals());

describe("AdultDiscover — dynamic gender-split Performers rows", () => {
  it("renders one strip per discovered gender value (3 genders -> 3 strips), not a hardcoded Female/Male pair", async () => {
    stubFetch((url) => {
      if (url.includes("/newest-rows/performer-genders"))
        return jsonResponse(["female", "male", "non-binary"]);
      if (url.includes("/newest-rows/1/resolve")) {
        // Non-empty image on every fixture below so each card renders an
        // <img>, not the TextPoster text fallback — the fallback repeats the
        // name as its own text node, which would give findByText two matches.
        if (url.includes("gender=female"))
          return jsonResponse([{ id: "p-f", title: "Perf Female", studio: "", date: "", image: "https://cdn.theporndb.net/performers/f.jpg", source: "", rowType: "performer" }]);
        if (url.includes("gender=male"))
          return jsonResponse([{ id: "p-m", title: "Perf Male", studio: "", date: "", image: "https://cdn.theporndb.net/performers/m.jpg", source: "", rowType: "performer" }]);
        if (url.includes("gender=non-binary"))
          return jsonResponse([{ id: "p-nb", title: "Perf NB", studio: "", date: "", image: "https://cdn.theporndb.net/performers/nb.jpg", source: "", rowType: "performer" }]);
        return jsonResponse([]);
      }
      if (url.includes("/api/modes/adult/newest-rows"))
        return jsonResponse([
          { id: 1, title: "Performers", rowType: "performer", sortOrder: 0, enabled: true, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
        ]);
      return mounts(url);
    });

    render(() => <AdultDiscover />);

    // Three strips, one per discovered gender — titled from the gender value
    // itself (capitalized), not a fixed Female/Male pair.
    expect(await screen.findByText("Female Performers")).toBeInTheDocument();
    expect(await screen.findByText("Male Performers")).toBeInTheDocument();
    expect(await screen.findByText("Non-binary Performers")).toBeInTheDocument();
    // The old ungendered single "Performers" strip title never renders.
    expect(screen.queryByText("Performers")).toBeNull();

    // Each strip fetched its OWN gender leg.
    expect(await screen.findByText("Perf Female")).toBeInTheDocument();
    expect(await screen.findByText("Perf Male")).toBeInTheDocument();
    expect(await screen.findByText("Perf NB")).toBeInTheDocument();
  });

  it("renders no strip at all when no gender has been discovered yet (sparse/pre-backfill state)", async () => {
    stubFetch((url) => {
      if (url.includes("/newest-rows/performer-genders")) return jsonResponse([]);
      if (url.includes("/api/modes/adult/newest-rows"))
        return jsonResponse([
          { id: 1, title: "Performers", rowType: "performer", sortOrder: 0, enabled: true, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
        ]);
      return mounts(url);
    });

    render(() => <AdultDiscover />);

    await screen.findByPlaceholderText("Search scenes by title…");
    expect(screen.queryByText(/Performers$/)).toBeNull();
  });
});

describe("AdultDiscover — Studio rows: single strip, drill-down wired", () => {
  it("renders one non-gender-split strip, with onSelect opening the newest drill-down", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/modes/adult/discover/newest/entity-scenes"))
        return jsonResponse({
          items: [
            { id: "sc1", title: "Drill Scene", studio: "Drill Studio", date: "2026-01-01", image: "https://cdn.theporndb.net/scenes/drill.jpg", durationSeconds: 0, rating: 0, source: "prowlarr", slug: "" },
          ],
          hasMore: true,
        });
      if (url.includes("/newest-rows/1/resolve"))
        return jsonResponse([
          { id: "st1", title: "Drill Studio", studio: "", date: "", image: "https://cdn.theporndb.net/sites/drill.jpg", source: "", rowType: "studio" },
        ]);
      if (url.includes("/api/modes/adult/newest-rows"))
        return jsonResponse([
          { id: 1, title: "Studios", rowType: "studio", sortOrder: 0, enabled: true, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
        ]);
      return mounts(url);
    });

    render(() => <AdultDiscover />);

    // A single "Studios" strip, no gender split.
    expect(await screen.findByText("Studios")).toBeInTheDocument();
    expect(screen.queryByText("Female Studios")).toBeNull();

    const card = await screen.findByText("Drill Studio");
    expect(card.closest("button")).toBeTruthy();
    fireEvent.click(card.closest("button") as HTMLElement);

    // Opens the existing drill-down shell (not a new component): "Back to
    // browse" + the entity's page-1 (pool-only) scenes, fetched via
    // fetchNewestEntityScenes with page=1.
    expect(await screen.findByText("Back to browse")).toBeInTheDocument();
    expect(await screen.findByText("Drill Scene")).toBeInTheDocument();
    expect(
      calls.some((c) => {
        const s = c.url;
        return (
          s.includes("/api/modes/adult/discover/newest/entity-scenes") &&
          s.includes("kind=studio") &&
          s.includes("name=Drill+Studio") &&
          s.includes("page=1")
        );
      }),
    ).toBe(true);
  });

  it("shows page-1 pool items with Show more available (even when page 1 is empty), then Show more fetches page 2, appends its items, and disappears", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/modes/adult/discover/newest/entity-scenes")) {
        if (url.includes("page=2")) {
          return jsonResponse({
            items: [
              { id: "sc-live", title: "Live Scene", studio: "Drill Studio", date: "", image: "https://cdn.theporndb.net/scenes/live.jpg", durationSeconds: 0, rating: 0, source: "prowlarr", slug: "" },
            ],
            hasMore: false,
          });
        }
        // page 1: pool has nothing yet for this entity — an empty items array
        // with hasMore:true is correct (the live search hasn't run), not a bug.
        return jsonResponse({ items: [], hasMore: true });
      }
      if (url.includes("/newest-rows/1/resolve"))
        return jsonResponse([
          { id: "st1", title: "Drill Studio", studio: "", date: "", image: "https://cdn.theporndb.net/sites/drill.jpg", source: "", rowType: "studio" },
        ]);
      if (url.includes("/api/modes/adult/newest-rows"))
        return jsonResponse([
          { id: 1, title: "Studios", rowType: "studio", sortOrder: 0, enabled: true, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
        ]);
      return mounts(url);
    });

    render(() => <AdultDiscover />);

    const card = await screen.findByText("Drill Studio");
    fireEvent.click(card.closest("button") as HTMLElement);

    expect(await screen.findByText("Back to browse")).toBeInTheDocument();
    // Page 1 came back empty, but "Show more" must still be reachable
    // (hasMore:true on an empty page 1 is the documented contract).
    const showMore = await screen.findByText("Show more");
    expect(screen.queryByText("Live Scene")).toBeNull();

    fireEvent.click(showMore);

    // Page 2's item is appended…
    expect(await screen.findByText("Live Scene")).toBeInTheDocument();
    // …and "Show more" is gone now that hasMore:false.
    expect(screen.queryByText("Show more")).toBeNull();

    expect(
      calls.some((c) => {
        const s = c.url;
        return (
          s.includes("/api/modes/adult/discover/newest/entity-scenes") &&
          s.includes("kind=studio") &&
          s.includes("name=Drill+Studio") &&
          s.includes("page=2")
        );
      }),
    ).toBe(true);
  });
});
