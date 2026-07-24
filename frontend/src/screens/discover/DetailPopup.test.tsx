// DetailPopup tests: the pure availability-grid derivation logic
// (candidateAt/computeDefaults) plus the rendered popup's selector
// disabled-state behavior, Series' season/episode gating, Adult's
// no-quality-prefs default path, and the Grab wiring — mirroring this
// repo's existing Discover test conventions (stubFetch/jsonResponse from
// Discover.test.tsx / Discover.grab.test.tsx).

import { afterEach, describe, expect, it, vi } from "vitest";
import { createSignal, Show } from "solid-js";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import type {
  AdultDiscoverItem,
  AvailabilityCandidate,
  AvailabilityPreview,
  DiscoverItem,
  TitleDetail,
} from "@dto";
import {
  DetailPopup,
  type DetailTarget,
  candidateAt,
  computeDefaults,
  externalDetailURL,
  sourceLabel,
} from "./DetailPopup";

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

const candidate = (over: Partial<AvailabilityCandidate> = {}): AvailabilityCandidate => ({
  guid: "g1",
  title: "Release.Title",
  indexer: "IndexerA",
  protocol: "torrent",
  size: 1000,
  seeders: 10,
  downloadUrl: "magnet:?xt=urn:btih:abc",
  publishDate: "2024-01-01",
  score: 5,
  ...over,
});

// emptyPreview builds the full 4×4×2 grid with every cell nil — the same
// all-nil shape the real handler emits when nothing qualified anywhere.
// Tests mutate individual cells to place candidates precisely.
const emptyTier = () => ({ usenet: undefined, torrent: undefined });
const emptyRes = () => ({
  low: emptyTier(),
  medium: emptyTier(),
  high: emptyTier(),
  lossless: emptyTier(),
});
const emptyPreview = (): AvailabilityPreview => ({
  res2160: emptyRes(),
  res1080: emptyRes(),
  res720: emptyRes(),
  res480: emptyRes(),
});

const movie = (over: Partial<DiscoverItem> = {}): DiscoverItem => ({
  id: 1,
  title: "Hero Movie",
  posterPath: "/p.jpg",
  overview: "An overview.",
  releaseDate: "2024-05-01",
  voteAverage: 7.8,
  mediaType: "movie",
  ...over,
});

const adultScene = (over: Partial<AdultDiscoverItem> = {}): AdultDiscoverItem => ({
  id: "s1",
  title: "A Scene",
  studio: "Vixen",
  date: "2023-01-01",
  image: "",
  durationSeconds: 1800,
  rating: 4,
  source: "tpdb",
  slug: "evilangel-a-scene-1",
  ...over,
});

type Call = { url: string; method: string; body: unknown };
type Handler = (url: string, init?: RequestInit) => Response | Promise<Response>;
const stubFetch = (handler: Handler) => {
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

afterEach(() => vi.unstubAllGlobals());

describe("DetailPopup — availability grid derivation (pure logic)", () => {
  it("candidateAt reads the exact (resolution, tier, protocol) cell and undefined for every other cell", () => {
    const preview = emptyPreview();
    preview.res1080.high.torrent = candidate({ title: "T1" });

    expect(candidateAt(preview, 1080, "high", "torrent")?.title).toBe("T1");
    expect(candidateAt(preview, 1080, "high", "usenet")).toBeUndefined();
    expect(candidateAt(preview, 1080, "low", "torrent")).toBeUndefined();
    expect(candidateAt(preview, 720, "high", "torrent")).toBeUndefined();
  });

  it("computeDefaults picks the exact quality-prefs (maxResolution, tier) combination, preferring torrent when both protocols qualify", () => {
    const preview = emptyPreview();
    preview.res1080.high.torrent = candidate({ title: "PreferredTorrent" });
    preview.res1080.high.usenet = candidate({ title: "AlsoUsenet" });

    expect(computeDefaults(preview, { tier: "high", maxResolution: 1080 })).toEqual({
      resolution: 1080,
      tier: "high",
      protocol: "torrent",
    });
  });

  it("computeDefaults falls back to usenet at the prefs combination when only usenet qualifies there", () => {
    const preview = emptyPreview();
    preview.res1080.high.usenet = candidate({ title: "OnlyUsenet" });

    expect(computeDefaults(preview, { tier: "high", maxResolution: 1080 })).toEqual({
      resolution: 1080,
      tier: "high",
      protocol: "usenet",
    });
  });

  it("computeDefaults falls back to the first available grid combination when the prefs combination has no candidate", () => {
    const preview = emptyPreview();
    preview.res720.medium.torrent = candidate({ title: "Fallback" });

    // Prefs point at (2160, lossless), which has nothing — must fall back,
    // not stay stuck on an all-nil default.
    expect(computeDefaults(preview, { tier: "lossless", maxResolution: 2160 })).toEqual({
      resolution: 720,
      tier: "medium",
      protocol: "torrent",
    });
  });

  // Regression test for the real reported bug: a configured tier was ignored
  // whenever maxResolution was left at its default 0 ("no cap") — the
  // overwhelmingly likely case for anyone who set a tier but never touched
  // the resolution cap. The buggy code required an EXACT (maxResolution,
  // tier) match to honor the tier at all; leaving maxResolution at 0 skipped
  // the configured-tier branch entirely and fell straight to the
  // first-available-combination scan, which starts from the Low tier.
  it("computeDefaults honors the configured tier even when maxResolution is 0 (no cap) — the reported bug", () => {
    const preview = emptyPreview();
    // The old buggy fallback scan (resolution descending, tier low-first)
    // would hit this Low-tier candidate at the highest resolution FIRST.
    preview.res2160.low.torrent = candidate({ title: "WouldWinUnderTheBug" });
    // The configured tier ("high") only qualifies at a lower resolution.
    preview.res1080.high.torrent = candidate({ title: "ConfiguredTier" });

    expect(computeDefaults(preview, { tier: "high", maxResolution: 0 })).toEqual({
      resolution: 1080,
      tier: "high",
      protocol: "torrent",
    });
  });

  // maxResolution is documented elsewhere (Library.tsx's QualityPrefsSection
  // help text) as a SOFT cap: "softly prefers at-or-below-cap results,
  // falling back to whatever's available." This proves computeDefaults keeps
  // searching the configured TIER above the cap before ever abandoning the
  // tier for a resolution-only match — the old code had no such above-cap
  // extension at all, and its fallback scan (resolution descending, ANY
  // tier) would have picked the very-high-resolution wrong-tier candidate
  // below instead.
  it("computeDefaults searches above the resolution cap for the configured tier before abandoning it", () => {
    const preview = emptyPreview();
    // Above the cap AND the wrong tier — the old fallback scan's first hit.
    preview.res2160.low.torrent = candidate({ title: "WrongTierVeryHighRes" });
    // Above the cap, but the RIGHT tier, at a lower (still above-cap) res.
    preview.res1080.high.torrent = candidate({ title: "RightTierAboveCap" });

    expect(computeDefaults(preview, { tier: "high", maxResolution: 480 })).toEqual({
      resolution: 1080,
      tier: "high",
      protocol: "torrent",
    });
  });

  it("computeDefaults prefers a configured protocol over the default torrent-first pick", () => {
    const preview = emptyPreview();
    preview.res1080.high.torrent = candidate({ title: "DefaultTorrent" });
    preview.res1080.high.usenet = candidate({ title: "PreferredUsenet" });

    expect(
      computeDefaults(preview, { tier: "high", maxResolution: 1080, protocol: "usenet" }),
    ).toEqual({ resolution: 1080, tier: "high", protocol: "usenet" });
  });

  it("computeDefaults falls back to torrent-preferred when the configured protocol has no candidate at that cell", () => {
    const preview = emptyPreview();
    preview.res1080.high.torrent = candidate({ title: "OnlyTorrent" });

    expect(
      computeDefaults(preview, { tier: "high", maxResolution: 1080, protocol: "usenet" }),
    ).toEqual({ resolution: 1080, tier: "high", protocol: "torrent" });
  });

  it("computeDefaults returns undefined when nothing in the grid has a candidate anywhere", () => {
    expect(computeDefaults(emptyPreview(), { tier: "high", maxResolution: 1080 })).toBeUndefined();
  });

  it("computeDefaults works with no prefs at all (Adult's path) — goes straight to the grid scan", () => {
    const preview = emptyPreview();
    preview.res2160.lossless.torrent = candidate({ title: "BestAvailable" });

    expect(computeDefaults(preview)).toEqual({
      resolution: 2160,
      tier: "lossless",
      protocol: "torrent",
    });
  });
});

describe("DetailPopup — selector disabled-state derivation (rendered)", () => {
  it("disables a resolution/protocol option with no candidate at the CURRENT other-two-axes combination, not just any candidate anywhere", async () => {
    const preview = emptyPreview();
    // Default combo lands on (1080, high) via prefs. Torrent qualifies there;
    // usenet does not — even though usenet exists elsewhere in the grid.
    preview.res1080.high.torrent = candidate({ title: "Preferred1080HighTorrent" });
    preview.res1080.low.usenet = candidate({ title: "UsenetAtADifferentTier" });
    // 720p also qualifies at (high, torrent) — the CURRENT combo — so it must
    // be enabled; 2160p/480p have nothing at (high, torrent) anywhere.
    preview.res720.high.torrent = candidate({ title: "720HighTorrent" });

    stubFetch((url) => {
      if (url.includes("/discover/availability")) return jsonResponse(preview);
      if (url.includes("/quality-prefs"))
        return jsonResponse({ tier: "high", maxResolution: 1080 });
      throw new Error("unexpected fetch: " + url);
    });

    const target: DetailTarget = { mode: "movies", item: movie() };
    render(() => <DetailPopup target={target} onClose={() => {}} />);

    expect(await screen.findByRole("button", { name: "Grab" })).not.toBeDisabled();

    expect(screen.getByRole("button", { name: "720p" })).not.toBeDisabled();
    expect(screen.getByRole("button", { name: "480p" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "2160p" })).toBeDisabled();

    // Usenet exists in the grid (at a different tier), but not at the
    // CURRENT (1080, high) combination — must render disabled.
    expect(screen.getByRole("button", { name: "Usenet" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Torrent" })).not.toBeDisabled();
  });

  it("re-derives every selector's disabled state on a resolution switch, without any additional fetch", async () => {
    const preview = emptyPreview();
    preview.res1080.high.torrent = candidate({ title: "A" });
    preview.res720.high.torrent = candidate({ title: "B" });
    // Only reachable via a DIFFERENT tier/protocol than the current selection.
    preview.res720.medium.usenet = candidate({ title: "C" });

    const calls = stubFetch((url) => {
      if (url.includes("/discover/availability")) return jsonResponse(preview);
      if (url.includes("/quality-prefs"))
        return jsonResponse({ tier: "high", maxResolution: 1080 });
      throw new Error("unexpected fetch: " + url);
    });

    const target: DetailTarget = { mode: "movies", item: movie() };
    render(() => <DetailPopup target={target} onClose={() => {}} />);
    await screen.findByRole("button", { name: "Grab" });

    const availabilityCalls = () =>
      calls.filter((c) => c.url.includes("/discover/availability")).length;
    expect(availabilityCalls()).toBe(1);

    fireEvent.click(screen.getByRole("button", { name: "720p" }));

    // Switching a selector never refetches — only re-derives disabled state
    // against the already-fetched grid.
    expect(availabilityCalls()).toBe(1);

    // At the new (720, high, torrent) combination: "medium" and "usenet"
    // only have a candidate at a DIFFERENT combination (720/medium/usenet),
    // not this one — both must now be disabled.
    expect(screen.getByRole("button", { name: "Medium" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Usenet" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "High" })).not.toBeDisabled();
    expect(screen.getByRole("button", { name: "Torrent" })).not.toBeDisabled();
  });

  it("Series gates the availability fetch behind season/episode — no call until the picker is submitted", async () => {
    const preview = emptyPreview();
    preview.res1080.high.torrent = candidate();

    const calls = stubFetch((url) => {
      if (url.includes("/discover/availability")) return jsonResponse(preview);
      if (url.includes("/quality-prefs"))
        return jsonResponse({ tier: "high", maxResolution: 1080 });
      throw new Error("unexpected fetch: " + url);
    });

    const target: DetailTarget = {
      mode: "series",
      item: movie({ id: 5, title: "A Series", mediaType: "tv" }),
    };
    render(() => <DetailPopup target={target} onClose={() => {}} />);

    expect(screen.getByLabelText("Season")).toBeInTheDocument();
    expect(calls.filter((c) => c.url.includes("/discover/availability"))).toHaveLength(0);

    fireEvent.input(screen.getByLabelText("Season"), { target: { value: "2" } });
    fireEvent.input(screen.getByLabelText("Episode"), { target: { value: "4" } });
    fireEvent.click(screen.getByText("Go"));

    expect(await screen.findByRole("button", { name: "Grab" })).toBeInTheDocument();
    const availCall = calls.find((c) => c.url.includes("/discover/availability"));
    expect(availCall?.url).toContain("season=2");
    expect(availCall?.url).toContain("episode=4");
  });

  it("Adult now also fetches and honors quality-prefs, same as Movies/Series", async () => {
    const preview = emptyPreview();
    // The old fallback-scan-only result the bug would have produced.
    preview.res2160.low.torrent = candidate({ title: "WouldWinUnderTheOldPath" });
    // The configured tier, which Adult must now honor.
    preview.res1080.high.torrent = candidate({ title: "ConfiguredTierForAdult" });

    const calls = stubFetch((url) => {
      if (url.includes("/discover/availability")) return jsonResponse(preview);
      if (url.includes("/quality-prefs"))
        return jsonResponse({ tier: "high", maxResolution: 0, protocol: "" });
      throw new Error("unexpected fetch: " + url);
    });

    const target: DetailTarget = { mode: "adult", item: adultScene() };
    render(() => <DetailPopup target={target} onClose={() => {}} />);

    expect(await screen.findByRole("button", { name: "Grab" })).not.toBeDisabled();
    expect(screen.getByRole("button", { name: "1080p" })).not.toBeDisabled();
    expect(screen.getByRole("button", { name: "High" })).not.toBeDisabled();

    expect(calls.some((c) => c.url.includes("/api/modes/adult/quality-prefs"))).toBe(true);
    const availCall = calls.find((c) => c.url.includes("/discover/availability"));
    expect(availCall?.url).toContain("studio=Vixen");
    expect(availCall?.url).toContain("durationSeconds=1800");
    expect(availCall?.url).not.toContain("tmdbId");
  });
});

describe("DetailPopup — external database link (poster + \"More on …\")", () => {
  it("externalDetailURL builds the TMDB movie URL from the item's TMDB id", () => {
    const target: DetailTarget = { mode: "movies", item: movie({ id: 42 }) };
    expect(externalDetailURL(target)).toBe("https://www.themoviedb.org/movie/42");
    expect(sourceLabel(target)).toBe("TMDB");
  });

  it("externalDetailURL builds the TMDB tv URL for Series", () => {
    const target: DetailTarget = {
      mode: "series",
      item: movie({ id: 7, mediaType: "tv" }),
    };
    expect(externalDetailURL(target)).toBe("https://www.themoviedb.org/tv/7");
  });

  // TPDB is slug-path (theporndb.net/scenes/{slug}), NOT id-path — confirmed
  // against a real example URL
  // (theporndb.net/scenes/evilangel-ivy-ireland-dp-dvp-threesome-1). An
  // earlier version of this function used the scene's opaque `id` here,
  // which is a real bug this test guards against regressing.
  it("externalDetailURL maps each Adult source to its own site — TPDB by slug, stash-box by id", () => {
    expect(
      externalDetailURL({
        mode: "adult",
        item: adultScene({ id: "s1", slug: "evilangel-ivy-ireland-dp-dvp-threesome-1", source: "tpdb" }),
      }),
    ).toBe("https://theporndb.net/scenes/evilangel-ivy-ireland-dp-dvp-threesome-1");
    expect(
      externalDetailURL({ mode: "adult", item: adultScene({ id: "s2", source: "stashdb" }) }),
    ).toBe("https://stashdb.org/scenes/s2");
    expect(
      externalDetailURL({ mode: "adult", item: adultScene({ id: "s3", source: "fansdb" }) }),
    ).toBe("https://fansdb.cc/scenes/s3");
    expect(sourceLabel({ mode: "adult", item: adultScene({ source: "stashdb" }) })).toBe(
      "StashDB",
    );
  });

  it("externalDetailURL returns undefined for a TPDB scene with no slug (older/edge-case scene)", () => {
    expect(
      externalDetailURL({ mode: "adult", item: adultScene({ source: "tpdb", slug: "" }) }),
    ).toBeUndefined();
  });

  it("externalDetailURL returns undefined for an unrecognized Adult source", () => {
    expect(
      externalDetailURL({ mode: "adult", item: adultScene({ source: "unknown-source" }) }),
    ).toBeUndefined();
  });

  it("renders the poster as a link and a \"More on TMDB\" link below the description", async () => {
    const preview = emptyPreview();
    preview.res1080.high.torrent = candidate();
    stubFetch((url) => {
      if (url.includes("/discover/availability")) return jsonResponse(preview);
      if (url.includes("/quality-prefs"))
        return jsonResponse({ tier: "high", maxResolution: 1080, protocol: "" });
      throw new Error("unexpected fetch: " + url);
    });

    const target: DetailTarget = { mode: "movies", item: movie({ id: 42, title: "Hero Movie" }) };
    render(() => <DetailPopup target={target} onClose={() => {}} />);
    await screen.findByRole("button", { name: "Grab" });

    const moreLink = screen.getByText(/More on TMDB/);
    expect(moreLink.closest("a")).toHaveAttribute(
      "href",
      "https://www.themoviedb.org/movie/42",
    );
    const posterLink = screen.getByAltText("Hero Movie").closest("a");
    expect(posterLink).toHaveAttribute("href", "https://www.themoviedb.org/movie/42");
  });
});

describe("DetailPopup — Watch Trailer link", () => {
  const stubWithTrailer = (trailerUrl: string) =>
    stubFetch((url) => {
      if (url.includes("/discover/trailer")) return jsonResponse({ url: trailerUrl });
      if (url.includes("/discover/availability")) return jsonResponse(emptyPreview());
      if (url.includes("/quality-prefs"))
        return jsonResponse({ tier: "high", maxResolution: 1080, protocol: "" });
      throw new Error("unexpected fetch: " + url);
    });

  it("renders a Watch Trailer link for Movies when TMDB has one on file", async () => {
    const calls = stubWithTrailer("https://www.youtube.com/watch?v=abc123");
    const target: DetailTarget = { mode: "movies", item: movie({ id: 42 }) };
    render(() => <DetailPopup target={target} onClose={() => {}} />);

    const trailerLink = await screen.findByText("Watch Trailer →");
    expect(trailerLink.closest("a")).toHaveAttribute(
      "href",
      "https://www.youtube.com/watch?v=abc123",
    );
    const trailerCall = calls.find((c) => c.url.includes("/discover/trailer"));
    expect(trailerCall?.url).toBe("/api/modes/movies/discover/trailer?tmdbId=42");
  });

  it("omits the Watch Trailer link when TMDB has none on file", async () => {
    stubWithTrailer("");
    const target: DetailTarget = { mode: "movies", item: movie({ id: 42 }) };
    render(() => <DetailPopup target={target} onClose={() => {}} />);

    await screen.findByText(/More on TMDB/);
    expect(screen.queryByText("Watch Trailer →")).not.toBeInTheDocument();
  });

  it("never fetches a trailer for Adult — Adult has no TMDB id to resolve one from", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/discover/availability")) return jsonResponse(emptyPreview());
      if (url.includes("/quality-prefs"))
        return jsonResponse({ tier: "high", maxResolution: 0, protocol: "" });
      throw new Error("unexpected fetch: " + url);
    });

    const target: DetailTarget = { mode: "adult", item: adultScene() };
    render(() => <DetailPopup target={target} onClose={() => {}} />);
    await screen.findByRole("button", { name: "Grab" });

    expect(calls.some((c) => c.url.includes("/discover/trailer"))).toBe(false);
    expect(screen.queryByText("Watch Trailer →")).not.toBeInTheDocument();
  });

  it("fetches the Series trailer via the series mode path", async () => {
    const calls = stubWithTrailer("https://www.youtube.com/watch?v=series1");
    const target: DetailTarget = {
      mode: "series",
      item: movie({ id: 7, mediaType: "tv" }),
    };
    render(() => <DetailPopup target={target} onClose={() => {}} />);

    fireEvent.input(screen.getByLabelText("Season"), { target: { value: "1" } });
    fireEvent.click(screen.getByText("Go"));

    await screen.findByText("Watch Trailer →");
    const trailerCall = calls.find((c) => c.url.includes("/discover/trailer"));
    expect(trailerCall?.url).toBe("/api/modes/series/discover/trailer?tmdbId=7");
  });
});

describe("DetailPopup — Grab wiring (mirrors GrabDialog.pickManual's call shape)", () => {
  it("resolves the root folder, then calls manualGrab with the selected candidate's fields", async () => {
    const preview = emptyPreview();
    preview.res1080.high.torrent = candidate({
      title: "Hero.Movie.1080p",
      indexer: "IndexerA",
      protocol: "torrent",
      downloadUrl: "magnet:?xt=urn:btih:abc",
    });

    const calls = stubFetch((url) => {
      if (url.includes("/discover/availability")) return jsonResponse(preview);
      if (url.includes("/quality-prefs"))
        return jsonResponse({ tier: "high", maxResolution: 1080 });
      if (url.includes("/library/root-folder")) return jsonResponse({ path: "/movies" });
      if (url.includes("/search/grab"))
        return jsonResponse({ id: 9, mode: "movies", title: "Hero Movie", status: "queued" });
      throw new Error("unexpected fetch: " + url);
    });

    const target: DetailTarget = { mode: "movies", item: movie({ id: 42, title: "Hero Movie" }) };
    render(() => <DetailPopup target={target} onClose={() => {}} />);

    fireEvent.click(await screen.findByRole("button", { name: "Grab" }));

    expect(await screen.findByText(/Grabbed/)).toBeInTheDocument();
    const grabCall = calls.find((c) => c.url.includes("/search/grab"));
    expect(grabCall?.body).toMatchObject({
      title: "Hero Movie",
      tmdbId: 42,
      indexer: "IndexerA",
      protocol: "torrent",
      downloadUrl: "magnet:?xt=urn:btih:abc",
      rootFolderPath: "/movies",
    });
  });
});

describe("DetailPopup — Adult tags/performers", () => {
  const stubAdultFetches = () =>
    stubFetch((url) => {
      if (url.includes("/discover/availability")) return jsonResponse(emptyPreview());
      if (url.includes("/quality-prefs"))
        return jsonResponse({ tier: "high", maxResolution: 0, protocol: "" });
      throw new Error("unexpected fetch: " + url);
    });

  it("renders genres and performers when the item has them", async () => {
    stubAdultFetches();
    const target: DetailTarget = {
      mode: "adult",
      item: adultScene({ genres: ["Anal", "Blonde"], performers: ["Jane Doe", "John Roe"] }),
    };
    render(() => <DetailPopup target={target} onClose={() => {}} />);

    expect(await screen.findByText("Anal")).toBeInTheDocument();
    expect(screen.getByText("Blonde")).toBeInTheDocument();
    expect(screen.getByText("Jane Doe, John Roe")).toBeInTheDocument();
  });

  it("renders neither section when the item has no genres/performers", async () => {
    stubAdultFetches();
    const target: DetailTarget = { mode: "adult", item: adultScene() };
    render(() => <DetailPopup target={target} onClose={() => {}} />);

    expect(await screen.findByRole("button", { name: "Grab" })).toBeInTheDocument();
    expect(screen.queryByText(/Performers:/)).not.toBeInTheDocument();
  });
});

// titleDetail builds a full F1 detail bundle (every section populated); pass an
// override to blank fields for the graceful-empty path.
const titleDetail = (over: Partial<TitleDetail> = {}): TitleDetail => ({
  status: "Released",
  originalLanguage: "en",
  productionCountry: "United States",
  productionCountryCode: "US",
  collectionName: "Hero Collection",
  collectionId: 123,
  networks: [],
  studios: ["Studio X"],
  runtime: 125,
  releaseDates: [{ type: "Theatrical", date: "2024-05-01" }],
  genres: ["Action"],
  keywords: ["heist", "spy"],
  cast: [{ name: "Actor One", character: "Hero", profilePath: "/a1.jpg" }],
  crew: [{ name: "Dir One", job: "Director", profilePath: "" }],
  watchProviders: [{ name: "Netflix", logoPath: "/nf.jpg" }],
  recommendations: [
    {
      id: 99,
      title: "Recommended B",
      posterPath: "",
      overview: "",
      releaseDate: "",
      voteAverage: 0,
      mediaType: "movie",
    },
  ],
  ...over,
});

const emptyDetail = (): TitleDetail =>
  titleDetail({
    status: "",
    originalLanguage: "",
    productionCountry: "",
    productionCountryCode: "",
    collectionName: "",
    collectionId: 0,
    networks: [],
    studios: [],
    runtime: 0,
    releaseDates: [],
    genres: [],
    keywords: [],
    cast: [],
    crew: [],
    watchProviders: [],
    recommendations: [],
  });

describe("DetailPopup — F1 rich detail sections (Movies/Series)", () => {
  const stubWithDetail = (
    detail: TitleDetail,
    preview: AvailabilityPreview = emptyPreview(),
  ) =>
    stubFetch((url) => {
      if (url.includes("/discover/detail")) return jsonResponse(detail);
      if (url.includes("/discover/availability")) return jsonResponse(preview);
      if (url.includes("/discover/trailer")) return jsonResponse({ url: "" });
      if (url.includes("/quality-prefs"))
        return jsonResponse({ tier: "high", maxResolution: 1080, protocol: "" });
      throw new Error("unexpected fetch: " + url);
    });

  it("renders every populated section (collection/keywords/metadata/crew/cast/providers+JustWatch/recommendations)", async () => {
    stubWithDetail(titleDetail());
    const target: DetailTarget = { mode: "movies", item: movie({ id: 42 }) };
    render(() => <DetailPopup target={target} onClose={() => {}} />);

    // Collection banner + keyword chips + metadata sidebar.
    expect(await screen.findByText("Hero Collection")).toBeInTheDocument();
    expect(screen.getByText("heist")).toBeInTheDocument();
    expect(screen.getByText("Released")).toBeInTheDocument();
    expect(screen.getByText("2h 5m")).toBeInTheDocument();
    // Production Country renders with a leading flag emoji, so match loosely.
    expect(screen.getByText(/United States/)).toBeInTheDocument();
    expect(screen.getByText("Studio X")).toBeInTheDocument();
    expect(screen.getByText("Theatrical")).toBeInTheDocument();

    // Crew ABOVE cast; both render their people.
    expect(screen.getByText("Dir One")).toBeInTheDocument();
    expect(screen.getByText("Director")).toBeInTheDocument();
    expect(screen.getByText("Actor One")).toBeInTheDocument();
    expect(screen.getByText("Hero")).toBeInTheDocument();

    // Providers row + the hard-required JustWatch attribution.
    expect(screen.getByAltText("Netflix")).toBeInTheDocument();
    expect(screen.getByText("Powered by JustWatch")).toBeInTheDocument();

    // Recommendation rail (empty posterPath → the title shows in both the
    // TextPoster fallback and the card title, so match all).
    expect(screen.getAllByText("Recommended B").length).toBeGreaterThan(0);

    // Revenue/Budget are explicitly excluded.
    expect(screen.queryByText(/Revenue/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/Budget/i)).not.toBeInTheDocument();
  });

  it("routes every detail image (headshot, provider logo) through the image proxy — never a direct TMDB host", async () => {
    stubWithDetail(titleDetail());
    const target: DetailTarget = { mode: "movies", item: movie({ id: 42 }) };
    render(() => <DetailPopup target={target} onClose={() => {}} />);

    const headshot = await screen.findByAltText("Actor One");
    const logo = screen.getByAltText("Netflix");
    for (const img of [headshot, logo]) {
      const src = img.getAttribute("src") ?? "";
      expect(src).toContain("/api/images/proxy");
      expect(src).not.toContain("//image.tmdb.org/");
    }
  });

  it("gracefully renders with every section empty (backend soft-fail per sub-call)", async () => {
    stubWithDetail(emptyDetail());
    const target: DetailTarget = { mode: "movies", item: movie({ id: 42 }) };
    render(() => <DetailPopup target={target} onClose={() => {}} />);

    // The popup itself still works (Grab present); no empty section headings.
    expect(await screen.findByRole("button", { name: "Grab" })).toBeInTheDocument();
    expect(screen.queryByText("Cast")).not.toBeInTheDocument();
    expect(screen.queryByText("Crew")).not.toBeInTheDocument();
    expect(screen.queryByText("Currently Streaming On")).not.toBeInTheDocument();
    expect(screen.queryByText("Powered by JustWatch")).not.toBeInTheDocument();
    expect(screen.queryByText("More like this")).not.toBeInTheDocument();
    expect(screen.queryByText("Hero Collection")).not.toBeInTheDocument();
  });

  it("soft-fails the whole detail fetch to no sections, popup still usable", async () => {
    stubFetch((url) => {
      if (url.includes("/discover/detail"))
        return new Response("boom", { status: 500 });
      if (url.includes("/discover/availability"))
        return jsonResponse(emptyPreview());
      if (url.includes("/discover/trailer")) return jsonResponse({ url: "" });
      if (url.includes("/quality-prefs"))
        return jsonResponse({ tier: "high", maxResolution: 1080, protocol: "" });
      throw new Error("unexpected fetch: " + url);
    });
    const target: DetailTarget = { mode: "movies", item: movie({ id: 42 }) };
    render(() => <DetailPopup target={target} onClose={() => {}} />);

    expect(await screen.findByRole("button", { name: "Grab" })).toBeInTheDocument();
    expect(screen.queryByText("More like this")).not.toBeInTheDocument();
  });

  it("never fetches /discover/detail for Adult (no TMDB id)", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/discover/availability"))
        return jsonResponse(emptyPreview());
      if (url.includes("/quality-prefs"))
        return jsonResponse({ tier: "high", maxResolution: 0, protocol: "" });
      throw new Error("unexpected fetch: " + url);
    });
    const target: DetailTarget = { mode: "adult", item: adultScene() };
    render(() => <DetailPopup target={target} onClose={() => {}} />);
    await screen.findByRole("button", { name: "Grab" });

    expect(calls.some((c) => c.url.includes("/discover/detail"))).toBe(false);
  });

  // The advisor-flagged risk: a recommendation click swaps detailTarget from one
  // truthy target to another. Mainstream renders the popup <Show keyed>, so this
  // test replicates that harness and clicks a recommendation AFTER grabbing on
  // the first title — proving the popup both re-targets (fetches the new tmdbId)
  // AND resets its component-local grab state (the keyed remount), not just that
  // the new title's data appears.
  it("re-targets to a clicked recommendation and resets grab state (keyed remount)", async () => {
    const previewA = emptyPreview();
    previewA.res1080.high.torrent = candidate({ title: "A.1080p" });

    const calls = stubFetch((url) => {
      const tmdbId = new URL(url, "http://x").searchParams.get("tmdbId");
      if (url.includes("/discover/detail")) {
        // Title A carries a recommendation to B; B carries none.
        return jsonResponse(tmdbId === "99" ? emptyDetail() : titleDetail());
      }
      if (url.includes("/discover/availability")) return jsonResponse(previewA);
      if (url.includes("/discover/trailer")) return jsonResponse({ url: "" });
      if (url.includes("/quality-prefs"))
        return jsonResponse({ tier: "high", maxResolution: 1080, protocol: "" });
      if (url.includes("/library/root-folder"))
        return jsonResponse({ path: "/movies" });
      if (url.includes("/search/grab"))
        return jsonResponse({ id: 9, mode: "movies", title: "Hero Movie", status: "queued" });
      throw new Error("unexpected fetch: " + url);
    });

    const Harness = () => {
      const [target, setTarget] = createSignal<DetailTarget | null>({
        mode: "movies",
        item: movie({ id: 42, title: "Hero Movie" }),
      });
      return (
        <Show when={target()} keyed>
          {(t) => (
            <DetailPopup
              target={t}
              onClose={() => setTarget(null)}
              onSelectRecommendation={setTarget}
            />
          )}
        </Show>
      );
    };
    render(() => <Harness />);

    // Grab on title A → "Grabbed" appears, grab state is now set. (The rec rail
    // adds its own PosterCard "Grab" buttons; the popup's primary Grab is the
    // first in DOM order, rendered above the detail section.)
    const grabButtons = await screen.findAllByRole("button", { name: "Grab" });
    fireEvent.click(grabButtons[0]!);
    expect(await screen.findByText(/Grabbed/)).toBeInTheDocument();

    // Click the recommendation card body (its title) → re-target to B (id 99).
    fireEvent.click(screen.getAllByText("Recommended B")[0]!);

    // The popup remounts on the new title: a fresh Grab button, and the prior
    // title's "Grabbed" state is gone (state reset, not carried over).
    expect(await screen.findByRole("button", { name: "Grab" })).toBeInTheDocument();
    expect(screen.queryByText(/Grabbed/)).not.toBeInTheDocument();

    // Proof it actually re-targeted: a detail/availability fetch fired for id 99.
    expect(
      calls.some(
        (c) => c.url.includes("tmdbId=99") && c.url.includes("/discover/availability"),
      ),
    ).toBe(true);
  });
});
