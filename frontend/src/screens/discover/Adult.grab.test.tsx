// AdultDiscover grab-plumbing tests for the direct-enclosure (D4/C1) feature:
//
//   1. sceneTarget() must carry downloadUrl/downloadProtocol from the card when
//      the item has them, so the SELECT-MODE BULK BATCH dispatches directly to
//      the download client instead of silently falling through to a Prowlarr
//      search. When the item has no enclosure, those keys must be ABSENT
//      (JSON.stringify drops undefined), so the Prowlarr path runs unchanged.
//
// Which case proves what, stated precisely because it is easy to over-read: the
// POSITIVE case is the plumbing proof (it fails if sceneTarget() stops carrying
// the two fields). The NEGATIVE case pins the JSON.stringify-drops-undefined
// behavior — an absence assertion cannot, on its own, distinguish "correctly
// omitted for this fixture" from "never sent at all", which is why it also
// asserts the fields the fixture does carry.
//
// Claude 2026-08-02: both cases used to enter through AdultCard's inline Grab
// button and assert on POST /api/modes/adult/autograb. That button was removed
// by the Discover card cleanup, and DetailPopup — the affordance that replaced
// it — CANNOT carry downloadUrl/downloadProtocol (its Adult grab always uses a
// Prowlarr-sourced candidate). So select-mode bulk grab is now the ONLY surface
// that reaches the Prowlarr-skipping direct-enclosure path, and these two tests
// are re-pointed at it: POST /api/autograb-batch, asserting on items[0].request.
// Reason: .omc/plans/autopilot-impl-discover-card-cleanup.md §0.2 accepts that
// capability loss ON THE GROUNDS THAT select-mode preserves it (GATE-A). §8's
// AC9 makes exercising that mitigation mandatory — a mitigation nobody runs is
// an assertion, not a mitigation. These tests are what make GATE-A answerable
// with evidence rather than a code-trace claim.
// Troubleshooting: without this coverage, a future change to selection.tsx's
// buildBatch or to sceneTarget() could drop the two fields with every existing
// test still green, silently re-routing every fresh-feed Adult grab through a
// Prowlarr search.
// Review if: DetailPopup ever learns to thread downloadUrl through its Adult
// grab — the single-card path would be restored and these could cover it too.
//
// SCOPE OF THE PROOF, stated precisely: these assert the two fields survive the
// card → selection.register → buildBatch → POST body chain. Whether the BACKEND
// then skips Prowlarr is server-side and unobservable from here; that half is
// internal/autograb's own contract.
//
// (The former masked-feed-URL enable-toggle test was removed with consensus plan
// Step 13: RSS feed admin — including the feedUrl:null-on-toggle three-state —
// moved off Discover into Settings → UI → Discover → Adult, so feeds no longer
// appear in Adult's RowEditor at all. Feed decoupling is now covered by
// Adult.rssfeed.test.tsx; the three-state save is the Settings panel's own test.)
//
// Renders the exported AdultDiscover directly (fewer stubs than the full
// Discover shell). Conventions mirror Discover.test.tsx (stubFetch/Call +
// a defaults answerer for the mount fetches).

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@solidjs/testing-library";
import type { AutoGrabBatchItem } from "@dto";
import { AdultDiscover } from "./Adult";
import { BulkBar } from "./BulkBar";
import { SelectionProvider, createSelection } from "./selection";

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

type Call = { url: string; method: string; body: unknown };
type Override = (
  url: string,
  init?: RequestInit,
) => Response | undefined | Promise<Response | undefined>;

const stubFetch = (override?: Override) => {
  const calls: Call[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = (init?.method ?? "GET").toUpperCase();
    calls.push({
      url,
      method,
      body: init?.body ? JSON.parse(init.body as string) : undefined,
    });
    if (override) {
      const r = await override(url, init);
      if (r) return r;
    }
    throw new Error(`unexpected fetch: ${method} ${url}`);
  });
  vi.stubGlobal("fetch", fn);
  return calls;
};

// defaults answers AdultDiscover's mount fetches with empties so each test only
// special-cases what it asserts on. Returns null for anything unrecognized.
const defaults = (url: string): Response | null => {
  // No /api/connections and no row-order stub: Adult reads neither any more
  // (the connections-gated stash-box rows are gone, and its row order lives in
  // adultnewest's sort_order column, not the per-screen KV store).
  if (url.includes("/api/discover/rss-feeds")) return jsonResponse([]);
  if (url.includes("/api/modes/adult/newest-rows")) return jsonResponse([]);
  if (url.includes("/api/modes/adult/studios")) return jsonResponse([]);
  if (url.includes("/api/modes/adult/performers")) return jsonResponse([]);
  return null;
};

// batchResponse is a well-formed AutoGrabBatchResponse for a one-item batch.
// Deliberately NOT a bare {} : BulkBar catches a thrown/malformed response into
// its own `error` signal, and the POST is recorded either way — so a loose stub
// would let the payload assertions below pass while the flow actually failed.
// Asserting the "✓ Grabbed" row BulkResultModal renders from this is what pins
// the test to the happy path.
const batchResponse = (label: string) =>
  jsonResponse({
    results: [
      {
        index: 0,
        mode: "adult",
        label,
        grabbed: true,
        fallback: false,
        message: `auto-grabbed ${label}`,
      },
    ],
  });

// oneSceneRow wires a single enabled "scene" newest row whose resolve returns
// the given item — the simplest path to a selectable AdultCard.
const oneSceneRow =
  (item: Record<string, unknown>): Override =>
  (url) => {
    if (url.includes("/newest-rows/1/resolve")) return jsonResponse([item]);
    if (url.includes("/api/modes/adult/newest-rows"))
      return jsonResponse([
        {
          id: 1,
          title: "Newest Scenes",
          rowType: "scene",
          sortOrder: 0,
          enabled: true,
          createdAt: "2026-01-01T00:00:00Z",
          updatedAt: "2026-01-01T00:00:00Z",
        },
      ]);
    if (url.includes("/api/autograb-batch"))
      return batchResponse(String(item.title));
    return defaults(url) ?? undefined;
  };

// renderInSelectMode mounts AdultDiscover the way the Discover shell does
// (index.tsx wraps the tab content in SelectionProvider and renders BulkBar as a
// sibling), with select-mode already on. BulkBar is NOT inside AdultDiscover, so
// it must be mounted here or "Grab all" never exists.
const renderInSelectMode = () => {
  const selection = createSelection();
  selection.setSelectMode(true);
  render(() => (
    <SelectionProvider store={selection}>
      <AdultDiscover />
      <BulkBar />
    </SelectionProvider>
  ));
};

// batchItems pulls the submitted item list out of the recorded POST body.
const batchItems = (calls: Call[]): AutoGrabBatchItem[] => {
  const batch = calls.find((c) => c.url.includes("/api/autograb-batch"));
  expect(batch?.method).toBe("POST");
  return (batch?.body as { items: AutoGrabBatchItem[] }).items;
};

afterEach(() => vi.unstubAllGlobals());

describe("AdultDiscover — sceneTarget direct-enclosure (D4/C1) via select-mode bulk grab", () => {
  it("carries downloadUrl/downloadProtocol into the autograb-batch request when the scene's feed is fresh", async () => {
    const calls = stubFetch(
      oneSceneRow({
        id: "n1",
        title: "Fresh Scene",
        studio: "Vixen",
        date: "2026-01-02",
        // Non-empty so the card renders an <img>, not the TextPoster fallback —
        // the fallback would repeat the title, giving findByText two matches.
        image: "https://cdn.theporndb.net/scenes/fresh.jpg",
        source: "tpdb",
        rowType: "scene",
        durationSeconds: 1800,
        releaseTitle: "Fresh.Scene.2026.1080p",
        downloadUrl: "https://feed.example/fetch/abc.torrent",
        protocol: "torrent",
        sizeBytes: 2147483648,
      }),
    );

    renderInSelectMode();
    const card = (await screen.findByText("Fresh Scene")).closest(
      ".w-\\[240px\\]",
    ) as HTMLElement;

    // In select-mode the card body toggles selection instead of opening
    // DetailPopup, which raises BulkBar.
    fireEvent.click(within(card).getByText("Fresh Scene"));
    fireEvent.click(await screen.findByText("Grab all"));

    // The happy path really completed — BulkResultModal rendered the grabbed
    // row from the stubbed response, so the assertions below are about a
    // successful submission, not a swallowed error.
    expect(await screen.findByText("✓ Grabbed")).toBeInTheDocument();

    const items = batchItems(calls);
    expect(items).toHaveLength(1);
    // The load-bearing assertion (AC9's second Adult leg): the direct-enclosure
    // fields survive into the BULK payload, so the Prowlarr-skipping capability
    // the removed inline Grab button used to carry still has a live surface.
    expect(items[0]).toMatchObject({
      mode: "adult",
      request: {
        title: "Fresh Scene",
        studio: "Vixen",
        releaseTitle: "Fresh.Scene.2026.1080p",
        durationSeconds: 1800,
        downloadUrl: "https://feed.example/fetch/abc.torrent",
        downloadProtocol: "torrent",
      },
    });
  });

  it("omits downloadUrl/downloadProtocol (Prowlarr fallback) when the scene has no enclosure", async () => {
    const calls = stubFetch(
      oneSceneRow({
        id: "n2",
        title: "Browse Scene",
        studio: "Blacked",
        date: "2026-01-03",
        image: "https://cdn.theporndb.net/scenes/browse.jpg",
        source: "tpdb",
        rowType: "scene",
        durationSeconds: 0,
        // no downloadUrl / protocol — a browse-only or stale-feed entity.
      }),
    );

    renderInSelectMode();
    const card = (await screen.findByText("Browse Scene")).closest(
      ".w-\\[240px\\]",
    ) as HTMLElement;

    fireEvent.click(within(card).getByText("Browse Scene"));
    fireEvent.click(await screen.findByText("Grab all"));
    expect(await screen.findByText("✓ Grabbed")).toBeInTheDocument();

    // Via unknown: AutoGrabRequest has no index signature, and the point of this
    // assertion is exactly that the two optional keys are ABSENT from the
    // serialized body (JSON.stringify drops undefined), which needs a raw
    // property-presence check rather than a typed field read.
    const request = batchItems(calls)[0]!.request as unknown as Record<
      string,
      unknown
    >;
    // The absence assertions alone would pass even if sceneTarget() stopped
    // reading downloadUrl/protocol ENTIRELY — i.e. under the exact regression
    // the case above exists to catch. Pin the fields this fixture DOES carry
    // first, so a change that guts the request shape fails here too and this
    // case cannot go quietly vacuous.
    expect(request).toMatchObject({
      title: "Browse Scene",
      studio: "Blacked",
    });
    expect("downloadUrl" in request).toBe(false);
    expect("downloadProtocol" in request).toBe(false);
  });

  // §9.5 — Adult release-persistence: sceneTarget box/sceneId wiring (A2(c)).
  it("carries box+sceneId into the batch request for catalog-sourced cards (A2(c))", async () => {
    const calls = stubFetch(
      oneSceneRow({
        id: "scene-uuid-xyz",
        title: "Catalog Scene",
        studio: "AdultTime",
        date: "2026-02-01",
        image: "https://cdn.theporndb.net/scenes/catalog.jpg",
        source: "stashdb",
        rowType: "scene",
        durationSeconds: 2400,
        performers: ["Performer A"],
        // No downloadUrl — browse-only catalog card; the point here is box/sceneId.
      }),
    );

    renderInSelectMode();
    const card = (await screen.findByText("Catalog Scene")).closest(
      ".w-\\[240px\\]",
    ) as HTMLElement;

    fireEvent.click(within(card).getByText("Catalog Scene"));
    fireEvent.click(await screen.findByText("Grab all"));
    expect(await screen.findByText("✓ Grabbed")).toBeInTheDocument();

    const items = batchItems(calls);
    expect(items).toHaveLength(1);
    // box+sceneId must survive into the batch payload so the backend resolver
    // can use the stable box:sceneId cache key rather than the title-fallback key.
    expect(items[0]).toMatchObject({
      mode: "adult",
      request: {
        title: "Catalog Scene",
        studio: "AdultTime",
        box: "stashdb",
        sceneId: "scene-uuid-xyz",
      },
    });
  });

  it("omits box/sceneId for prowlarr-sourced (Show More) cards", async () => {
    const calls = stubFetch(
      oneSceneRow({
        id: "",
        title: "ShowMore Scene",
        studio: "Brazzers",
        date: "2026-02-02",
        image: "https://cdn.theporndb.net/scenes/showmore.jpg",
        // source="prowlarr" + empty id — the Show More item shape.
        source: "prowlarr",
        rowType: "scene",
        durationSeconds: 1200,
      }),
    );

    renderInSelectMode();
    const card = (await screen.findByText("ShowMore Scene")).closest(
      ".w-\\[240px\\]",
    ) as HTMLElement;

    fireEvent.click(within(card).getByText("ShowMore Scene"));
    fireEvent.click(await screen.findByText("Grab all"));
    expect(await screen.findByText("✓ Grabbed")).toBeInTheDocument();

    const request = batchItems(calls)[0]!.request as unknown as Record<
      string,
      unknown
    >;
    // Pin the fields that ARE present so this can't go vacuously green.
    expect(request).toMatchObject({ title: "ShowMore Scene", studio: "Brazzers" });
    // A2(c): prowlarr-sourced cards must never send box or sceneId.
    expect("box" in request).toBe(false);
    expect("sceneId" in request).toBe(false);
  });
});
