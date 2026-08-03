// AdultDiscover RSS-feed decoupling tests (consensus plan Step 13 / Verification
// Step 9): RSS feeds are no longer part of Discover's row-order/editor system —
// their admin moved to Settings → UI → Discover → Adult. This file proves the
// three guarantees that decoupling must hold on the Discover side:
//
//   1. RSS feed rows NO LONGER appear in edit-mode RowEditor (no drag handle,
//      no enabled toggle, no delete) — feeds left the editor's row set
//      entirely, so they can never reach RowEditor.
//   2. Every OTHER row type is unaffected and still fully editable/reorderable
//      in RowEditor: the admin newest row keeps its drag handle + Enabled
//      toggle + Delete. (This guarantee used to be carried by a stash-box
//      STRUCTURAL row as well. Adult has no structural rows at all any more —
//      the optional StashDB/FansDB rows are gone — so the admin newest row is
//      the only kind left to exercise it.)
//   3. RssFeedRow still renders on the page for every ENABLED adult feed, on an
//      independent path positioned AFTER all editor-managed rows and ordered by
//      the feed's own sort_order (a DOM-position check, not mere presence) —
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
// a prefix with their own list endpoints, so they must be matched FIRST.
// Deliberately stubs NO /api/connections, no row-order and no row-hidden: Adult
// reads none of the three any more (the connections-gated stash-box rows are
// gone, and its order lives in adultnewest's sort_order column), so stubbing
// them would only hide a regression — anything unstubbed throws.
const mounts = (url: string): Response | undefined => {
  // newest-row items (…/newest-rows/{id}/resolve) BEFORE the list endpoint.
  if (url.includes("/newest-rows/") && url.includes("/resolve"))
    return jsonResponse([]);
  if (url.includes("/newest-rows/performer-genders")) return jsonResponse([]);
  if (url.includes("/api/modes/adult/newest-rows"))
    return jsonResponse(newestRows);
  // feed items (…/rss-feeds/{id}/resolve) BEFORE the list endpoint.
  if (url.includes("/rss-feeds/") && url.includes("/resolve"))
    return jsonResponse([]);
  if (url.includes("/api/discover/rss-feeds")) return jsonResponse(feeds);
  return undefined;
};

afterEach(() => vi.unstubAllGlobals());

describe("AdultDiscover — RSS feeds decoupled from the row editor", () => {
  it("excludes feed rows from edit-mode RowEditor while leaving the admin newest row editable/reorderable", async () => {
    stubFetch(mounts);

    render(() => <AdultDiscover editMode={() => true} />);

    // The admin newest row (entity) is present in the editor and reorderable +
    // enable-toggleable — unchanged, and now the ONLY row kind the editor
    // manages on this screen. Awaited because it enters the editor only after
    // its async newest-rows resource resolves.
    await screen.findByLabelText("Drag My Newest Row");
    expect(screen.getByLabelText("My Newest Row enabled")).toBeTruthy();
    expect(screen.getByText("Delete")).toBeTruthy();

    // RSS feeds appear NOWHERE in the editor: no drag handle, no enabled toggle
    // for any feed (enabled, disabled, or non-adult). This is the central fix —
    // feeds are no longer tracked by the row-order store at all.
    expect(screen.queryByLabelText("Drag Feed A")).toBeNull();
    expect(screen.queryByLabelText("Feed A enabled")).toBeNull();
    expect(screen.queryByLabelText("Drag Feed B")).toBeNull();
    expect(screen.queryByLabelText("Feed B enabled")).toBeNull();
    expect(screen.queryByLabelText("Drag Feed C Disabled")).toBeNull();
  });

  it("renders RssFeedRow for every enabled adult feed, after all editor-managed rows, in sort_order", async () => {
    stubFetch(mounts);

    render(() => <AdultDiscover />);

    // Both enabled adult feeds render their own RssFeedRow (Carousel title).
    const feedA = await screen.findByText("Feed A");
    const feedB = await screen.findByText("Feed B");

    // Disabled and non-adult feeds render nothing.
    expect(screen.queryByText("Feed C Disabled")).toBeNull();
    expect(screen.queryByText("Movie Feed")).toBeNull();

    // Positioned AFTER all editor-managed rows: the admin newest row precedes
    // Feed A in the DOM. Presence alone would pass even if the independent
    // render path were mis-placed, so this asserts real document order.
    //
    // Claude 2026-08-02: this used to lead with a "StashDB Trending" structural
    // row and assert it too. That row no longer exists anywhere in Adult, so
    // the assertion could only be deleted — but NOT the guarantee it was half
    // of. The admin-newest-row line below is the surviving, and now sole,
    // expression of "feeds render after everything the row editor manages",
    // which is what guarantee 3 in the file header has always been about.
    // Review if: Adult ever regains a row type the editor manages that is not
    // an admin newest row.
    const newest = await screen.findByText("My Newest Row");
    const FOLLOWING = Node.DOCUMENT_POSITION_FOLLOWING;
    expect(newest.compareDocumentPosition(feedA) & FOLLOWING).toBeTruthy();

    // Feeds render in sort_order: Feed A (sortOrder 0) before Feed B (1).
    expect(feedA.compareDocumentPosition(feedB) & FOLLOWING).toBeTruthy();
  });
});
