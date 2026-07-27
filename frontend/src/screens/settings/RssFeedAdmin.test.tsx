// RssFeedAdmin tests — the Settings "RSS Feeds" panel (target=adult feeds).
// Covers: render, Edit (no protocol field, preserves protocol/enabled/feedUrl),
// Add (no protocol field, shows the detected protocol), Re-scan (updates the
// displayed protocol), the inconclusive-detection fallback pop-up for BOTH the
// Add and Re-scan paths (and that completing it retries with the chosen
// protocol), and the reorder id-mapping. Conventions mirror
// SliderAdmin.test.tsx (stubFetch/defaultGet/Call). A real pointer drag isn't
// simulable in jsdom (same as RowEditor.test), so drag reorder is verified via
// the pure reorderAdultFeedIds mapping instead.

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  fireEvent,
  render,
  screen,
  waitFor,
} from "@solidjs/testing-library";
import { RssFeedAdminSection, reorderAdultFeedIds } from "./RssFeedAdmin";
import type { RssFeed } from "../../api/rssFeeds";

const jsonResponse = (obj: unknown, status = 200): Response =>
  new Response(JSON.stringify(obj), {
    status,
    headers: { "Content-Type": "application/json" },
  });
const noContent = (): Response => new Response(null, { status: 204 });
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

  it("is a plain identity when every feed is adult", () => {
    const all: RssFeed[] = [feed({ id: 1 }), feed({ id: 2 }), feed({ id: 3 })];
    expect(reorderAdultFeedIds(all, [3, 1, 2])).toEqual([3, 1, 2]);
  });
});

describe("RssFeedAdminSection — list", () => {
  it("shows the empty state with no feeds", async () => {
    stubFetch();
    render(() => <RssFeedAdminSection />);
    expect(await screen.findByText("No RSS feeds yet.")).toBeInTheDocument();
  });

  it("lists adult feeds with their read-only protocol, excluding non-adult feeds", async () => {
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
    expect(screen.getByText("usenet")).toBeInTheDocument();
    // A non-adult feed never appears in this Adult panel.
    expect(screen.queryByText("Mainstream Movies")).not.toBeInTheDocument();
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
    fireEvent.click(await screen.findByText("Edit"));

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
  it("creates with no protocol field and shows the detected protocol", async () => {
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
    // The detected protocol shows once the list refreshes.
    expect(await screen.findByText("torrent")).toBeInTheDocument();
  });
});

describe("RssFeedAdminSection — re-scan", () => {
  it("updates the displayed protocol on a confident re-scan", async () => {
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
    expect(screen.getByText("usenet")).toBeInTheDocument();
    fireEvent.click(screen.getByText("Re-scan"));
    expect(await screen.findByText("torrent")).toBeInTheDocument();
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
    fireEvent.click(screen.getByText("Re-scan"));

    expect(await screen.findByText("Choose a protocol")).toBeInTheDocument();
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
