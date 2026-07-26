// RssFeedCard tests — the one-click Grab button's request/response/"Grabbed"
// state handling: fetches the mode's root folder, then calls manualGrab
// directly (no autograb/GrabDialog — an RSS item is already a fully-resolved
// release). Conventions mirror SliderAdmin.test.tsx (stubFetch/Call).

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import type { RssFeedItem } from "@dto";
import { RssFeedCard } from "./RssFeedCard";

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
    throw new Error("unexpected fetch: " + url);
  });
  vi.stubGlobal("fetch", fn);
  return calls;
};

afterEach(() => vi.unstubAllGlobals());

const item = (over: Partial<RssFeedItem> = {}): RssFeedItem => ({
  title: "Some.Release.2026",
  link: "https://example.com/details/1",
  pubDate: "Wed, 15 Jul 2026 12:00:00 +0000",
  sizeBytes: 1073741824,
  downloadUrl: "https://example.com/fetch/1.nzb",
  protocol: "usenet",
  indexer: "My Feed",
  ...over,
});

describe("RssFeedCard — grab", () => {
  it("fetches the root folder then calls manualGrab, showing Grabbed on success", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/modes/movies/library/root-folder"))
        return jsonResponse({ path: "/data/movies" });
      if (url.includes("/api/modes/movies/search/grab"))
        return jsonResponse({ id: 1, mode: "movies", title: "Some.Release.2026", status: "queued" });
      return undefined;
    });

    render(() => <RssFeedCard item={item()} mode="movies" />);

    expect(screen.getByText("Some.Release.2026")).toBeInTheDocument();
    fireEvent.click(screen.getByText("Grab"));

    expect(await screen.findByText("Grabbed")).toBeInTheDocument();

    const grabCall = calls.find((c) => c.url.includes("/search/grab"));
    expect(grabCall?.body).toEqual({
      title: "Some.Release.2026",
      indexer: "My Feed",
      protocol: "usenet",
      downloadUrl: "https://example.com/fetch/1.nzb",
      rootFolderPath: "/data/movies",
    });
  });

  it("shows an error and leaves the Grab button clickable again when the root folder isn't configured", async () => {
    stubFetch((url) => {
      if (url.includes("/library/root-folder")) return jsonResponse({ path: "" });
      return undefined;
    });

    render(() => <RssFeedCard item={item()} mode="adult" />);
    fireEvent.click(screen.getByText("Grab"));

    expect(
      await screen.findByText(/no root folder configured/i),
    ).toBeInTheDocument();
    expect(screen.getByText("Grab")).toBeInTheDocument();
    expect(screen.queryByText("Grabbed")).not.toBeInTheDocument();
  });

  it("renders size/pubDate/indexer meta and falls back to em dash when absent", async () => {
    stubFetch();
    render(() => (
      <RssFeedCard item={item({ sizeBytes: 0, pubDate: "", indexer: "" })} mode="series" />
    ));
    expect(screen.getByText("—")).toBeInTheDocument();
  });
});

describe("RssFeedCard — resolved Adult poster", () => {
  it("renders a proxied poster and the resolved title when resolved data is present", () => {
    const it0 = item({
      resolvedTitle: "Resolved Scene Title",
      resolvedStudio: "Resolved Studio",
      resolvedImage: "https://cdn.theporndb.net/scene-123.jpg",
    });
    render(() => <RssFeedCard item={it0} mode="adult" />);

    const img = screen.getByRole("img") as HTMLImageElement;
    // The image must flow through the Go image proxy, never hot-link the CDN.
    expect(img.src).toContain("/api/images/proxy?url=");
    expect(img.src).toContain(encodeURIComponent(it0.resolvedImage as string));

    // The resolved (cleaned) title is displayed, not the raw release name.
    expect(screen.getByText("Resolved Scene Title")).toBeInTheDocument();
    expect(screen.queryByText("Some.Release.2026")).not.toBeInTheDocument();
  });

  it("renders the plain-text tile with no poster when resolved data is absent", () => {
    render(() => <RssFeedCard item={item()} mode="movies" />);

    expect(screen.queryByRole("img")).not.toBeInTheDocument();
    // Falls back to the raw feed title exactly as before enrichment existed.
    expect(screen.getByText("Some.Release.2026")).toBeInTheDocument();
  });
});
