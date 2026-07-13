import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import type {
  AdultDiscoverItem,
  AvailabilityResponse,
  DiscoverItem,
} from "@dto";
import { Discover } from "./Discover";

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

const movie = (over: Partial<DiscoverItem>): DiscoverItem => ({
  id: 1,
  title: "Trending Movie",
  posterPath: "/poster1.jpg",
  overview: "An overview.",
  releaseDate: "2024-05-01",
  voteAverage: 7.8,
  mediaType: "movie",
  ...over,
});

const scene = (over: Partial<AdultDiscoverItem>): AdultDiscoverItem => ({
  id: "s1",
  title: "A Scene",
  studio: "Tushy",
  date: "2023-02-02",
  image: "https://cdn.theporndb.net/scenes/abc.jpg",
  durationSeconds: 1800,
  ...over,
});

const avail = (over: Partial<AvailabilityResponse> = {}): AvailabilityResponse => ({
  available: true,
  releaseCount: 3,
  checkedAt: "2026-07-13T00:00:00Z",
  ...over,
});

type Handler = (url: string) => Response | Promise<Response>;
const stubFetch = (handler: Handler) => {
  const fn = vi.fn(async (input: RequestInfo | URL) => handler(String(input)));
  vi.stubGlobal("fetch", fn);
  return fn;
};

afterEach(() => vi.unstubAllGlobals());

describe("Discover — Movies/Series title view", () => {
  it("renders a hero, rows, poster cards, and availability badges", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending")) {
        return jsonResponse([movie({ id: 1, title: "Hero Movie" }), movie({ id: 2, title: "Second Movie" })]);
      }
      if (url.includes("/api/modes/movies/discover") && url.includes("popular")) {
        return jsonResponse([movie({ id: 3, title: "Popular Movie" })]);
      }
      if (url.includes("/api/modes/movies/availability")) {
        return jsonResponse(avail());
      }
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);

    // The top trending title appears in BOTH the hero and the trending row
    // (Netflix-style — the featured item is also first in its row), so expect
    // multiple matches.
    expect((await screen.findAllByText("Hero Movie")).length).toBeGreaterThan(1);
    // A poster card from the Popular row renders too (row-only, so unique).
    expect(await screen.findByText("Popular Movie")).toBeInTheDocument();
    // Availability badge resolves.
    expect(await screen.findAllByText("3 available")).not.toHaveLength(0);
  });

  it("routes every poster image through the image proxy — never hot-links image.tmdb.org", async () => {
    stubFetch((url) => {
      if (url.includes("/discover") && url.includes("trending")) {
        return jsonResponse([movie({ id: 1, title: "Hero Movie", posterPath: "/p1.jpg" })]);
      }
      if (url.includes("/discover") && url.includes("popular")) {
        return jsonResponse([movie({ id: 2, title: "Pop", posterPath: "/p2.jpg" })]);
      }
      if (url.includes("/availability")) return jsonResponse(avail());
      throw new Error("unexpected fetch: " + url);
    });

    const { container } = render(() => <Discover />);
    await screen.findAllByText("Hero Movie");

    const imgs = Array.from(container.querySelectorAll("img"));
    expect(imgs.length).toBeGreaterThan(0);
    for (const img of imgs) {
      const src = img.getAttribute("src") ?? "";
      expect(src.startsWith("/api/images/proxy?url=")).toBe(true);
      // The raw TMDB host must be percent-encoded inside the proxy param, never
      // the <img src>'s own host (that would be a direct hot-link).
      expect(src.startsWith("https://image.tmdb.org")).toBe(false);
      expect(decodeURIComponent(src)).toContain("https://image.tmdb.org/t/p/");
    }
  });

  it("falls back to a text tile when a title has no poster", async () => {
    stubFetch((url) => {
      if (url.includes("/discover") && url.includes("trending")) {
        return jsonResponse([movie({ id: 1, title: "No Art Movie", posterPath: "" })]);
      }
      if (url.includes("/discover") && url.includes("popular")) return jsonResponse([]);
      if (url.includes("/availability")) return jsonResponse(avail({ available: false, releaseCount: 0 }));
      throw new Error("unexpected fetch: " + url);
    });

    const { container } = render(() => <Discover />);
    await screen.findAllByText("No Art Movie");
    // No <img> for a poster-less card; the title still shows via the text tile.
    expect(container.querySelectorAll("img").length).toBe(0);
  });
});

describe("Discover — mode switching + Adult view", () => {
  it("switches to Adult and renders scene cards with proxied art", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover")) return jsonResponse([]);
      if (url.includes("/api/modes/adult/discover")) {
        return jsonResponse([scene({ id: "s1", title: "Scene One" })]);
      }
      if (url.includes("/api/modes/adult/availability")) return jsonResponse(avail());
      throw new Error("unexpected fetch: " + url);
    });

    const { container } = render(() => <Discover />);
    // Start on Movies; switch to Adult.
    fireEvent.click(await screen.findByText("Adult"));

    expect(await screen.findByText("Scene One")).toBeInTheDocument();
    const imgs = Array.from(container.querySelectorAll("img"));
    expect(imgs.length).toBeGreaterThan(0);
    for (const img of imgs) {
      expect((img.getAttribute("src") ?? "").startsWith("/api/images/proxy?url=")).toBe(true);
    }
  });

  it("switches to Series (same TMDB title path, different mode) and fetches series discover", async () => {
    const fetchMock = stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover")) return jsonResponse([]);
      if (url.includes("/api/modes/series/discover")) {
        return jsonResponse([movie({ id: 42, title: "A Series", mediaType: "tv" })]);
      }
      if (url.includes("/availability")) return jsonResponse(avail());
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click(await screen.findByText("Series"));

    // The mode-keyed resources refetch against the series endpoint (title
    // shows in both hero and row, so expect it present at least once).
    expect((await screen.findAllByText("A Series")).length).toBeGreaterThan(0);
    expect(
      fetchMock.mock.calls.some(([u]) =>
        String(u).includes("/api/modes/series/discover"),
      ),
    ).toBe(true);
  });

  it("renders an Adult scene with no art as a text tile (no hot-link, no broken img)", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover")) return jsonResponse([]);
      if (url.includes("/api/modes/adult/discover")) {
        return jsonResponse([scene({ id: "s2", title: "Artless Scene", image: "" })]);
      }
      if (url.includes("/api/modes/adult/availability")) return jsonResponse(avail());
      throw new Error("unexpected fetch: " + url);
    });

    const { container } = render(() => <Discover />);
    fireEvent.click(await screen.findByText("Adult"));
    await screen.findAllByText("Artless Scene");
    expect(container.querySelectorAll("img").length).toBe(0);
  });
});

describe("Discover — TMDB/TPDB not-configured setup pop-up", () => {
  // Richer stubFetch (captures method/body) scoped to this block only — the
  // file's shared stubFetch above only exposes the URL, which the plain
  // GET-only tests never needed. Doesn't touch that shared helper.
  type Call = { url: string; method: string; body: unknown };
  const stubFetchWithCalls = (
    handler: (url: string, init?: RequestInit) => Response | Promise<Response>,
  ) => {
    const calls: Call[] = [];
    const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      calls.push({
        url,
        method: (init?.method ?? "GET").toUpperCase(),
        body: init?.body ? JSON.parse(init.body as string) : undefined,
      });
      return handler(url, init);
    });
    vi.stubGlobal("fetch", fn);
    return calls;
  };

  const notConfigured = (service: string) =>
    new Response(`${service} isn't configured yet — add it in Settings first`, {
      status: 400,
    });

  it("does not crash and shows a setup pop-up when TMDB isn't configured (regression: this used to be an uncaught exception + stuck 'Loading…')", async () => {
    const pageErrors: unknown[] = [];
    // solid-testing-library runs in jsdom; an uncaught error surfaces as an
    // unhandled 'error' event on window, which is the closest equivalent
    // available here to the real browser's uncaught-exception signal this
    // regression was originally caught with via CDP.
    const onError = (e: ErrorEvent) => pageErrors.push(e.error ?? e.message);
    window.addEventListener("error", onError);

    stubFetchWithCalls((url) => {
      if (url.includes("/api/modes/movies/discover")) return notConfigured("tmdb");
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);

    expect(await screen.findByText("Set up TMDB")).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: /themoviedb\.org\/settings\/api/i }),
    ).toHaveAttribute("href", "https://www.themoviedb.org/settings/api");
    expect(pageErrors).toHaveLength(0);

    window.removeEventListener("error", onError);
  });

  it("saving an API key from the pop-up PUTs to the connections endpoint with the correct three-state body, then refetches", async () => {
    let configured = false;
    const calls = stubFetchWithCalls((url, init) => {
      if (url.includes("/api/modes/movies/discover")) {
        return configured
          ? jsonResponse([movie({ id: 1, title: "Now Visible Movie" })])
          : notConfigured("tmdb");
      }
      if (url === "/api/connections/tmdb" && init?.method === "PUT") {
        configured = true;
        return new Response(null, { status: 204 });
      }
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    await screen.findByText("Set up TMDB");

    fireEvent.input(screen.getByPlaceholderText("API key"), {
      target: { value: "a-real-tmdb-key" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    // The title only appears once the modal's onSaved refetch succeeds
    // against the now-configured connection. findAllByText (not findByText)
    // because the mock returns the same movie for both the trending and
    // popular categories, and the intentional hero+row duplication (see the
    // comment in Discover.tsx) means it legitimately renders more than once.
    expect(
      (await screen.findAllByText("Now Visible Movie")).length,
    ).toBeGreaterThan(0);

    const putCall = calls.find(
      (c) => c.url === "/api/connections/tmdb" && c.method === "PUT",
    );
    expect(putCall?.body).toEqual({
      url: "https://api.themoviedb.org/3",
      apiKey: "a-real-tmdb-key",
    });
  });

  it("shows the TPDB pop-up (not TMDB's) when Adult's scene fetch reports tpdb not configured", async () => {
    stubFetchWithCalls((url) => {
      if (url.includes("/api/modes/movies/discover")) return jsonResponse([]);
      if (url.includes("/api/modes/adult/discover")) return notConfigured("tpdb");
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click(await screen.findByText("Adult"));

    expect(await screen.findByText("Set up TPDB")).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: /theporndb\.net\/user\/api-tokens/i }),
    ).toHaveAttribute("href", "https://theporndb.net/user/api-tokens");
  });

  it("falls back to plain error text (no pop-up) for an unrelated error", async () => {
    stubFetchWithCalls((url) => {
      if (url.includes("/api/modes/movies/discover")) {
        return new Response("internal server error", { status: 500 });
      }
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);

    expect(await screen.findByText("internal server error")).toBeInTheDocument();
    expect(screen.queryByText(/^Set up/)).not.toBeInTheDocument();
  });
});
