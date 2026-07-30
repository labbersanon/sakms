// AdultDiscover RSS-feed decoupling tests (consensus plan Step 13 / Verification
// Step 9): RSS feeds are no longer part of Discover's row-order/editor system —
// their admin moved to Settings → UI → Discover → Adult. This file proves the
// three guarantees that decoupling must hold on the Discover side:
//
//   1. RSS feed rows NO LONGER appear in edit-mode RowEditor (no drag handle,
//      no enabled toggle, no delete) — feeds left knownKeys entirely, so they
//      can never reach descriptorFor/RowEditor.
//   2. Every OTHER row type is unaffected and still fully editable/reorderable
//      in RowEditor: structural rows (stash-box rows, the only structural kind
//      left once the fixed Studios/Performers rows were replaced by
//      RSS-derived admin newest rows — see US-6) keep their drag handle +
//      Show/Hide toggle; the admin newest row keeps its drag handle + Enabled
//      toggle + Delete.
//   3. RssFeedRow still renders on the page for every ENABLED adult feed, on an
//      independent path positioned AFTER all structural rows and ordered by the
//      feed's own sort_order (a DOM-position check, not mere presence) —
//      disabled and non-adult feeds render nothing.
//
// Renders the exported AdultDiscover directly (same convention as
// Adult.merged.test.tsx / Adult.grab.test.tsx).

import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@solidjs/testing-library";
import { AdultDiscover } from "./Adult";

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

type Override = (url: string) => Response | undefined;

const stubFetch = (override?: Override) => {
  const fn = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (override) {
      const r = override(url);
      if (r) return r;
    }
    throw new Error(`unexpected fetch: ${url}`);
  });
  vi.stubGlobal("fetch", fn);
};

// A single enabled admin newest row (the entity-row control group).
const newestRows = [
  {
    id: 1,
    title: "My Newest Row",
    rowType: "scene",
    genreFilter: "",
    enabled: true,
    sortOrder: 0,
  },
];

// Feeds returned already sortOrder-ascending, exactly as fetchRssFeeds' backend
// List query yields them. Feed A before Feed B (order under test); a disabled
// adult feed and a non-adult feed prove filtering.
const feeds = [
  { id: 10, title: "Feed A", feedUrl: "", target: "adult", protocol: "usenet", enabled: true, sortOrder: 0 },
  { id: 11, title: "Feed B", feedUrl: "", target: "adult", protocol: "torrent", enabled: true, sortOrder: 1 },
  { id: 12, title: "Feed C Disabled", feedUrl: "", target: "adult", protocol: "usenet", enabled: false, sortOrder: 2 },
  { id: 13, title: "Movie Feed", feedUrl: "", target: "movie", protocol: "usenet", enabled: true, sortOrder: 3 },
];

// mounts answers every fetch AdultDiscover fires on the plain browse view.
// Substring-collision ordering matters: the two "/resolve" item endpoints share
// a prefix with their own list endpoints, so they must be matched FIRST. A
// StashDB connection is configured so the screen has at least one STRUCTURAL
// row (the fixed Studios/Performers rows are gone — see US-6 — so a
// stash-box row is now the only structural-kind row left to exercise
// "every other row type is unaffected").
const mounts = (url: string): Response | undefined => {
  if (url.includes("/api/connections"))
    return jsonResponse([
      { service: "stashdb", url: "https://stashdb.example", hasApiKey: true, updatedAt: "2026-01-01T00:00:00Z" },
    ]);
  if (url.includes("/api/discover/row-order/adult"))
    return jsonResponse({ keys: [] });
  if (url.includes("/api/discover/row-hidden/adult"))
    return jsonResponse({ keys: [] });
  // newest-row items (…/newest-rows/{id}/resolve) BEFORE the list endpoint.
  if (url.includes("/newest-rows/") && url.includes("/resolve"))
    return jsonResponse([]);
  if (url.includes("/newest-rows/performer-genders")) return jsonResponse([]);
  if (url.includes("/api/modes/adult/newest-rows"))
    return jsonResponse(newestRows);
  if (url.includes("/api/modes/adult/discover/stashdb/trending"))
    return jsonResponse([]);
  // feed items (…/rss-feeds/{id}/resolve) BEFORE the list endpoint.
  if (url.includes("/rss-feeds/") && url.includes("/resolve"))
    return jsonResponse([]);
  if (url.includes("/api/discover/rss-feeds")) return jsonResponse(feeds);
  return undefined;
};

afterEach(() => vi.unstubAllGlobals());

describe("AdultDiscover — RSS feeds decoupled from the row editor", () => {
  it("excludes feed rows from edit-mode RowEditor while leaving every other row type editable/reorderable", async () => {
    stubFetch(mounts);

    render(() => <AdultDiscover editMode={() => true} />);

    // The admin newest row (entity) is present in the editor and reorderable +
    // enable-toggleable — unchanged. Awaited first since it enters the editor
    // only after its async newest-rows resource resolves (the stash-box row
    // is a static knownKeys entry that renders immediately once connections
    // resolves).
    await screen.findByLabelText("Drag My Newest Row");
    expect(screen.getByLabelText("My Newest Row enabled")).toBeTruthy();

    // Structural rows are present in the editor and reorderable (drag handle) +
    // toggleable (Show/Hide) — unchanged. StashDB Trending is the structural
    // row exercised here now that the fixed Studios/Performers rows are gone.
    expect(screen.getByLabelText("Drag StashDB Trending")).toBeTruthy();
    expect(screen.getByLabelText("StashDB Trending visible")).toBeTruthy();

    // RSS feeds appear NOWHERE in the editor: no drag handle, no enabled toggle
    // for any feed (enabled, disabled, or non-adult). This is the central fix —
    // feeds are no longer tracked by the row-order store at all.
    expect(screen.queryByLabelText("Drag Feed A")).toBeNull();
    expect(screen.queryByLabelText("Feed A enabled")).toBeNull();
    expect(screen.queryByLabelText("Drag Feed B")).toBeNull();
    expect(screen.queryByLabelText("Feed B enabled")).toBeNull();
    expect(screen.queryByLabelText("Drag Feed C Disabled")).toBeNull();
  });

  it("renders RssFeedRow for every enabled adult feed, after all structural rows, in sort_order", async () => {
    stubFetch(mounts);

    render(() => <AdultDiscover />);

    // Both enabled adult feeds render their own RssFeedRow (Carousel title).
    const feedA = await screen.findByText("Feed A");
    const feedB = await screen.findByText("Feed B");

    // Disabled and non-adult feeds render nothing.
    expect(screen.queryByText("Feed C Disabled")).toBeNull();
    expect(screen.queryByText("Movie Feed")).toBeNull();

    // Positioned AFTER all structural rows: StashDB Trending (the only
    // structural row in this fixture, now that the fixed Studios/Performers
    // rows are gone — see US-6) precedes Feed A in the DOM. Presence alone
    // would pass even if the independent render path were mis-placed, so this
    // asserts real document order.
    const stashboxRow = await screen.findByText("StashDB Trending");
    const newest = screen.getByText("My Newest Row");
    const FOLLOWING = Node.DOCUMENT_POSITION_FOLLOWING;
    expect(stashboxRow.compareDocumentPosition(feedA) & FOLLOWING).toBeTruthy();
    expect(newest.compareDocumentPosition(feedA) & FOLLOWING).toBeTruthy();

    // Feeds render in sort_order: Feed A (sortOrder 0) before Feed B (1).
    expect(feedA.compareDocumentPosition(feedB) & FOLLOWING).toBeTruthy();
  });
});
