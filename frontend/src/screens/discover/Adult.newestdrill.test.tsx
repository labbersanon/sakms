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
import { jsonResponse } from "../../testing/http";


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
  // No /api/connections, row-order or row-hidden stub: Adult reads none of them
  // any more (the connections-gated stash-box rows are gone, and its row order
  // lives in adultnewest's sort_order column, not the per-screen KV store).
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

// ---------------------------------------------------------------------------
// Three-row drill-down restructure + bio banner.
//
// Row 1 is the Back button plus the entity name promoted into it as a real
// <h2> (ungated — both kinds), row 2 is the catalog bio banner, row 3 is the
// scene strip, whose heading is now the literal "Scenes" rather than the
// entity name.
//
// ⚠ The name renders exactly once ONLY FOR AN ENTITY THAT HAS ARTWORK, and
// that caveat is measured, not assumed. An image-less entity with a bio still
// renders its name TWICE in the drill view: once in the <h2>, once inside the
// banner's `<Show fallback={<TextPoster label={d().name} />}>`. That is the
// SAME TextPoster hazard the gender-split fixtures above document at the top
// of this file — the drill restructure does not eliminate it (it is a browse-
// card property), it reproduces its shape one level down. So every fixture
// below gives the drilled entity a NON-EMPTY image, and a name distinct from
// any scene's `studio` field (AdultCard's subtitle is `studio · year`, so a
// scene whose studio equals the entity name and whose date is blank produces a
// second exact-text match unrelated to this restructure).
// ---------------------------------------------------------------------------

const DESCRIPTION_URL = "/api/modes/adult/discover/description";

// newestEntityStub answers the mount + browse + drill fetches for a single
// RSS-derived newest row of the given kind, so each test below only has to
// special-case the description endpoint.
const newestEntityStub =
  (kind: "studio" | "performer", name: string) =>
  (url: string): Response | undefined => {
    if (url.includes("/api/modes/adult/discover/newest/entity-scenes"))
      return jsonResponse({
        items: [
          { id: "sc1", title: "Drill Scene", studio: "Some Other Studio", date: "2026-01-01", image: "https://cdn.theporndb.net/scenes/drill.jpg", durationSeconds: 0, rating: 0, source: "prowlarr", slug: "" },
        ],
        hasMore: true,
      });
    if (url.includes("/newest-rows/performer-genders"))
      return jsonResponse(["female"]);
    if (url.includes("/newest-rows/1/resolve"))
      return jsonResponse([
        { id: "e1", title: name, studio: "", date: "", image: "https://cdn.theporndb.net/entities/e1.jpg", source: "", rowType: kind },
      ]);
    if (url.includes("/api/modes/adult/newest-rows"))
      return jsonResponse([
        { id: 1, title: kind === "studio" ? "Studios" : "Performers", rowType: kind, sortOrder: 0, enabled: true, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
      ]);
    return mounts(url);
  };

// openDrill clicks the browse card for `name`, which unmounts the browse rows
// and mounts the drill-down shell.
const openDrill = async (name: string) => {
  const card = await screen.findByText(name);
  fireEvent.click(card.closest("button") as HTMLElement);
  await screen.findByText("Back to browse");
};

// banner is the bio banner's outer framed block. `items-start` is unique to it
// within Adult.tsx (it is also load-bearing styling — see the component's own
// comment), which makes it a precise "is the whole banner absent?" probe: the
// no-bio case must render NO framed block at all, not an empty one.
const banner = (container: HTMLElement) =>
  container.querySelector('div[class~="items-start"]');

describe("AdultDiscover — drill-down row 1: promoted entity name + 'Scenes' strip", () => {
  it("renders the studio name once, as an h2 beside Back to browse, with the strip titled 'Scenes'", async () => {
    stubFetch((url) => {
      if (url.includes(DESCRIPTION_URL)) return jsonResponse({ text: "", source: "" });
      return newestEntityStub("studio", "Vixen Media")(url);
    });

    const { container } = render(() => <AdultDiscover />);
    await openDrill("Vixen Media");

    const h2 = container.querySelector("h2");
    expect(h2?.textContent).toBe("Vixen Media");
    // The strip heading is the literal "Scenes" for a STUDIO drill too — the
    // one gating mistake that would silently cost studios their name display.
    expect(await screen.findByText("Scenes")).toBeInTheDocument();
    expect(screen.queryAllByText("Vixen Media")).toHaveLength(1);
  });

  it("renders the performer name once, as an h2, with the strip titled 'Scenes'", async () => {
    stubFetch((url) => {
      if (url.includes(DESCRIPTION_URL)) return jsonResponse({ text: "", source: "" });
      return newestEntityStub("performer", "Angela Vance")(url);
    });

    const { container } = render(() => <AdultDiscover />);
    await openDrill("Angela Vance");

    const h2 = container.querySelector("h2");
    expect(h2?.textContent).toBe("Angela Vance");
    expect(await screen.findByText("Scenes")).toBeInTheDocument();
    expect(screen.queryAllByText("Angela Vance")).toHaveLength(1);
  });
});

describe("AdultDiscover — drill-down row 2: the catalog bio banner", () => {
  it("renders the banner with the bio text when the catalog has one, and frames a performer 2:3 / object-cover", async () => {
    stubFetch((url) => {
      if (url.includes(DESCRIPTION_URL))
        return jsonResponse({ text: "An award-winning performer.", source: "tpdb" });
      return newestEntityStub("performer", "Angela Vance")(url);
    });

    const { container } = render(() => <AdultDiscover />);
    await openDrill("Angela Vance");

    expect(await screen.findByText("An award-winning performer.")).toBeInTheDocument();
    const frame = banner(container);
    expect(frame).not.toBeNull();

    const art = frame?.querySelector("div");
    expect(art?.className).toContain("aspect-[2/3]");
    expect(art?.className).toContain("w-20");
    expect(art?.className).not.toContain("aspect-video");

    const img = frame?.querySelector("img");
    expect(img?.className).toContain("object-cover");
    expect(img?.className).not.toContain("object-contain");
  });

  it("frames a studio 16:9 / object-contain (a logo is letterboxed, never cropped)", async () => {
    stubFetch((url) => {
      if (url.includes(DESCRIPTION_URL))
        return jsonResponse({ text: "A studio known for its cinematography.", source: "tpdb" });
      return newestEntityStub("studio", "Vixen Media")(url);
    });

    const { container } = render(() => <AdultDiscover />);
    await openDrill("Vixen Media");

    expect(
      await screen.findByText("A studio known for its cinematography."),
    ).toBeInTheDocument();
    const frame = banner(container);
    const art = frame?.querySelector("div");
    expect(art?.className).toContain("aspect-video");
    expect(art?.className).toContain("w-36");
    expect(art?.className).not.toContain("aspect-[2/3]");

    const img = frame?.querySelector("img");
    expect(img?.className).toContain("object-contain");
    expect(img?.className).not.toContain("object-cover");
  });

  it("renders the banner container but no bio text when the catalog has no bio (no placeholder paragraph)", async () => {
    // Claude 2026-08-24: test updated — banner container now always renders on
    // a drill to host the Monitor switch (hoisted out of <Show when={entityBio}>).
    // The behavioral contract that changed: no-bio → container present, bio <p>
    // absent (not "container absent" as before). The no-placeholder-paragraph
    // guarantee is preserved: the bio text <p> only renders when text is non-empty.
    stubFetch((url) => {
      if (url.includes(DESCRIPTION_URL)) return jsonResponse({ text: "", source: "" });
      return newestEntityStub("studio", "Vixen Media")(url);
    });

    const { container } = render(() => <AdultDiscover />);
    await openDrill("Vixen Media");
    // The scene strip renders, so the drill is fully settled.
    expect(await screen.findByText("Drill Scene")).toBeInTheDocument();

    // Banner container IS present (hosts the Monitor switch).
    const frame = banner(container);
    expect(frame).not.toBeNull();
    // But no bio paragraph inside the banner — scoped inside the frame to
    // avoid matching AdultCard hover-overlay <p class="line-clamp-4"> nodes.
    expect(frame?.querySelector('p[class~="line-clamp-4"]')).toBeNull();
  });

  it("survives a rejected description fetch: banner present (no crash), no bio text (this app has no ErrorBoundary)", async () => {
    // Claude 2026-08-24: test updated — banner container now always renders.
    // The no-crash guarantee (swallowed error → null resource, not SPA crash)
    // is unchanged. The banner is present (Monitor switch lives there); the bio
    // paragraph is absent since the fetch failed → empty text.
    stubFetch((url) => {
      // Fall through to stubFetch's `throw new Error("unexpected fetch")` for
      // the description endpoint — an unswallowed rejection here would make the
      // resource errored, and Solid re-throws on read, taking the SPA down.
      if (url.includes(DESCRIPTION_URL)) return undefined;
      return newestEntityStub("studio", "Vixen Media")(url);
    });

    const { container } = render(() => <AdultDiscover />);
    await openDrill("Vixen Media");

    expect(await screen.findByText("Drill Scene")).toBeInTheDocument();
    // Banner present (Monitor switch), no bio paragraph inside banner.
    const frame = banner(container);
    expect(frame).not.toBeNull();
    expect(frame?.querySelector('p[class~="line-clamp-4"]')).toBeNull();
    expect(container.querySelector("h2")?.textContent).toBe("Vixen Media");
  });

  it("fires the description fetch ONCE per drill open — paginating the strip fires no second call", async () => {
    const calls = stubFetch((url) => {
      if (url.includes(DESCRIPTION_URL))
        return jsonResponse({ text: "A studio bio.", source: "tpdb" });
      if (url.includes("/api/modes/adult/discover/newest/entity-scenes")) {
        if (url.includes("page=2"))
          return jsonResponse({
            items: [
              { id: "sc-live", title: "Live Scene", studio: "Some Other Studio", date: "2026-02-02", image: "https://cdn.theporndb.net/scenes/live.jpg", durationSeconds: 0, rating: 0, source: "prowlarr", slug: "" },
            ],
            hasMore: false,
          });
        return jsonResponse({ items: [], hasMore: true });
      }
      return newestEntityStub("studio", "Vixen Media")(url);
    });

    render(() => <AdultDiscover />);
    await openDrill("Vixen Media");
    await screen.findByText("A studio bio.");

    const descriptionCalls = () =>
      calls.filter((c) => c.url.includes(DESCRIPTION_URL)).length;
    expect(descriptionCalls()).toBe(1);

    fireEvent.click(await screen.findByText("Show more"));
    expect(await screen.findByText("Live Scene")).toBeInTheDocument();

    // The resource is keyed on the `drill` signal, which "Show more" never
    // touches — so pagination cannot re-fire it.
    expect(descriptionCalls()).toBe(1);
  });
});

describe("AdultDiscover — drill-down row 2: no stale bio across a drill switch", () => {
  // Regression test for a Phase 4 architect finding: entityDetail is a
  // createResource keyed on the `drill` signal. Solid resources RETAIN their
  // previous value while a new fetch is in flight (they don't clear on a key
  // change), so opening studio A's drill (bio loads) → Back to browse → open
  // studio B's drill used to render A's bio inside B's banner for the whole
  // duration of B's fetch. This test drives B's fetch with a manually
  // resolvable promise (the deferred-fetch pattern
  // shared.paginatedstrip.test.tsx already uses) so it can assert on the
  // in-flight state, not just the eventual result.
  it("does not show studio A's stale bio while studio B's fetch is pending, then shows B's own bio once it resolves", async () => {
    let resolveB: ((r: Response) => void) | undefined;
    const fn = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes(DESCRIPTION_URL)) {
        if (url.includes("name=Studio+A"))
          return jsonResponse({ text: "Bio for A.", source: "tpdb" });
        if (url.includes("name=Studio+B")) {
          return new Promise<Response>((res) => {
            resolveB = res;
          });
        }
      }
      if (url.includes("/api/modes/adult/discover/newest/entity-scenes"))
        return jsonResponse({ items: [], hasMore: true });
      if (url.includes("/newest-rows/1/resolve"))
        return jsonResponse([
          { id: "e1", title: "Studio A", studio: "", date: "", image: "https://cdn.theporndb.net/entities/a.jpg", source: "", rowType: "studio" },
          { id: "e2", title: "Studio B", studio: "", date: "", image: "https://cdn.theporndb.net/entities/b.jpg", source: "", rowType: "studio" },
        ]);
      if (url.includes("/api/modes/adult/newest-rows"))
        return jsonResponse([
          { id: 1, title: "Studios", rowType: "studio", sortOrder: 0, enabled: true, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
        ]);
      const m = mounts(url);
      if (m) return m;
      throw new Error(`unexpected fetch: ${url}`);
    });
    vi.stubGlobal("fetch", fn);

    render(() => <AdultDiscover />);

    await openDrill("Studio A");
    expect(await screen.findByText("Bio for A.")).toBeInTheDocument();

    fireEvent.click(screen.getByText("Back to browse"));
    await openDrill("Studio B");

    // B's description fetch is still pending here — A's stale bio (still
    // sitting in the resource, since Solid never clears it on a key change)
    // must not leak into B's banner. The banner container IS present (Monitor
    // switch lives there regardless of bio state — see 2026-08-24 hoist), but
    // the bio paragraph must be absent while B's text is loading.
    // Claude 2026-08-24: removed `expect(banner(container)).toBeNull()` —
    // banner container now always renders on drill. The stale-bio guarantee
    // is fully covered by the queryByText("Bio for A.") check above it.
    expect(screen.queryByText("Bio for A.")).toBeNull();

    resolveB!(jsonResponse({ text: "Bio for B.", source: "tpdb" }));

    expect(await screen.findByText("Bio for B.")).toBeInTheDocument();
    expect(screen.queryByText("Bio for A.")).toBeNull();
  });
});
