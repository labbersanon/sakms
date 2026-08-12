// SearchTakeover — the suite that supersedes MoveModePanel.test.tsx
// (.omc/plans/autopilot-impl.md §8.1). Five of that file's seven tests are
// migrated here; the sixth (a half-filled season/episode pair raising an inline
// error) is DELETED as structurally unreachable — SeasonEpisodePicker's grid
// emits a complete pick or nothing, never half a pair — and the seventh (the
// season-0 case) is REWRITTEN against the two-step grid flow rather than the
// deleted number inputs.
//
// APPENDED 2026-08-09: Series step 2 mounts SeasonEpisodeAccordion now, not
// SeasonEpisodePicker. The D-1 tests below kept every assertion AND every click
// — the two UIs happen to need the identical two-step interaction ("Season 4"
// then "Whole season"; "Specials" then "E3 · Special Three"), because expanding
// an accordion row and drilling into a season tile are the same two clicks. Only
// the comments changed, plus one NEW test for the accordion's currentSlot
// auto-expand wiring. The accordion's own behavior (multi-open, toggle-collapse,
// no images) is covered at component level in
// discover/SeasonEpisodeAccordion.test.tsx. The paragraph above stands as
// written: it is the migration's own history, not a claim about today's UI.
//
// WHAT THIS FILE IS FOR, beyond the migration. SearchTakeover's own header
// names five things a future session must not simplify away; the four that are
// observable from a test have a dedicated guard here:
//
//   rule 1 (D-1) — `episode === 0` maps to a SHOW-LEVEL commit with BOTH slot
//     fields omitted, because both endpoints 400 on `episodeNumber < 1`. Guarded
//     twice, once through the whole-season row and once through the degraded
//     free-text fallback. Season 0 is NOT collapsed by that rule, which is what
//     the rewritten season-0 test proves.
//   rule 2 (D-5) — "Use show-level match only" is not a redundant duplicate of
//     the whole-season row; it has its own omits-both-keys guard.
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
import { moveProposalMode, repickProposal } from "../api/rename";
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

// Season 0 is reserved for the season-0 test. Every other accordion test uses
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
const MOVIE_SEARCH_URL = "/api/modes/movies/tmdb-search";
const SEASONS_URL = "/api/modes/series/discover/detail";

// seriesFetch is the shared stub for the Series two-step flow: TWO tmdb-search
// hits, one ?sections=seasons hit. `seasons` is what the accordion's self-fetch
// resolves to — pass [] to force its degraded free-text fallback.
//
// THE MOVIES BRANCH IS NOT OPTIONAL. Series search is a dual call now (series
// catalog + movies catalog, Promise.all) — without it every test here would
// reject on the throw below rather than on any behaviour under test.
//
// `movies` DEFAULTS TO EMPTY on purpose. Every pre-existing test in this file
// expects exactly one result tile, and both stubs returning catalogItem() would
// render "A Show" twice — making getByLabelText("Use A Show") throw "found
// multiple elements" across the whole suite. The merge tests below pass their
// own movie fixtures explicitly.
const seriesFetch = (seasons: unknown[], movies: DiscoverItem[] = []) =>
  vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.startsWith(MOVIE_SEARCH_URL)) return jsonResponse(movies);
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
  it("season 0 (Specials) survives the SeasonEpisodeAccordion swap and ships a literal 0 paired with a real episode", async () => {
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

    // Expand the Specials row, then click its E3 episode row. Identical two
    // clicks to the grid's old drill-down — expanding a collapsed accordion row
    // and drilling into a season tile happen to need the same interaction, so
    // this test's steps did not have to change with the accordion swap. No
    // currentSlot is passed here, so every row starts collapsed and the first
    // click genuinely expands rather than collapsing.
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

  it("the whole-season row omits BOTH slot keys (never ships episodeNumber: 0)", async () => {
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
    // Expand season 4's row, then take the whole season.
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
    // Zero seasons -> SeasonEpisodeAccordion's state 4, the free-text S/E
    // inputs. The accordion duplicates that fallback from SeasonEpisodePicker
    // rather than importing it, so this test is also the guard that the
    // duplicate did not drift — same aria-labels, same blank-Episode-means-0
    // coercion, same "Go" submit.
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

describe("SearchTakeover — currentSlot reaches the accordion", () => {
  // The WIRING guard, deliberately separate from the accordion's own
  // auto-expand test (SeasonEpisodeAccordion.test.tsx). That one proves the
  // component honours the prop; this one proves SearchTakeover actually hands
  // it over — a component-level test passes just as happily against a mount
  // point that never passes `currentSlot` at all.
  it("expands the currently-matched season on mount, with no click", async () => {
    vi.stubGlobal("fetch", seriesFetch([specialsSeason, season4]));

    render(() => (
      <SearchTakeover
        heading="Re-pick “Wrong.Match.Show”"
        searchMode="series"
        initialQuery="A Show"
        autoSearch={false}
        currentSlot={{ season: 4, episode: 7 }}
        onCommit={commitSpy()}
        onDone={vi.fn()}
        onCancel={vi.fn()}
      />
    ));

    await pickShow();

    // Season 4's body is open without any interaction; Specials' is not. The
    // negative half is what makes this meaningful — an accordion that ignored
    // the prop and expanded everything would satisfy the positive half alone.
    expect(await screen.findByText("E7 · Seven")).toBeInTheDocument();
    expect(screen.queryByText("E3 · Special Three")).toBeNull();
  });
});

describe("SearchTakeover — 'Use show-level match only' (D-5)", () => {
  it("commits the no-slot payload without ever expanding a season row", async () => {
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
    // every accordion row still collapsed. That is the whole point of D-5: the
    // picker structurally cannot express "leave both blank", which is today's
    // default and most common Series repick. Unchanged by the accordion swap:
    // an accordion cannot express it either.
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
        // Series mode fans out to the movies catalog too; empty keeps this
        // test's single "Auto Show" tile unambiguous.
        if (url.startsWith(MOVIE_SEARCH_URL)) return jsonResponse([]);
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
    // Widened to `(movies|series)` because Series search is a dual call now —
    // one read-only GET per catalog. Kept as a PER-CALL assertion over every
    // request that happened, per this test's own comment above: the guarantee
    // is "cancelling WRITES nothing", so what matters is that each call was a
    // search GET, not how many there were.
    for (const [url, init] of fetchMock.mock.calls) {
      expect(String(url)).toMatch(/^\/api\/modes\/(movies|series)\/tmdb-search/);
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

// Series-mode search merges BOTH TMDB catalogs. Spec:
// .omc/specs/deep-interview-series-search-includes-movies.md. The motivating
// case is a short film TMDB files under movies that the operator tracks in
// their Series library.
//
// mergeFetch is separate from seriesFetch rather than a third parameter on it:
// seriesFetch's series branch is FIXED at one `catalogItem()`, and that is
// precisely what keeps the seven pre-existing tests' `getByLabelText("Use A
// Show")` unambiguous. These tests need both catalogs controlled independently,
// including an EMPTY series side, which that helper cannot express.
const mergeFetch = (
  movies: DiscoverItem[],
  series: DiscoverItem[],
  seasons: unknown[] = [],
) =>
  vi.fn(async (input: RequestInfo | URL, _init?: RequestInit) => {
    const url = String(input);
    if (url.startsWith(MOVIE_SEARCH_URL)) return jsonResponse(movies);
    if (url.startsWith(SEARCH_URL)) return jsonResponse(series);
    if (url.startsWith(SEASONS_URL)) return jsonResponse({ seasons });
    if (url === "/api/proposals/7/repick") return jsonResponse({ ok: true });
    throw new Error("unexpected fetch: " + url);
  });

// A short film: a MOVIES-catalog hit, with an id and title deliberately
// distinct from `catalogItem()`'s 42/"A Show". The distinctness is load-bearing
// in the end-to-end test below — with a shared id, that test would pass whether
// or not the movie's own tmdbId actually reached the wire.
const shortFilm = () =>
  catalogItem({ id: 777, title: "A Short Film", mediaType: "movie" });

const tmdbSearchCalls = (fetchMock: { mock: { calls: unknown[][] } }) =>
  fetchMock.mock.calls
    .map(([u]) => String(u))
    .filter((u) => u.includes("/tmdb-search"));

describe("SearchTakeover — Series search merges the movies catalog", () => {
  it("issues one tmdb-search per catalog, for the same query, and renders both sets in one grid", async () => {
    const fetchMock = mergeFetch([shortFilm()], [catalogItem()]);
    vi.stubGlobal("fetch", fetchMock);

    render(() => (
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

    fireEvent.click(screen.getByText("Search"));

    // Both catalogs' results reach the one grid.
    expect(await screen.findByLabelText("Use A Short Film")).toBeInTheDocument();
    expect(screen.getByLabelText("Use A Show")).toBeInTheDocument();

    const searches = tmdbSearchCalls(fetchMock);
    expect(searches).toHaveLength(2);
    expect(searches.some((u) => u.startsWith(MOVIE_SEARCH_URL))).toBe(true);
    expect(searches.some((u) => u.startsWith(SEARCH_URL))).toBe(true);
    // One query, fanned out — not two different searches.
    for (const u of searches) expect(u).toContain("q=A%20Show");
  });

  it("searches TMDB with the single query field", async () => {
    const fetchMock = mergeFetch([shortFilm()], [catalogItem()]);
    vi.stubGlobal("fetch", fetchMock);

    render(() => (
      <SearchTakeover
        heading="Re-pick “episode.mkv”"
        searchMode="series"
        initialQuery="Duck Soup"
        autoSearch={false}
        onCommit={commitSpy()}
        onDone={vi.fn()}
        onCancel={vi.fn()}
      />
    ));

    fireEvent.input(screen.getByLabelText("Catalog search query"), {
      target: { value: "Laurel and Hardy" },
    });
    fireEvent.click(screen.getByText("Search"));

    await screen.findByLabelText("Use A Show");

    const laurelSearches = tmdbSearchCalls(fetchMock).filter((u) =>
      decodeURIComponent(u).includes("Laurel"),
    );
    expect(laurelSearches.some((u) => u.startsWith(MOVIE_SEARCH_URL))).toBe(true);
    expect(laurelSearches.some((u) => u.startsWith(SEARCH_URL))).toBe(true);
    for (const u of laurelSearches) {
      expect(decodeURIComponent(u)).not.toContain("Duck");
    }
  });

  it("badges each result with the catalog it came out of, never crossed over", async () => {
    vi.stubGlobal("fetch", mergeFetch([shortFilm()], [catalogItem()]));

    render(() => (
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

    fireEvent.click(screen.getByText("Search"));

    const movieTile = await screen.findByLabelText("Use A Short Film");
    const seriesTile = screen.getByLabelText("Use A Show");
    expect(within(movieTile).getByText("Movie")).toBeInTheDocument();
    expect(within(seriesTile).getByText("Series")).toBeInTheDocument();
    // The negative half is what makes this meaningful: a badge hardcoded to one
    // literal would satisfy the positive half on whichever tile matched it.
    expect(within(movieTile).queryByText("Series")).toBeNull();
    expect(within(seriesTile).queryByText("Movie")).toBeNull();
  });

  it("does NOT de-duplicate a title present in both catalogs — both rows render, each badged", async () => {
    // Distinct ids, same title: genuinely two different TMDB entries, which is
    // why no dedup pass is correct (matching Mainstream's own precedent).
    vi.stubGlobal(
      "fetch",
      mergeFetch(
        [catalogItem({ id: 900, title: "Doubled" })],
        [catalogItem({ id: 901, title: "Doubled" })],
      ),
    );

    render(() => (
      <SearchTakeover
        heading="Re-pick “Wrong.Match.Show”"
        searchMode="series"
        initialQuery="Doubled"
        autoSearch={false}
        onCommit={commitSpy()}
        onDone={vi.fn()}
        onCancel={vi.fn()}
      />
    ));

    fireEvent.click(screen.getByText("Search"));

    // getAllBy, not getBy: two tiles share one aria-label here by design, and
    // getByLabelText would throw "found multiple elements".
    const tiles = await screen.findAllByLabelText("Use Doubled");
    expect(tiles).toHaveLength(2);
    // Movies-first, mirroring Mainstream.tsx:826-827's concatenation order.
    // Asserting the badges (not just the count) is what proves the two rows are
    // correctly tagged rather than merely duplicated.
    expect(within(tiles[0]!).getByText("Movie")).toBeInTheDocument();
    expect(within(tiles[1]!).getByText("Series")).toBeInTheDocument();
  });

  it("Movies-mode search is untouched: exactly one call, and no badge", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.startsWith(MOVIE_SEARCH_URL)) {
        return jsonResponse([catalogItem({ title: "Target Film" })]);
      }
      throw new Error("unexpected fetch: " + url);
    });
    vi.stubGlobal("fetch", fetchMock);

    render(() => (
      <SearchTakeover
        heading="Move “Some File” to another section"
        searchMode="movies"
        initialQuery="Target Film"
        autoSearch={false}
        onCommit={commitSpy()}
        onDone={vi.fn()}
        onCancel={vi.fn()}
      />
    ));

    fireEvent.click(screen.getByText("Search"));

    // GUARD THE GUARD FIRST — the tile, and specifically the footer line the
    // badge would live in (the year). Without this the absence checks below
    // would pass just as happily against a card that never rendered.
    const tile = await screen.findByLabelText("Use Target Film");
    expect(within(tile).getByText("2020")).toBeInTheDocument();

    // A movies-mode search returns one catalog, so a badge on every row would
    // be pure clutter. The badge test above proves the mechanism works, which
    // is what keeps this absence assertion non-vacuous.
    expect(within(tile).queryByText("Movie")).toBeNull();
    expect(within(tile).queryByText("Series")).toBeNull();

    const searches = tmdbSearchCalls(fetchMock);
    expect(searches).toHaveLength(1);
    expect(searches[0]!.startsWith(MOVIE_SEARCH_URL)).toBe(true);
  });

  it("a movie-origin pick drills into step 2 like any other, and degrades to free text with its EXISTING copy", async () => {
    // Zero seasons is not a contrivance here — it is what TMDB genuinely
    // returns for a movie's id, and letting it degrade naturally (rather than
    // special-casing movie picks) is the spec's explicit Round-5 choice.
    const fetchMock = mergeFetch([shortFilm()], [], []);
    vi.stubGlobal("fetch", fetchMock);
    const onCommit = commitSpy();

    render(() => (
      <SearchTakeover
        heading="Re-pick “Some.Short.Film”"
        searchMode="series"
        initialQuery="A Short Film"
        autoSearch={false}
        onCommit={onCommit}
        onDone={vi.fn()}
        onCancel={vi.fn()}
      />
    ));

    fireEvent.click(screen.getByText("Search"));
    fireEvent.click(await screen.findByLabelText("Use A Short Film"));

    // Step 2, not an immediate commit — useCatalogItem branches on
    // props.searchMode, never on the hit's origin.
    expect(
      await screen.findByText("Use show-level match only"),
    ).toBeInTheDocument();
    expect(screen.getByText("Change show")).toBeInTheDocument();
    expect(onCommit).not.toHaveBeenCalled();

    // The accordion self-fetched against the MOVIE's own tmdbId.
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
    expect(String(seasonsCall[0])).toContain("tmdbId=777");

    // Zero seasons -> the free-text fallback, unchanged and unreworded.
    expect(await screen.findByLabelText("Season")).toBeInTheDocument();
    expect(screen.getByLabelText("Episode")).toBeInTheDocument();
  });

  it("END-TO-END: a movie-origin pick with a typed season/episode POSTs a real repick carrying the MOVIE's tmdbId", async () => {
    // The acceptance criterion the spec singles out as needing a test rather
    // than inspection: this is the one place a silent regression would ship an
    // actually-bad proposal. So it asserts the REAL POST BODY produced by the
    // real repickProposal adapter (copied from Rename.tsx's commitRepick), not
    // just an onCommit spy's argument.
    const fetchMock = mergeFetch([shortFilm()], [], []);
    vi.stubGlobal("fetch", fetchMock);
    const onDone = vi.fn();

    render(() => (
      <SearchTakeover
        heading="Re-pick “Some.Short.Film”"
        searchMode="series"
        initialQuery="A Short Film"
        autoSearch={false}
        onCommit={async (pick) => {
          if (pick.kind !== "catalog") {
            throw new Error("repick requires a catalog match");
          }
          await repickProposal(7, {
            tmdbId: pick.tmdbId,
            title: pick.title,
            year: pick.year,
            ...(pick.seasonNumber != null && pick.episodeNumber != null
              ? {
                  seasonNumber: pick.seasonNumber,
                  episodeNumber: pick.episodeNumber,
                }
              : {}),
          });
        }}
        onDone={onDone}
        onCancel={vi.fn()}
      />
    ));

    fireEvent.click(screen.getByText("Search"));
    fireEvent.click(await screen.findByLabelText("Use A Short Film"));

    // A REAL episode number, deliberately not blank: a blank Episode hits the
    // D-1 rule and collapses to a show-level commit, which would not exercise
    // the slot-carrying payload this test exists for.
    fireEvent.input(await screen.findByLabelText("Season"), {
      target: { value: "2" },
    });
    fireEvent.input(screen.getByLabelText("Episode"), {
      target: { value: "5" },
    });
    fireEvent.click(screen.getByText("Go"));

    await waitFor(() => expect(onDone).toHaveBeenCalledTimes(1));

    const post = fetchMock.mock.calls.find(
      ([u]) => String(u) === "/api/proposals/7/repick",
    )!;
    expect(post).toBeDefined();
    expect((post[1] as RequestInit).method).toBe("POST");
    const body = JSON.parse(String((post[1] as RequestInit).body));
    // 777, not 42: the MOVIE catalog's id reached the wire.
    expect(body).toHaveProperty("tmdbId", 777);
    expect(body).toHaveProperty("title", "A Short Film");
    expect(body).toHaveProperty("seasonNumber", 2);
    expect(body).toHaveProperty("episodeNumber", 5);
  });

  // Phase-4 review: the file header's `Promise.all` comment ASSERTS a
  // movies-catalog failure fails the whole series search and does not throw
  // mid-render — this is the test that PROVES it rather than leaving it
  // asserted. Mirrors "a failed search does not re-throw mid-render" below,
  // but that test fails every URL in adult mode; this one needs the series
  // half to succeed so the dual-call shape itself is what is under test.
  it("a movies-catalog failure fails the whole series search without re-throwing mid-render", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url.startsWith(MOVIE_SEARCH_URL)) {
          return jsonResponse({ error: "TMDB is unreachable" }, 500);
        }
        if (url.startsWith(SEARCH_URL)) return jsonResponse([catalogItem()]);
        throw new Error("unexpected fetch: " + url);
      }),
    );

    render(() => (
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

    fireEvent.click(screen.getByText("Search"));

    // The series half SUCCEEDED — if the merge partially rendered instead of
    // failing outright, "A Show" would appear here.
    expect(await screen.findByText("TMDB is unreachable")).toBeInTheDocument();
    expect(screen.queryByLabelText("Use A Show")).toBeNull();
    expect(screen.getByLabelText("Back to list")).toBeInTheDocument();
    expect(screen.queryByText("No results.")).toBeNull();
  });

  // The badge's own SIGHTED text isn't enough — an explicit aria-label
  // overrides all descendant content for the accessible NAME, so the badge is
  // wired as a DESCRIPTION (aria-describedby) instead, deliberately leaving
  // every tile's accessible name (`Use ${title}`) untouched so this feature
  // doesn't ripple `getByLabelText` queries across this suite and its
  // siblings. This test is what would catch a regression back to folding the
  // badge into aria-label.
  it("wires the badge as aria-describedby, not into the accessible name", async () => {
    vi.stubGlobal("fetch", mergeFetch([shortFilm()], [catalogItem()]));

    render(() => (
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

    fireEvent.click(screen.getByText("Search"));

    const movieTile = await screen.findByLabelText("Use A Short Film");
    const describedBy = movieTile.getAttribute("aria-describedby");
    expect(describedBy).toBeTruthy();
    const badge = document.getElementById(describedBy!);
    expect(badge).not.toBeNull();
    expect(badge!.textContent).toBe("Movie");
  });

  // The corruption-risk finding from Phase-4 review, recorded in CLAUDE.md's
  // CORRECTED 2026-08-09 note: the id-negation structural fix was offered and
  // declined in favor of this warning. This is the test for the warning
  // itself, not for the (deliberately unfixed) underlying risk.
  it("shows the movie-origin advisory in step 2, and ONLY for a movie-origin pick", async () => {
    vi.stubGlobal("fetch", mergeFetch([shortFilm()], [catalogItem()], []));

    render(() => (
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

    fireEvent.click(screen.getByText("Search"));
    fireEvent.click(await screen.findByLabelText("Use A Short Film"));

    expect(
      await screen.findByText(/came from TMDB's movie catalog/),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByText("Change show"));
    fireEvent.click(await screen.findByLabelText("Use A Show"));

    expect(
      screen.queryByText(/came from TMDB's movie catalog/),
    ).toBeNull();
  });
});

describe("SearchTakeover — Series database dropdown", () => {
  it("shows Series name placeholder on TMDB and Episode name on TVDB", () => {
    render(() => (
      <SearchTakeover
        heading="Re-pick"
        searchMode="series"
        initialQuery="A Show"
        autoSearch={false}
        onCommit={commitSpy()}
        onDone={vi.fn()}
        onCancel={vi.fn()}
      />
    ));

    expect(screen.getByPlaceholderText("Series name")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Database"), {
      target: { value: "tvdb" },
    });
    expect(screen.getByPlaceholderText("Episode name")).toBeInTheDocument();
  });

  it("calls tvdb-search with kind=episode when TVDB is selected", async () => {
    const fetchMock = vi.fn(async (url: string) => {
      if (url.includes("/tvdb-search")) {
        return jsonResponse([
          {
            tmdbId: 42,
            title: "Duck Soup",
            seriesTitle: "Laurel & Hardy",
            releaseDate: "1921-01-01",
            seasonNumber: 3,
            episodeNumber: 1,
          },
        ]);
      }
      return jsonResponse([]);
    });
    vi.stubGlobal("fetch", fetchMock);

    render(() => (
      <SearchTakeover
        heading="Re-pick"
        searchMode="series"
        initialQuery="Duck Soup"
        initialSeriesDatabase="tvdb"
        autoSearch={false}
        onCommit={commitSpy()}
        onDone={vi.fn()}
        onCancel={vi.fn()}
      />
    ));

    fireEvent.click(screen.getByText("Search"));

    expect(await screen.findByLabelText("Use Duck Soup")).toBeInTheDocument();
    const tvdbCall = fetchMock.mock.calls.find(([u]) =>
      String(u).includes("/tvdb-search"),
    );
    expect(tvdbCall).toBeTruthy();
    expect(String(tvdbCall![0])).toContain("kind=episode");
  });
});

describe("SearchTakeover — Adult URL resolve", () => {
  it("calls scene-resolve for a pasted URL and shows one result", async () => {
    const fetchMock = vi.fn(async (url: string) => {
      if (url.includes("/scene-resolve")) {
        return jsonResponse({
          item: {
            box: "stashdb",
            sceneId: "a29768db-b3cd-4a71-a75e-4294373207bb",
            title: "Resolved Scene",
            studio: "Test Studio",
            date: "2024-01-01",
          },
        });
      }
      return jsonResponse({ items: [] });
    });
    vi.stubGlobal("fetch", fetchMock);

    render(() => (
      <SearchTakeover
        heading="Re-pick"
        searchMode="adult"
        initialQuery="https://stashdb.org/scenes/a29768db-b3cd-4a71-a75e-4294373207bb"
        autoSearch={false}
        onCommit={commitSpy()}
        onDone={vi.fn()}
        onCancel={vi.fn()}
      />
    ));

    fireEvent.click(screen.getByText("Search"));
    expect(await screen.findByLabelText("Use Resolved Scene")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([u]) => String(u).includes("/scene-resolve"))).toBe(
      true,
    );
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
