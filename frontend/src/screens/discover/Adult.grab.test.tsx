// AdultDiscover grab-plumbing tests for the direct-enclosure (D4/C1) feature
// and the masked-feed-URL three-state toggle:
//
//   1. sceneTarget() must carry downloadUrl/downloadProtocol from the card when
//      the item has them, so the single Grab (and, via the same GrabTarget, the
//      bulk batch) dispatches directly to the download client instead of
//      silently falling through to a Prowlarr search. When the item has no
//      enclosure, those keys must be ABSENT (JSON.stringify drops undefined), so
//      the Prowlarr path runs unchanged.
//   2. The RSS-feed enable/disable toggle must send feedUrl: null (preserve) —
//      NOT the masked "" value f.feedUrl now holds, which the backend rejects.
//
// Renders the exported AdultDiscover directly (fewer stubs than the full
// Discover shell). Conventions mirror Discover.test.tsx (stubFetch/Call +
// a defaults answerer for the mount fetches).

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@solidjs/testing-library";
import { AdultDiscover } from "./Adult";

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
  if (url.includes("/api/connections")) return jsonResponse([]);
  if (url.includes("/api/discover/row-order/adult"))
    return jsonResponse({ keys: [] });
  if (url.includes("/api/discover/rss-feeds")) return jsonResponse([]);
  if (url.includes("/api/modes/adult/newest-rows")) return jsonResponse([]);
  if (url.includes("/api/modes/adult/studios")) return jsonResponse([]);
  if (url.includes("/api/modes/adult/performers")) return jsonResponse([]);
  return null;
};

// oneSceneRow wires a single enabled "scene" newest row whose resolve returns
// the given item — the simplest path to a grab-able AdultCard.
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
    if (url.includes("/api/modes/adult/autograb"))
      return jsonResponse({ grabbed: true, fallback: false, message: "Grabbed." });
    return defaults(url) ?? undefined;
  };

afterEach(() => vi.unstubAllGlobals());

describe("AdultDiscover — sceneTarget direct-enclosure (D4/C1)", () => {
  it("threads downloadUrl/downloadProtocol into the autograb request when the scene's feed is fresh", async () => {
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

    render(() => <AdultDiscover />);
    const card = (await screen.findByText("Fresh Scene")).closest(
      ".w-\\[200px\\]",
    ) as HTMLElement;
    fireEvent.click(within(card).getByText("Grab"));

    await vi.waitFor(() =>
      expect(
        calls.some((c) => c.url.includes("/api/modes/adult/autograb")),
      ).toBe(true),
    );

    const grab = calls.find((c) =>
      c.url.includes("/api/modes/adult/autograb"),
    );
    expect(grab?.method).toBe("POST");
    expect(grab?.body).toMatchObject({
      title: "Fresh Scene",
      studio: "Vixen",
      releaseTitle: "Fresh.Scene.2026.1080p",
      durationSeconds: 1800,
      // The load-bearing assertion: the direct-enclosure fields are present, so
      // the backend dispatches straight to the download client (skips Prowlarr).
      downloadUrl: "https://feed.example/fetch/abc.torrent",
      downloadProtocol: "torrent",
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

    render(() => <AdultDiscover />);
    const card = (await screen.findByText("Browse Scene")).closest(
      ".w-\\[200px\\]",
    ) as HTMLElement;
    fireEvent.click(within(card).getByText("Grab"));

    await vi.waitFor(() =>
      expect(
        calls.some((c) => c.url.includes("/api/modes/adult/autograb")),
      ).toBe(true),
    );

    const grab = calls.find((c) =>
      c.url.includes("/api/modes/adult/autograb"),
    );
    const body = grab?.body as Record<string, unknown>;
    expect("downloadUrl" in body).toBe(false);
    expect("downloadProtocol" in body).toBe(false);
  });
});

describe("AdultDiscover — RSS feed enable toggle three-state", () => {
  it("sends feedUrl: null (preserve) on toggle, never the masked display value", async () => {
    const feed = {
      id: 9,
      title: "Adult Feed",
      // Masked on read, exactly as the real backend now emits it.
      feedUrl: "",
      target: "adult",
      protocol: "usenet",
      sortOrder: 0,
      enabled: true,
      createdAt: "2026-01-01T00:00:00Z",
      updatedAt: "2026-01-01T00:00:00Z",
    };
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/discover/rss-feeds/9") && init?.method === "PUT")
        return jsonResponse({ ...feed, enabled: false });
      if (url.includes("/api/discover/rss-feeds")) return jsonResponse([feed]);
      return defaults(url) ?? undefined;
    });

    // editMode true → the browse rows are swapped for RowEditor, which exposes
    // the per-feed "enabled" checkbox that drives toggleRowEnabled.
    render(() => <AdultDiscover editMode={() => true} />);

    const toggle = (await screen.findByLabelText(
      "Adult Feed enabled",
    )) as HTMLInputElement;
    fireEvent.click(toggle);

    await vi.waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.url.includes("/api/discover/rss-feeds/9") && c.method === "PUT",
        ),
      ).toBe(true),
    );

    const put = calls.find(
      (c) => c.url.includes("/api/discover/rss-feeds/9") && c.method === "PUT",
    );
    const body = put?.body as Record<string, unknown>;
    // Explicit null on the wire — discriminates the fix from both the old
    // `feedUrl: f.feedUrl` (which posted "") bug and a plain omit.
    expect(body.feedUrl).toBeNull();
    expect(body.enabled).toBe(false);
    expect(body.title).toBe("Adult Feed");
  });
});
