// SearchTakeover — the suite that supersedes MoveModePanel.test.tsx
// (.omc/plans/autopilot-impl.md §8.1). Five of that file's seven tests are
// migrated here; the sixth (a half-filled season/episode pair raising an inline
// error) is DELETED as structurally unreachable — SeasonEpisodePicker's grid
// emits a complete pick or nothing, never half a pair — and the seventh (the
// season-0 case) is REWRITTEN against the two-step grid flow rather than the
// deleted number inputs.
//
// WHAT THIS FILE IS FOR, beyond the migration. SearchTakeover's own header
// names five things a future session must not simplify away; the four that are
// observable from a test have a dedicated guard here:
//
//   rule 1 (D-1) — `episode === 0` maps to a SHOW-LEVEL commit with BOTH slot
//     fields omitted, because both endpoints 400 on `episodeNumber < 1`. Guarded
//     twice, once through the whole-season tile and once through the degraded
//     free-text fallback. Season 0 is NOT collapsed by that rule, which is what
//     the rewritten season-0 test proves.
//   rule 2 (D-5) — "Use show-level match only" is not a redundant duplicate of
//     the whole-season tile; it has its own omits-both-keys guard.
//   rule 4 — the results block is wrapped in `<Show when={!results.error}>`
//     because a Solid resource re-throws on read once its fetcher has errored.
//     Guarded by forcing a search error and asserting the takeover still
//     renders its own chrome.
//   rule 5 — the free-text-fallback notice is UNCONDITIONAL in Series step 2.
//     Its presence is asserted; per that rule's closing sentence, its ABSENCE
//     is deliberately never asserted anywhere in this file.
//
// Plus the structural guarantee the takeover exists to deliver: it is NOT a
// modal (no role="dialog", no `fixed inset-0`), asserted the way this repo's
// Discover.test.tsx "row editor carries NO per-entity actions" test asserts
// absence — guard the guard first, so nothing can pass vacuously.
//
// PAYLOAD ASSERTIONS USE `not.toHaveProperty`, NEVER `toMatchObject`. A stray
// `episodeNumber: 0` is exactly the regression these tests exist to catch, and
// `toMatchObject` passes straight through it.

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@solidjs/testing-library";
import type { AdultSceneCandidate, DiscoverItem } from "@dto";
import { moveProposalMode } from "../api/rename";
import { SourcePreviewDisclosure } from "../components/SourcePreview";
import { SearchTakeover, type TakeoverPick } from "./SearchTakeover";

afterEach(() => vi.unstubAllGlobals());

const jsonResponse = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });

// Every fixture carries real art. TextPoster renders the same label the card
// footer does, so an art-less card puts its title in the DOM TWICE and every
// `getByText(title)` in this file would throw "found multiple elements". The
// two tests that deliberately exercise the art-less path scope their queries to
// one tile instead.
const catalogItem = (over: Partial<DiscoverItem> = {}): DiscoverItem => ({
  id: 42,
  title: "A Show",
  posterPath: "/poster.jpg",
  overview: "",
  releaseDate: "2020-01-01",
  voteAverage: 0,
  mediaType: "tv",
  ...over,
});

const adultCandidate = (
  over: Partial<AdultSceneCandidate> = {},
): AdultSceneCandidate => ({
  box: "stashdb",
  sceneId: "abc123",
  title: "A Scene Title",
  studio: "A Studio",
  date: "2026-01-01",
  imageUrl: "https://cdn.example.com/scene.jpg",
  ...over,
});

// Season 0 is reserved for the season-0 test. Every other season-grid test uses
// season 4, so an omitted `seasonNumber` there is unambiguously "the D-1 rule
// fired" rather than "season 0 got dropped by a truthiness check".
const specialsSeason = {
  seasonNumber: 0,
  name: "Specials",
  airDate: "2019-01-01",
  episodeCount: 3,
  posterPath: "/s0.jpg",
  episodes: [
    {
      episodeNumber: 3,
      name: "Special Three",
      airDate: "2019-03-01",
      runtime: 22,
      stillPath: "/e3.jpg",
    },
  ],
};

const season4 = {
  seasonNumber: 4,
  name: "Season 4",
  airDate: "2023-01-01",
  episodeCount: 8,
  posterPath: "/s4.jpg",
  episodes: [
    {
      episodeNumber: 7,
      name: "Seven",
      airDate: "2023-03-01",
      runtime: 45,
      stillPath: "/e7.jpg",
    },
  ],
};

const SEARCH_URL = "/api/modes/series/tmdb-search";
const SEASONS_URL = "/api/modes/series/discover/detail";

// seriesFetch is the shared stub for the Series two-step flow: one tmdb-search
// hit, one ?sections=seasons hit. `seasons` is what the picker's self-fetch
// resolves to — pass [] to force its degraded free-text fallback.
const seriesFetch = (seasons: unknown[]) =>
  vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.startsWith(SEARCH_URL)) return jsonResponse([catalogItem()]);
    if (url.startsWith(SEASONS_URL)) return jsonResponse({ seasons });
    throw new Error("unexpected fetch: " + url);
  });

const commitSpy = () => vi.fn(async (_pick: TakeoverPick) => {});

// pickShow drives step 1: search, then click the candidate tile. The wait is on
// the tile's aria-label, not on the title text, because the title also appears
// in the card footer.
const pickShow = async (title = "A Show") => {
  fireEvent.click(screen.getByText("Search"));
  fireEvent.click(await screen.findByLabelText(`Use ${title}`));
};

describe("SearchTakeover — search 403 is section-agnostic", () => {
  it("a Discover-locked search (non-Adult section) renders the shared, non-hardcoded copy", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.startsWith("/api/modes/movies/tmdb-search")) {
        return jsonResponse(
          { error: "section locked", code: "section_locked", section: "discover" },
          403,
        );
      }
      throw new Error("unexpected fetch: " + url);
    });
    vi.stubGlobal("fetch", fetchMock);

    render(() => (
      <SearchTakeover
        heading="Move “Some Movie” to another section"
        searchMode="movies"
        initialQuery="Some Movie"
        autoSearch={false}
        onCommit={commitSpy()}
        onDone={vi.fn()}
        onCancel={vi.fn()}
      />
    ));

    fireEvent.click(screen.getByText("Search"));

    expect(
      await screen.findByText("Discover is PIN-locked — unlock it to search"),
    ).toBeInTheDocument();
    // Never the fixed Adult-only commit copy on a search failure.
    expect(
      screen.queryByText("Adult is PIN-locked — unlock to continue"),
    ).toBeNull();
  });

  it("an Adult search 403 uses the same shared handler (renders 'Adult content')", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.startsWith("/api/modes/adult/scene-search")) {
        return jsonResponse(
          {
            error: "section locked",
            code: "section_locked",
            section: "adult-content",
          },
          403,
        );
      }
      throw new Error("unexpected fetch: " + url);
    });
    vi.stubGlobal("fetch", fetchMock);

    render(() => (
      <SearchTakeover
        heading="Move “Some Scene” to another section"
        searchMode="adult"
        initialQuery="Some Scene"
        autoSearch={false}
        onCommit={commitSpy()}
        onDone={vi.fn()}
        onCancel={vi.fn()}
      />
    ));

    fireEvent.click(screen.getByText("Search"));

    expect(
      await screen.findByText(
        "Adult content is PIN-locked — unlock it to search",
      ),
    ).toBeInTheDocument();
  });

  it("falls back to neutral copy when the server omits a section name", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse(
        { error: "section locked", code: "section_locked", section: "" },
        403,
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    render(() => (
      <SearchTakeover
        heading="Move “Some Movie” to another section"
        searchMode="movies"
        initialQuery="Some Movie"
        autoSearch={false}
        onCommit={commitSpy()}
        onDone={vi.fn()}
        onCancel={vi.fn()}
      />
    ));

    fireEvent.click(screen.getByText("Search"));

    expect(
      await screen.findByText("This section is PIN-locked"),
    ).toBeInTheDocument();
  });
});

describe("SearchTakeover — commit 403 is a SEPARATE branch from search 403", () => {
  it("a successful Adult search followed by a locked commit renders the commit-specific copy, keeps the takeover open, and never calls onDone", async () => {
    const candidate = adultCandidate();
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.startsWith("/api/modes/adult/scene-search")) {
        return jsonResponse({ items: [candidate] });
      }
      if (url === "/api/proposals/1/move-mode") {
        return jsonResponse(
          {
            error: "section locked",
            code: "section_locked",
            section: "adult-content",
          },
          403,
        );
      }
      throw new Error("unexpected fetch: " + url);
    });
    vi.stubGlobal("fetch", fetchMock);
    const onDone = vi.fn();

    // A REAL SectionLockedError, thrown by the real client through the real
    // move-mode adapter — not a hand-constructed one. The classification under
    // test is `e instanceof SectionLockedError && e.section === "adult-content"`,
    // which a fake error could satisfy without the client actually producing it.
    render(() => (
      <SearchTakeover
        heading="Move “Some File” to another section"
        searchMode="adult"
        initialQuery="Some File"
        autoSearch={false}
        onCommit={(pick) =>
          moveProposalMode(1, {
            targetMode: "adult",
            title: pick.title,
            box: pick.kind === "adult" ? pick.box : undefined,
            sceneId: pick.kind === "adult" ? pick.sceneId : undefined,
          })
        }
        onDone={onDone}
        onCancel={vi.fn()}
      />
    ));

    fireEvent.click(screen.getByText("Search"));
    fireEvent.click(await screen.findByLabelText("Use A Scene Title"));

    expect(
      await screen.findByText("Adult is PIN-locked — unlock to continue"),
    ).toBeInTheDocument();
    // Distinct from the search-403 copy — the search succeeded here.
    expect(screen.queryByText(/unlock it to search/)).toBeNull();
    // Nothing was written: onDone must never fire.
    expect(onDone).not.toHaveBeenCalled();
    // The takeover stays mounted with the candidate still visible and
    // clickable — unlock-and-retry is one click.
    const tile = screen.getByLabelText("Use A Scene Title");
    expect(tile).toBeInTheDocument();
    expect(tile).not.toBeDisabled();
  });
});

describe("SearchTakeover — season/episode `!= null` semantics (D-1)", () => {
  it("season 0 (Specials) survives the SeasonEpisodePicker swap and ships a literal 0 paired with a real episode", async () => {
    const fetchMock = seriesFetch([specialsSeason]);
    vi.stubGlobal("fetch", fetchMock);
    const onCommit = commitSpy();
    const onDone = vi.fn();

    render(() => (
      <SearchTakeover
        heading="Re-pick “Wrong.Match.Show”"
        searchMode="series"
        initialQuery="A Show"
        autoSearch={false}
        onCommit={onCommit}
        onDone={onDone}
        onCancel={vi.fn()}
      />
    ));

    await pickShow();

    // Step 2 mounted and self-fetched its season block.
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some(([u]) =>
          String(u).startsWith(SEASONS_URL),
        ),
      ).toBe(true),
    );
    const seasonsCall = fetchMock.mock.calls.find(([u]) =>
      String(u).startsWith(SEASONS_URL),
    )!;
    expect(String(seasonsCall[0])).toContain("sections=seasons");

    // Drill: Specials -> E3.
    fireEvent.click((await screen.findByText("Specials")).closest("button")!);
    fireEvent.click(
      (await screen.findByText("E3 · Special Three")).closest("button")!,
    );

    await waitFor(() => expect(onCommit).toHaveBeenCalledTimes(1));
    const pick = onCommit.mock.calls[0]![0];
    // BOTH present, and the season is a literal 0 — not omitted, not dropped by
    // a truthiness check. This is the one assertion proving `!= null` semantics
    // survived the picker swap.
    expect(pick).toHaveProperty("seasonNumber", 0);
    expect(pick).toHaveProperty("episodeNumber", 3);
    expect(pick).toHaveProperty("tmdbId", 42);
    await waitFor(() => expect(onDone).toHaveBeenCalledTimes(1));
  });

  it("the whole-season tile omits BOTH slot keys (never ships episodeNumber: 0)", async () => {
    // Season 4, deliberately not season 0: an omitted `seasonNumber` here can
    // only mean the D-1 mapping fired, never "season 0 was dropped".
    const fetchMock = seriesFetch([season4]);
    vi.stubGlobal("fetch", fetchMock);
    const onCommit = commitSpy();

    render(() => (
      <SearchTakeover
        heading="Re-pick “Wrong.Match.Show”"
        searchMode="series"
        initialQuery="A Show"
        autoSearch={false}
        onCommit={onCommit}
        onDone={vi.fn()}
        onCancel={vi.fn()}
      />
    ));

    await pickShow();
    fireEvent.click((await screen.findByText("Season 4")).closest("button")!);
    fireEvent.click(
      (await screen.findByText("Whole season")).closest("button")!,
    );

    await waitFor(() => expect(onCommit).toHaveBeenCalledTimes(1));
    const pick = onCommit.mock.calls[0]![0];
    expect(pick).toHaveProperty("tmdbId", 42);
    // `not.toHaveProperty`, not `toMatchObject`: a stray `episodeNumber: 0` is
    // precisely the regression this guards, and both commit endpoints reject it
    // with a 400 (proposals.go / proposals_movemode.go validate `< 1`).
    expect(pick).not.toHaveProperty("seasonNumber");
    expect(pick).not.toHaveProperty("episodeNumber");
  });

  it("the degraded free-text fallback maps a blank Episode through the same D-1 rule, and warns that the typed season was not applied", async () => {
    // Zero seasons -> SeasonEpisodePicker's state 4, the free-text S/E inputs.
    const fetchMock = seriesFetch([]);
    vi.stubGlobal("fetch", fetchMock);
    const onCommit = commitSpy();

    render(() => (
      <SearchTakeover
        heading="Re-pick “Wrong.Match.Show”"
        searchMode="series"
        initialQuery="A Show"
        autoSearch={false}
        onCommit={onCommit}
        onDone={vi.fn()}
        onCancel={vi.fn()}
      />
    ));

    await pickShow();

    // The notice is UNCONDITIONAL in step 2 (SearchTakeover rule 5), so this
    // asserts presence only — never absence. A plain full-sentence string match
    // would FAIL here even against correct code: a real <strong> splits the
    // sentence, and getNodeText joins only DIRECT text-node children, so the
    // element's matchable text has "and" missing from the middle.
    expect(screen.getByText(/Enter a season/)).toBeInTheDocument();

    fireEvent.input(await screen.findByLabelText("Season"), {
      target: { value: "2" },
    });
    // Episode deliberately left blank -> FreeTextPicker coerces it to 0.
    fireEvent.click(screen.getByText("Go"));

    await waitFor(() => expect(onCommit).toHaveBeenCalledTimes(1));
    const pick = onCommit.mock.calls[0]![0];
    expect(pick).toHaveProperty("tmdbId", 42);
    expect(pick).not.toHaveProperty("seasonNumber");
    expect(pick).not.toHaveProperty("episodeNumber");
  });
});

describe("SearchTakeover — 'Use show-level match only' (D-5)", () => {
  it("commits the no-slot payload without ever entering the season grid", async () => {
    const fetchMock = seriesFetch([season4]);
    vi.stubGlobal("fetch", fetchMock);
    const onCommit = commitSpy();
    const onDone = vi.fn();

    render(() => (
      <SearchTakeover
        heading="Re-pick “Wrong.Match.Show”"
        searchMode="series"
        initialQuery="A Show"
        autoSearch={false}
        onCommit={onCommit}
        onDone={onDone}
        onCancel={vi.fn()}
      />
    ));

    await pickShow();

    // The escape hatch is a SIBLING of the picker, above it — reachable with
    // the season grid untouched. That is the whole point of D-5: the grid
    // structurally cannot express "leave both blank", which is today's default
    // and most common Series repick.
    fireEvent.click(screen.getByText("Use show-level match only"));

    await waitFor(() => expect(onCommit).toHaveBeenCalledTimes(1));
    const pick = onCommit.mock.calls[0]![0];
    expect(pick).toHaveProperty("tmdbId", 42);
    expect(pick).toHaveProperty("title", "A Show");
    expect(pick).not.toHaveProperty("seasonNumber");
    expect(pick).not.toHaveProperty("episodeNumber");
    await waitFor(() => expect(onDone).toHaveBeenCalledTimes(1));
  });
});

describe("SearchTakeover — Cancel is a structural no-op", () => {
  it("with autoSearch={false}, Back calls onCancel and never touches fetch", () => {
    const fetchMock = vi.fn(async () => {
      throw new Error("fetch must not be called");
    });
    vi.stubGlobal("fetch", fetchMock);
    const onCancel = vi.fn();

    render(() => (
      <SearchTakeover
        heading="Move “Some Movie” to another section"
        searchMode="movies"
        initialQuery="Some Movie"
        autoSearch={false}
        onCommit={commitSpy()}
        onDone={vi.fn()}
        onCancel={onCancel}
      />
    ));

    fireEvent.click(screen.getByLabelText("Back to list"));

    expect(onCancel).toHaveBeenCalledTimes(1);
    // The blanket form is valid ONLY with auto-search off — see the next test.
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("with autoSearch={true}, the mount-time search GET fires but Back still commits nothing", async () => {
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, _init?: RequestInit) => {
        const url = String(input);
        if (url.startsWith(SEARCH_URL)) {
          return jsonResponse([catalogItem({ title: "Auto Show" })]);
        }
        throw new Error("unexpected fetch: " + url);
      },
    );
    vi.stubGlobal("fetch", fetchMock);
    const onCancel = vi.fn();
    const onCommit = commitSpy();
    const onDone = vi.fn();

    render(() => (
      <SearchTakeover
        heading="Re-pick “Wrong.Match.Show”"
        searchMode="series"
        initialQuery="Auto Show"
        autoSearch={true}
        onCommit={onCommit}
        onDone={onDone}
        onCancel={onCancel}
      />
    ));

    // Awaited before clicking Back: without this the "the GET fired" assertion
    // races the resource.
    await screen.findByLabelText("Use Auto Show");
    expect(
      fetchMock.mock.calls.some(([u]) => String(u).startsWith(SEARCH_URL)),
    ).toBe(true);

    fireEvent.click(screen.getByLabelText("Back to list"));
    expect(onCancel).toHaveBeenCalledTimes(1);

    // DO NOT "FIX" THIS INTO `expect(fetchMock).not.toHaveBeenCalled()`. That
    // is the correct assertion for autoSearch={false} (previous test) and is
    // FALSE here by design: the mount-time search GET legitimately fired. The
    // guarantee under test is that cancelling WRITES nothing, so the assertion
    // is "no commit" — no onCommit call, and every request that did happen was
    // the read-only search GET, never a POST to a commit endpoint.
    expect(onCommit).not.toHaveBeenCalled();
    expect(onDone).not.toHaveBeenCalled();
    for (const [url, init] of fetchMock.mock.calls) {
      expect(String(url)).toMatch(/^\/api\/modes\/series\/tmdb-search/);
      expect((init as RequestInit | undefined)?.method ?? "GET").toBe("GET");
    }
  });
});

describe("SearchTakeover — the takeover is never a dialog", () => {
  // Structural absence, guard-the-guard first — the same shape as
  // Discover.test.tsx's "row editor carries NO per-entity actions" helper: a
  // scoping change that matched nothing would otherwise make every absence
  // check below pass against an empty DOM.
  const expectNotADialog = (container: HTMLElement) => {
    expect(container.querySelector('section[aria-label="Search"]')).not.toBeNull();
    const classed = Array.from(container.querySelectorAll("[class]"));
    expect(classed.length).toBeGreaterThan(0);

    expect(container.querySelector('[role="dialog"]')).toBeNull();
    const overlays = classed.filter((el) => {
      const c = el.getAttribute("class") ?? "";
      return c.includes("fixed") && c.includes("inset-0");
    });
    expect(overlays).toHaveLength(0);
  };

  it("renders no role=dialog and no full-viewport overlay, in step 1 or step 2", async () => {
    vi.stubGlobal("fetch", seriesFetch([season4]));

    const { container } = render(() => (
      <SearchTakeover
        heading="Re-pick “Wrong.Match.Show”"
        searchMode="series"
        initialQuery="A Show"
        autoSearch={false}
        onCommit={commitSpy()}
        onDone={vi.fn()}
        onCancel={vi.fn()}
      />
    ));

    // Step 1, with a real result grid on screen.
    fireEvent.click(screen.getByText("Search"));
    await screen.findByLabelText("Use A Show");
    expectNotADialog(container);

    // Step 2 is the only place a nested modal could ever appear (the picker
    // renders inline here, deliberately), so this is the half that carries the
    // weight.
    fireEvent.click(screen.getByLabelText("Use A Show"));
    await screen.findByText("Season 4");
    expectNotADialog(container);
  });
});

// The preview slot — .omc/plans/autopilot-impl-rename-preview.md §6.2/§8.5.
// SearchTakeover knows nothing about proposals or video URLs; it just renders
// whatever JSX.Element the caller hands it under `preview`, between the
// subheading and the search form. These three tests cover the slot's CONTRACT
// (collapsed by default, absent when not supplied, survives the Series step-2
// transition) — not what Rename/Dedup actually pass into it, which is covered
// in Rename.preview.test.tsx and Dedup.test.tsx respectively.
describe("SearchTakeover — the caller-supplied preview slot", () => {
  it("renders the caller's preview slot, collapsed", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        throw new Error("unexpected fetch: " + String(input));
      }),
    );

    render(() => (
      <SearchTakeover
        heading="Re-pick “Wrong.Match.Show”"
        searchMode="movies"
        initialQuery="A Movie"
        autoSearch={false}
        preview={
          <SourcePreviewDisclosure
            src="/api/modes/movies/proposals/9/video"
            label="A Movie"
          />
        }
        onCommit={commitSpy()}
        onDone={vi.fn()}
        onCancel={vi.fn()}
      />
    ));

    expect(
      await screen.findByText("Preview source file"),
    ).toBeInTheDocument();
    expect(document.querySelector("video")).toBeNull();
  });

  it("renders nothing when no preview is supplied", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        throw new Error("unexpected fetch: " + String(input));
      }),
    );

    const { container } = render(() => (
      <SearchTakeover
        heading="Re-pick “Wrong.Match.Show”"
        searchMode="movies"
        initialQuery="A Movie"
        autoSearch={false}
        onCommit={commitSpy()}
        onDone={vi.fn()}
        onCancel={vi.fn()}
      />
    ));

    // GUARD THE GUARD FIRST — same shape as expectNotADialog above: a scoping
    // change that matched nothing would otherwise make the absence checks
    // below pass against an empty DOM.
    expect(
      container.querySelector('section[aria-label="Search"]'),
    ).not.toBeNull();
    expect(
      screen.getByLabelText("Catalog search query"),
    ).toBeInTheDocument();

    expect(document.querySelector("video")).toBeNull();
    expect(screen.queryByText("Preview source file")).toBeNull();
  });

  it("the preview survives the Series step-2 transition", async () => {
    vi.stubGlobal("fetch", seriesFetch([season4]));

    render(() => (
      <SearchTakeover
        heading="Re-pick “Wrong.Match.Show”"
        searchMode="series"
        initialQuery="A Show"
        autoSearch={false}
        preview={
          <SourcePreviewDisclosure
            src="/api/modes/series/proposals/11/video"
            label="A Show"
          />
        }
        onCommit={commitSpy()}
        onDone={vi.fn()}
        onCancel={vi.fn()}
      />
    ));

    expect(
      await screen.findByText("Preview source file"),
    ).toBeInTheDocument();

    await pickShow();
    await screen.findByText("Season 4");

    // Still present alongside the season grid — §6.2's placement (above the
    // results block, which is what keeps it reachable in both steps).
    expect(screen.getByText("Preview source file")).toBeInTheDocument();
  });
});

describe("SearchTakeover — images are proxied, never hot-linked", () => {
  it("Adult cards proxy imageUrl and fall back to TextPoster when it is absent", async () => {
    const withArt = adultCandidate({
      sceneId: "a1",
      title: "Art Scene",
      imageUrl: "https://cdn.example.com/a.jpg",
    });
    const noArt = adultCandidate({
      sceneId: "a2",
      title: "No Art Scene",
      imageUrl: undefined,
    });
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url.startsWith("/api/modes/adult/scene-search")) {
          return jsonResponse({ items: [withArt, noArt] });
        }
        throw new Error("unexpected fetch: " + url);
      }),
    );

    render(() => (
      <SearchTakeover
        heading="Move “Some File” to another section"
        searchMode="adult"
        initialQuery="Some File"
        autoSearch={false}
        onCommit={commitSpy()}
        onDone={vi.fn()}
        onCancel={vi.fn()}
      />
    ));

    fireEvent.click(screen.getByText("Search"));

    const artTile = await screen.findByLabelText("Use Art Scene");
    const img = artTile.querySelector("img")!;
    expect(img).not.toBeNull();
    const src = img.getAttribute("src")!;
    // The standing never-hot-link rule: every byte flows through the Go image
    // proxy, so the src is same-origin and the upstream URL only ever appears
    // percent-encoded inside its query string.
    expect(src.startsWith("/api/images/proxy?url=")).toBe(true);
    expect(src).toBe(
      "/api/images/proxy?url=" +
        encodeURIComponent("https://cdn.example.com/a.jpg"),
    );

    // Absent imageUrl -> proxyImage("") -> "" -> TextPoster, which is the
    // bg-surface-2 placeholder tile. Scoped with `within`, because TextPoster
    // renders the same label the card footer does.
    const blankTile = await screen.findByLabelText("Use No Art Scene");
    expect(blankTile.querySelector("img")).toBeNull();
    expect(blankTile.querySelector(".bg-surface-2")).not.toBeNull();
    // Structural, not a count: the missing <img> plus the bg-surface-2
    // placeholder are what prove TextPoster rendered. Pinning an exact
    // occurrence count would couple this to the card footer's markup, which
    // has nothing to do with the never-hot-link rule under test.
    expect(within(blankTile).getAllByText("No Art Scene").length).toBeGreaterThan(0);
    expect(artTile.querySelector(".bg-surface-2")).toBeNull();
  });

  it("catalog cards proxy the TMDB poster through the same mechanism", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url.startsWith("/api/modes/movies/tmdb-search")) {
          return jsonResponse([
            catalogItem({ title: "Target Movie", posterPath: "/abc.jpg" }),
          ]);
        }
        throw new Error("unexpected fetch: " + url);
      }),
    );

    render(() => (
      <SearchTakeover
        heading="Move “Some File” to another section"
        searchMode="movies"
        initialQuery="Target Movie"
        autoSearch={false}
        onCommit={commitSpy()}
        onDone={vi.fn()}
        onCancel={vi.fn()}
      />
    ));

    fireEvent.click(screen.getByText("Search"));

    const tile = await screen.findByLabelText("Use Target Movie");
    const src = tile.querySelector("img")!.getAttribute("src")!;
    expect(src.startsWith("/api/images/proxy?url=")).toBe(true);
    expect(src).toBe(
      "/api/images/proxy?url=" +
        encodeURIComponent("https://image.tmdb.org/t/p/w342/abc.jpg"),
    );
    // image.tmdb.org never reaches an <img src> unencoded.
    expect(src.startsWith("https://")).toBe(false);
  });
});

describe("SearchTakeover — a failed search does not re-throw mid-render", () => {
  it("keeps rendering its own chrome when the search resource errors", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({ error: "TMDB is unreachable" }, 500)),
    );

    // Adult mode on purpose: adultSoftErrors() is the FIRST results() read
    // inside the `<Show when={!results.error}>` guard, so this is the most
    // exposed path for the re-throw bug rule 4 exists to prevent. A Solid
    // resource re-throws on read once its fetcher has errored; without the
    // outer guard that throw lands mid-render and blanks the component (the
    // shipped GrabDialog bug class). Surviving chrome IS the assertion.
    render(() => (
      <SearchTakeover
        heading="Move “Some File” to another section"
        searchMode="adult"
        initialQuery="Some File"
        autoSearch={false}
        onCommit={commitSpy()}
        onDone={vi.fn()}
        onCancel={vi.fn()}
      />
    ));

    fireEvent.click(screen.getByText("Search"));

    expect(await screen.findByText("TMDB is unreachable")).toBeInTheDocument();
    expect(screen.getByLabelText("Back to list")).toBeInTheDocument();
    expect(screen.getByLabelText("Catalog search query")).toBeInTheDocument();
    expect(screen.getByText("Search")).toBeInTheDocument();
    // The guarded block did not render at all — not even its empty state.
    expect(screen.queryByText("No results.")).toBeNull();
  });
});
