// RssFeedAdmin tests — the Settings "RSS Feeds" panel (target=adult feeds).
// Covers: render, Edit (no protocol field, preserves protocol/enabled/feedUrl),
// Add (no protocol field, reports the detected protocol in the status dialog),
// Re-scan (status dialog: detected + error), the inconclusive-detection
// fallback pop-up for BOTH the Add and Re-scan paths (and that completing it
// retries with the chosen protocol), and the reorder id-mapping. Conventions
// mirror SliderAdmin.test.tsx (stubFetch/defaultGet/Call).
//
// The panel renders the SHARED RowEditor now rather than its own solid-dnd
// wiring, so rows are title-only (no `protocol:` subtitle — that surface moved
// into ProtocolStatusDialog) and the per-row Edit/Re-scan controls are
// icon buttons found by aria-label.
//
// A real solid-dnd pointer drag IS simulable here — see dragRow below, copied
// from SliderAdmin.test.tsx (itself copied from AdultRowAdmin.test.tsx) rather
// than hoisted into a shared helper, since a handful of callers duplicating is
// this repo's stance. The "not simulable in jsdom" posture the earlier tests
// recorded was true only of a drag against jsdom's default all-zero
// getBoundingClientRect, which collapses every droppable onto one point.
// So reorder is covered twice over here, and both halves earn their place: the
// pure reorderAdultFeedIds mapping (composed with RowEditor's own reorderKeys,
// to pin that RowEditor's bare-id row keys still map correctly), AND a real
// drag through the live onReorder call site. Only the second can catch that
// call site handing reorderAdultFeedIds the adult SUBSET instead of the full
// feed list — a mistake the unit tests would never see, and which 422s as
// ErrReorderMismatch on any install that also has a movie/tv feed.

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  fireEvent,
  render,
  screen,
  waitFor,
} from "@solidjs/testing-library";
import { RssFeedAdminSection, reorderAdultFeedIds } from "./RssFeedAdmin";
import { reorderKeys } from "../discover/RowEditor";
import type { RssFeed } from "../../api/rssFeeds";
import { jsonResponse, noContent } from "../../testing/http";


const undetected = (): Response =>
  jsonResponse({ error: "protocol_undetected" }, 422);

type Call = { url: string; method: string; body: unknown };
type Override = (
  url: string,
  init?: RequestInit,
) => Response | undefined | Promise<Response | undefined>;

const feed = (over: Partial<RssFeed> = {}): RssFeed => ({
  id: 1,
  title: "NZBGeek",
  feedUrl: "",
  target: "adult",
  protocol: "usenet",
  sortOrder: 0,
  enabled: true,
  createdAt: "2026-07-27T00:00:00Z",
  updatedAt: "2026-07-27T00:00:00Z",
  ...over,
});

const isFeedList = (url: string) =>
  url.includes("/api/discover/rss-feeds") &&
  !url.includes("/reorder") &&
  !url.includes("/rescan") &&
  !url.includes("/resolve");

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
    if (method === "GET" && isFeedList(url)) return jsonResponse([]);
    return noContent();
  });
  vi.stubGlobal("fetch", fn);
  vi.stubGlobal("confirm", vi.fn(() => true));
  return calls;
};

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

// dragRow drives a REAL solid-dnd pointer drag of the row at fromIndex onto the
// slot of the row at toIndex. jsdom computes no layout, so every
// getBoundingClientRect is all-zero and solid-dnd's closestCenter sees every
// droppable at the same point; stubbing a 100px-tall rect per row is what makes
// the collision answer meaningful. The stubs are ELEMENT-LOCAL on purpose —
// patching Element.prototype would leak into every other test in this file.
// Then: pointerdown on the ⠿ grip (the only activator), a pointermove past the
// sensor's 10px activation distance, and pointerup to commit. Landing the move
// exactly on the target row's centre is what makes the resulting order exact.
const dragRow = (fromIndex: number, toIndex: number) => {
  const lis = Array.from(document.querySelectorAll("li")).filter((li) =>
    li.querySelector('[aria-label^="Drag "]'),
  );
  lis.forEach((li, i) => {
    li.getBoundingClientRect = () =>
      ({
        x: 0,
        y: i * 100,
        left: 0,
        top: i * 100,
        right: 500,
        bottom: i * 100 + 100,
        width: 500,
        height: 100,
        toJSON: () => ({}),
      }) as DOMRect;
  });
  const handle = lis[fromIndex]!.querySelector(
    '[aria-label^="Drag "]',
  ) as HTMLElement;
  fireEvent.pointerDown(handle, {
    clientX: 250,
    clientY: fromIndex * 100 + 50,
    button: 0,
  });
  const to = { clientX: 250, clientY: toIndex * 100 + 50 };
  fireEvent.pointerMove(document, to);
  fireEvent.pointerUp(document, to);
};

const isReorder = (c: Call) =>
  c.method === "POST" && c.url.includes("/reorder");

describe("reorderAdultFeedIds", () => {
  it("maps a new adult order back onto the full id list, preserving non-adult slots", () => {
    const all: RssFeed[] = [
      feed({ id: 10, target: "movie" }),
      feed({ id: 1, target: "adult" }),
      feed({ id: 2, target: "adult" }),
      feed({ id: 20, target: "tv" }),
      feed({ id: 3, target: "adult" }),
    ];
    // Adult feeds were [1,2,3]; drag reorders them to [2,1,3]. Movie(10)/tv(20)
    // keep their exact positions; only the adult slots are refilled.
    expect(reorderAdultFeedIds(all, [2, 1, 3])).toEqual([10, 2, 1, 20, 3]);
  });

  it("treats adult-movie slots as adult for reorder mapping", () => {
    const all: RssFeed[] = [
      feed({ id: 10, target: "movie" }),
      feed({ id: 1, target: "adult" }),
      feed({ id: 4, target: "adult-movie" }),
      feed({ id: 20, target: "tv" }),
    ];
    expect(reorderAdultFeedIds(all, [4, 1])).toEqual([10, 4, 1, 20]);
  });
});

describe("reorderAdultFeedIds ∘ RowEditor keys", () => {
  it("maps RowEditor's bare stringified-id keys through to the full id list", () => {
    // Same fixture as the describe above, so the two stay comparable. This is
    // the exact composition the RowEditor onReorder callback performs: RowEditor
    // hands back its own row keys (bare feed ids, one per ADULT feed) in the new
    // order, and the panel Number-maps them straight into reorderAdultFeedIds.
    const all: RssFeed[] = [
      feed({ id: 10, target: "movie" }),
      feed({ id: 1, target: "adult" }),
      feed({ id: 2, target: "adult" }),
      feed({ id: 20, target: "tv" }),
      feed({ id: 3, target: "adult" }),
    ];
    const adultKeys = ["1", "2", "3"];
    // Drag feed 2 onto feed 1's slot — what RowEditor's own onDragEnd computes.
    const orderedKeys = reorderKeys(adultKeys, "2", "1");
    expect(orderedKeys).toEqual(["2", "1", "3"]);
    expect(reorderAdultFeedIds(all, orderedKeys.map(Number))).toEqual([
      10, 2, 1, 20, 3,
    ]);
  });
});

describe("RssFeedAdminSection — reorder (real drag)", () => {
  it("a drag of an adult row POSTs the FULL id set, not the adult subset", async () => {
    const calls = stubFetch((url) => {
      // Mixed fixture on purpose — the adult-subset bug is invisible on an
      // adult-only install, which is exactly why the pure-mapping unit tests
      // above can't catch it.
      if (isFeedList(url))
        return jsonResponse([
          feed({ id: 10, title: "Mainstream Movies", target: "movie" }),
          feed({ id: 1, title: "First" }),
          feed({ id: 2, title: "Second" }),
          feed({ id: 20, title: "Mainstream Shows", target: "tv" }),
          feed({ id: 3, title: "Third" }),
        ]);
      return undefined;
    });
    render(() => <RssFeedAdminSection />);
    // Only the three ADULT feeds render as rows, so row 0/2 are First/Third.
    await screen.findByLabelText("Drag First");
    dragRow(0, 2);
    await waitFor(() => expect(calls.some(isReorder)).toBe(true));
    // The discriminating assertion, and the whole reason this test exists:
    // adult 1 moved into adult 3's slot while the movie(10)/tv(20) feeds keep
    // their exact positions and — critically — are still PRESENT. Passing
    // adultFeeds() instead of feeds() into reorderAdultFeedIds at the
    // onReorder call site yields { ids: [2, 3, 1] } here, which the backend
    // rejects as ErrReorderMismatch (Store.Reorder demands every existing
    // feed's id exactly once, across ALL targets).
    expect(calls.find(isReorder)!.body).toEqual({ ids: [10, 2, 3, 20, 1] });
  });
});

describe("RssFeedAdminSection — list", () => {
  it("shows the empty state with no feeds", async () => {
    stubFetch();
    render(() => <RssFeedAdminSection />);
    expect(await screen.findByText("No rows yet.")).toBeInTheDocument();
  });

  it("still offers + New feed when the list is empty", async () => {
    // The create affordance is RowEditor's `footer`, which renders as a SIBLING
    // of the empty-state <Show>. Nested inside it, a fresh install could never
    // create its first feed.
    stubFetch();
    render(() => <RssFeedAdminSection />);
    expect(await screen.findByText("No rows yet.")).toBeInTheDocument();
    expect(screen.getByText("+ New feed")).toBeInTheDocument();
  });

  it("lists adult feeds title-only, excluding non-adult feeds", async () => {
    stubFetch((url) => {
      if (isFeedList(url))
        return jsonResponse([
          feed({ id: 1, title: "NZBGeek", protocol: "usenet" }),
          feed({ id: 2, title: "Mainstream Movies", target: "movie" }),
        ]);
      return undefined;
    });
    render(() => <RssFeedAdminSection />);
    expect(await screen.findByText("NZBGeek")).toBeInTheDocument();
    // Rows are title-only now — the protocol lives in ProtocolStatusDialog.
    expect(screen.queryByText("usenet")).toBeNull();
    // A non-adult feed never appears in this Adult panel.
    expect(screen.queryByText("Mainstream Movies")).not.toBeInTheDocument();
  });

  it("renders a drag grip per adult feed", async () => {
    stubFetch((url) => {
      if (isFeedList(url))
        return jsonResponse([
          feed({ id: 1, title: "NZBGeek" }),
          feed({ id: 2, title: "Second" }),
        ]);
      return undefined;
    });
    render(() => <RssFeedAdminSection />);
    expect(await screen.findByLabelText("Drag NZBGeek")).toBeInTheDocument();
    expect(screen.getByLabelText("Drag Second")).toBeInTheDocument();
  });

  it("renders Edit/Re-scan as icon buttons, not unicode glyphs", async () => {
    stubFetch((url) => {
      if (isFeedList(url)) return jsonResponse([feed({ id: 1, title: "NZBGeek" })]);
      return undefined;
    });
    render(() => <RssFeedAdminSection />);
    const edit = await screen.findByLabelText("Edit NZBGeek");
    const rescan = screen.getByLabelText("Re-scan NZBGeek");
    for (const btn of [edit, rescan]) {
      expect(btn.querySelector("svg")).not.toBeNull();
      // Scoped to the button itself: RowEditor's grip handle legitimately
      // renders a ⠿ glyph on every row, so a container-wide check would be
      // wrong here.
      expect(btn.textContent ?? "").toBe("");
    }
  });
});

describe("RssFeedAdminSection — edit", () => {
  it("Edit expands inline with no protocol field and preserves protocol/enabled/feedUrl on save", async () => {
    const calls = stubFetch((url) => {
      if (isFeedList(url))
        return jsonResponse([
          feed({ id: 5, title: "Old", protocol: "torrent", enabled: true }),
        ]);
      return undefined;
    });
    render(() => <RssFeedAdminSection />);
    fireEvent.click(await screen.findByLabelText("Edit Old"));

    const titleInput = (await screen.findByLabelText(
      "Feed title",
    )) as HTMLInputElement;
    expect(titleInput.value).toBe("Old");
    // Protocol is derived, never an editable field — no protocol input exists.
    expect(screen.queryByLabelText("Protocol")).toBeNull();

    fireEvent.input(titleInput, { target: { value: "New" } });
    fireEvent.click(screen.getByText("Save changes"));

    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "PUT" &&
            c.url.includes("/api/discover/rss-feeds/5"),
        ),
      ).toBe(true),
    );
    const put = calls.find(
      (c) => c.method === "PUT" && c.url.includes("/api/discover/rss-feeds/5"),
    )!;
    // Only the title changed; protocol/enabled carried over from the row and
    // feedUrl sent as null to preserve the masked stored secret.
    expect(put.body).toEqual({
      title: "New",
      feedUrl: null,
      target: "adult",
      protocol: "torrent",
      enabled: true,
    });
  });
});

describe("RssFeedAdminSection — add", () => {
  it("creates with no protocol field and reports the detected protocol in the status dialog", async () => {
    let created = false;
    const calls = stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (isFeedList(url) && method === "GET")
        return jsonResponse(
          created ? [feed({ id: 9, title: "New Feed", protocol: "torrent" })] : [],
        );
      if (isFeedList(url) && method === "POST") {
        created = true;
        return jsonResponse(feed({ id: 9, title: "New Feed", protocol: "torrent" }));
      }
      return undefined;
    });
    render(() => <RssFeedAdminSection />);
    fireEvent.click(await screen.findByText("+ New feed"));

    // The add form has no protocol field either.
    expect(screen.queryByLabelText("Protocol")).toBeNull();
    fireEvent.input(screen.getByLabelText("Feed title"), {
      target: { value: "New Feed" },
    });
    fireEvent.input(screen.getByLabelText("Feed URL"), {
      target: { value: "https://nzbgeek.info/rss?x" },
    });
    fireEvent.click(screen.getByText("Create feed"));

    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "POST" &&
            c.url.endsWith("/api/discover/rss-feeds"),
        ),
      ).toBe(true),
    );
    const post = calls.find(
      (c) => c.method === "POST" && c.url.endsWith("/api/discover/rss-feeds"),
    )!;
    // No protocol key at all — the backend auto-detects.
    expect(post.body).toEqual({
      title: "New Feed",
      feedUrl: "https://nzbgeek.info/rss?x",
      target: "adult",
      enabled: true,
    });
    expect(Object.prototype.hasOwnProperty.call(post.body, "protocol")).toBe(
      false,
    );
    // Rows are title-only, so the status dialog is the ONLY place the operator
    // learns what protocol was auto-detected for the feed they just created.
    expect(await screen.findByText("Protocol detection")).toBeInTheDocument();
    expect(await screen.findByText("torrent")).toBeInTheDocument();
  });
});

describe("RssFeedAdminSection — re-scan", () => {
  it("reports the detected protocol in the status dialog on a confident re-scan", async () => {
    let protocol = "usenet";
    stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (isFeedList(url) && method === "GET")
        return jsonResponse([feed({ id: 3, title: "Rescan Me", protocol })]);
      if (url.includes("/api/discover/rss-feeds/3/rescan")) {
        protocol = "torrent";
        return jsonResponse(feed({ id: 3, title: "Rescan Me", protocol }));
      }
      return undefined;
    });
    render(() => <RssFeedAdminSection />);
    await screen.findByText("Rescan Me");
    // No protocol anywhere until the dialog opens.
    expect(screen.queryByText("usenet")).toBeNull();
    fireEvent.click(screen.getByLabelText("Re-scan Rescan Me"));
    expect(await screen.findByText("Protocol detection")).toBeInTheDocument();
    expect(await screen.findByText("torrent")).toBeInTheDocument();
  });

  it("shows a failed re-scan's message in the status dialog", async () => {
    stubFetch((url) => {
      if (isFeedList(url))
        return jsonResponse([feed({ id: 4, title: "Broken" })]);
      if (url.includes("/api/discover/rss-feeds/4/rescan"))
        return jsonResponse({ error: "feed unreachable" }, 500);
      return undefined;
    });
    render(() => <RssFeedAdminSection />);
    await screen.findByText("Broken");
    fireEvent.click(screen.getByLabelText("Re-scan Broken"));
    expect(await screen.findByText("Protocol detection")).toBeInTheDocument();
    expect(await screen.findByText("feed unreachable")).toBeInTheDocument();
  });
});

describe("RssFeedAdminSection — inconclusive-detection fallback pop-up", () => {
  it("Add path: 422 opens the pop-up, and the pick retries the create with the chosen protocol", async () => {
    let created = false;
    const calls = stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (isFeedList(url) && method === "GET")
        return jsonResponse(
          created ? [feed({ id: 8, title: "Torrents", protocol: "torrent" })] : [],
        );
      if (isFeedList(url) && method === "POST") {
        const body = init?.body ? JSON.parse(init.body as string) : {};
        if (body.protocol === undefined) return undetected();
        created = true;
        return jsonResponse(feed({ id: 8, title: "Torrents", protocol: "torrent" }));
      }
      return undefined;
    });
    render(() => <RssFeedAdminSection />);
    fireEvent.click(await screen.findByText("+ New feed"));
    fireEvent.input(screen.getByLabelText("Feed title"), {
      target: { value: "Torrents" },
    });
    fireEvent.input(screen.getByLabelText("Feed URL"), {
      target: { value: "https://tracker/rss" },
    });
    fireEvent.click(screen.getByText("Create feed"));

    // The pop-up appears (its own Protocol select is now present).
    expect(await screen.findByText("Choose a protocol")).toBeInTheDocument();
    fireEvent.change(await screen.findByLabelText("Protocol"), {
      target: { value: "torrent" },
    });
    fireEvent.click(screen.getByText("Save protocol"));

    await waitFor(() => {
      const posts = calls.filter(
        (c) => c.method === "POST" && c.url.endsWith("/api/discover/rss-feeds"),
      );
      expect(posts.length).toBe(2);
    });
    const posts = calls.filter(
      (c) => c.method === "POST" && c.url.endsWith("/api/discover/rss-feeds"),
    );
    // The retry is the SAME create body plus the chosen protocol — not a
    // second, different feed.
    expect(posts[0]!.body).toEqual({
      title: "Torrents",
      feedUrl: "https://tracker/rss",
      target: "adult",
      enabled: true,
    });
    expect(posts[1]!.body).toEqual({
      title: "Torrents",
      feedUrl: "https://tracker/rss",
      target: "adult",
      enabled: true,
      protocol: "torrent",
    });
    // The pop-up closes and the created feed shows.
    await waitFor(() =>
      expect(screen.queryByText("Choose a protocol")).not.toBeInTheDocument(),
    );
  });

  it("Re-scan path: 422 opens the pop-up, and the pick applies a protocol-only PUT", async () => {
    const calls = stubFetch((url) => {
      if (isFeedList(url))
        return jsonResponse([
          feed({ id: 7, title: "Torrents", protocol: "usenet", enabled: true }),
        ]);
      if (url.includes("/api/discover/rss-feeds/7/rescan")) return undetected();
      return undefined;
    });
    render(() => <RssFeedAdminSection />);
    await screen.findByText("Torrents");
    fireEvent.click(screen.getByLabelText("Re-scan Torrents"));

    expect(await screen.findByText("Choose a protocol")).toBeInTheDocument();
    // The status dialog hands off to the manual picker, it never coexists
    // with it — protocol is never collected by the status surface.
    expect(screen.queryByText("Protocol detection")).toBeNull();
    fireEvent.change(await screen.findByLabelText("Protocol"), {
      target: { value: "torrent" },
    });
    fireEvent.click(screen.getByText("Save protocol"));

    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "PUT" &&
            c.url.includes("/api/discover/rss-feeds/7"),
        ),
      ).toBe(true),
    );
    const put = calls.find(
      (c) => c.method === "PUT" && c.url.includes("/api/discover/rss-feeds/7"),
    )!;
    // The manual pick is applied as a full-body PUT (rescan has no manual
    // input), only protocol set to the choice, feedUrl null to preserve.
    expect(put.body).toEqual({
      title: "Torrents",
      feedUrl: null,
      target: "adult",
      protocol: "torrent",
      enabled: true,
    });
  });
});
