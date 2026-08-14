// SeasonEpisodePicker tests. The presentational half (FE-1a) passes every prop
// directly and stubs no fetch — covers plan §8's T-6.1..T-6.5 plus the
// state-precedence guard from §11 C-9 item 5 (tmdbId <= 0 renders the degraded
// fallback WITHOUT waiting on loading).
//
// FE-1b/FE-2 add the self-fetching half at the bottom: a mount that OMITS
// `seasons` fetches its own season block. Those tests stub fetch (same
// stubFetch/jsonResponse convention as Discover.test.tsx). The dividing line is
// prop presence, so a test that passes `seasons` — including as an explicit
// undefined — must fire no request at all, which is asserted rather than
// assumed: it is the guarantee that keeps DetailPopup from double-fetching the
// bundle it is already loading.

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@solidjs/testing-library";
import type { EpisodeSummary, SeasonSummary, TitleDetail } from "@dto";
import { SeasonEpisodePicker } from "./SeasonEpisodePicker";
import { jsonResponse } from "../../testing/http";


const stubFetch = (handler: (url: string) => Response | Promise<Response>) => {
  const fn = vi.fn(async (input: RequestInfo | URL) => handler(String(input)));
  vi.stubGlobal("fetch", fn);
  return fn;
};

afterEach(() => vi.unstubAllGlobals());

const episode = (over: Partial<EpisodeSummary> = {}): EpisodeSummary => ({
  episodeNumber: 1,
  name: "Pilot",
  airDate: "2019-04-06",
  runtime: 42,
  stillPath: "/still1.jpg",
  ...over,
});

const season = (over: Partial<SeasonSummary> = {}): SeasonSummary => ({
  seasonNumber: 4,
  name: "Season 4",
  airDate: "2019-04-06",
  episodeCount: 2,
  posterPath: "/poster4.jpg",
  episodes: [episode(), episode({ episodeNumber: 7, name: "The Long Night" })],
  ...over,
});

describe("SeasonEpisodePicker — season grid (T-6.1)", () => {
  it("renders one tile per season, proxied posters, MediaFallbackTile for a blank posterPath", () => {
    render(() => (
      <SeasonEpisodePicker
        tmdbId={1399}
        selectionMode="single"
        seasons={[
          season({ seasonNumber: 0, name: "Specials", posterPath: "", episodeCount: 3 }),
          season({ seasonNumber: 1, name: "Season 1", posterPath: "/p1.jpg", episodeCount: 10 }),
        ]}
      />
    ));

    // One tile per season, rendered in the order given (ordering is the API's
    // decision, deliberately not re-sorted here). The art-less tile's
    // MediaFallbackTile shows a letter, not a repeated title.
    expect(screen.getAllByText("Specials").length).toBeGreaterThan(0);
    expect(screen.getByText("Season 1")).toBeInTheDocument();

    // The season WITH art renders a proxied <img>; TMDB's host never reaches
    // an <img src> directly.
    const img = screen.getByRole("img", { name: "Season 1" });
    expect(img).toHaveAttribute(
      "src",
      "/api/images/proxy?url=" +
        encodeURIComponent("https://image.tmdb.org/t/p/w342/p1.jpg"),
    );

    // The season WITHOUT art renders no image at all — MediaFallbackTile
    // fills the poster hole, so scope the assertion to that tile.
    const specialsTile = screen.getAllByText("Specials")[0]!.closest("button")!;
    expect(within(specialsTile).queryByRole("img")).toBeNull();

    expect(screen.getByText(/10 eps/)).toBeInTheDocument();
    expect(screen.getByText(/3 eps/)).toBeInTheDocument();
  });
});

describe("SeasonEpisodePicker — drill-down (T-6.2)", () => {
  it("shows that season's episodes on click and returns via Back to Seasons", () => {
    render(() => (
      <SeasonEpisodePicker
        tmdbId={1399}
        selectionMode="single"
        seasons={[season({ seasonNumber: 1, name: "Season 1", episodes: [] }), season()]}
      />
    ));

    expect(screen.queryByText("Back to Seasons")).toBeNull();

    fireEvent.click(screen.getByText("Season 4").closest("button")!);

    // Season 4's own episodes, and only those.
    expect(screen.getByText("E1 · Pilot")).toBeInTheDocument();
    expect(screen.getByText("E7 · The Long Night")).toBeInTheDocument();
    expect(screen.queryByText("Season 1")).toBeNull();

    fireEvent.click(screen.getByText("Back to Seasons"));

    expect(screen.getByText("Season 1")).toBeInTheDocument();
    expect(screen.queryByText("E1 · Pilot")).toBeNull();
  });
});

describe("SeasonEpisodePicker — soft-failed season", () => {
  it("shows the empty-season notice but keeps the whole-season pack grabbable", () => {
    const onSubmit = vi.fn();
    // A season whose per-season episode fetch soft-failed arrives with
    // episodes: [] (empty, not absent). Its tile still renders, and drilling in
    // must still offer the whole-season pack — that is the point of rendering
    // the season card at all.
    render(() => (
      <SeasonEpisodePicker
        tmdbId={1399}
        selectionMode="single"
        seasons={[season({ seasonNumber: 1, name: "Season 1", episodeCount: 6, episodes: [] })]}
        onSubmit={onSubmit}
      />
    ));

    fireEvent.click(screen.getByText("Season 1").closest("button")!);

    expect(
      screen.getByText("No episode list available for this season."),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByText("Whole season"));
    expect(onSubmit).toHaveBeenCalledWith(1, 0);
  });
});

describe("SeasonEpisodePicker — still fallback (T-6.3)", () => {
  it("renders a title/number-only card with no <img> when stillPath is blank", () => {
    render(() => (
      <SeasonEpisodePicker
        tmdbId={1399}
        selectionMode="single"
        seasons={[
          season({
            episodes: [
              episode({ episodeNumber: 1, name: "Pilot", stillPath: "" }),
              episode({ episodeNumber: 2, name: "Withering", stillPath: "/s2.jpg" }),
            ],
          }),
        ]}
      />
    ));

    fireEvent.click(screen.getByText("Season 4").closest("button")!);

    // Scoped to the blank-still tile: a sibling episode DOES have a still, so a
    // document-wide assertion would pass/fail for the wrong reason.
    const blankTile = screen.getAllByText("E1 · Pilot")[0]!.closest("button")!;
    expect(within(blankTile).queryByRole("img")).toBeNull();
    expect(within(blankTile).getAllByText("E1 · Pilot").length).toBeGreaterThan(0);

    const stillTile = screen.getByText("E2 · Withering").closest("button")!;
    expect(within(stillTile).getByRole("img")).toHaveAttribute(
      "src",
      "/api/images/proxy?url=" +
        encodeURIComponent("https://image.tmdb.org/t/p/w300/s2.jpg"),
    );
  });
});

describe("SeasonEpisodePicker — degraded fallback (T-6.4)", () => {
  it("renders the free-text S/E inputs and still submits (4, 0) when seasons are absent", () => {
    const onSubmit = vi.fn();
    // Caller-managed with nothing to show: `seasons` IS passed (as undefined),
    // so the component fetches nothing and goes straight to state 4. The
    // self-fetching equivalent of this case is the fetch-failure test below.
    render(() => (
      <SeasonEpisodePicker
        tmdbId={1399}
        selectionMode="single"
        seasons={undefined}
        onSubmit={onSubmit}
      />
    ));

    fireEvent.input(screen.getByLabelText("Season"), { target: { value: "4" } });
    fireEvent.click(screen.getByText("Go"));

    expect(onSubmit).toHaveBeenCalledWith(4, 0);
  });

  // Phase-4 review FIX 1. The single-mode GrabButton path carries the identical
  // hazard the multi-mode one did (it predates the picker rewrite; the rewrite
  // only made it easier to reach), so it is fixed and covered in the same
  // component. Here the harm is one bad grab rather than a staged phantom
  // selection, but the empty-form input is the same.
  it("never submits a phantom season 0 from an empty or unparseable season field", () => {
    const onSubmit = vi.fn();
    render(() => (
      <SeasonEpisodePicker
        tmdbId={1399}
        selectionMode="single"
        seasons={undefined}
        onSubmit={onSubmit}
      />
    ));

    const go = screen.getByText("Go");
    // Disabled up front, so the empty form cannot be submitted by clicking.
    expect(go).toBeDisabled();
    fireEvent.click(go);
    expect(onSubmit).not.toHaveBeenCalled();

    // Enter in an input submits the form without touching the button, which is
    // why the handler carries its own guard rather than relying on `disabled`.
    fireEvent.submit(screen.getByLabelText("Season").closest("form")!);
    expect(onSubmit).not.toHaveBeenCalled();

    // A non-numeric entry is not a season either — parseInt would have taken
    // "abc" to NaN and then coerced it to 0.
    fireEvent.input(screen.getByLabelText("Season"), { target: { value: "abc" } });
    expect(go).toBeDisabled();
    fireEvent.submit(screen.getByLabelText("Season").closest("form")!);
    expect(onSubmit).not.toHaveBeenCalled();

    // A real season re-enables it, and an explicit 0 (Specials) is a legitimate
    // value the old `season <= 0` guard would have wrongly rejected.
    fireEvent.input(screen.getByLabelText("Season"), { target: { value: "0" } });
    expect(go).not.toBeDisabled();
    fireEvent.click(go);
    expect(onSubmit).toHaveBeenCalledWith(0, 0);
  });

  it("also falls back for an empty seasons array", () => {
    const onSubmit = vi.fn();
    render(() => (
      <SeasonEpisodePicker
        tmdbId={1399}
        selectionMode="single"
        seasons={[]}
        onSubmit={onSubmit}
      />
    ));

    fireEvent.input(screen.getByLabelText("Season"), { target: { value: "2" } });
    fireEvent.input(screen.getByLabelText("Episode"), { target: { value: "5" } });
    fireEvent.click(screen.getByText("Go"));

    expect(onSubmit).toHaveBeenCalledWith(2, 5);
  });

  it("renders skeletons, not the fallback, and fetches nothing, while a caller loads", () => {
    // Exactly DetailPopup's shape: `seasons` is passed but is still undefined
    // because the caller's own resource is in flight. The picker must show
    // state 1 on the caller's say-so AND must not fire its own request for the
    // bundle that caller is already fetching.
    const fetchFn = stubFetch(() => jsonResponse({}));
    render(() => (
      <SeasonEpisodePicker
        tmdbId={1399}
        selectionMode="single"
        seasons={undefined}
        loading={true}
      />
    ));

    expect(screen.getByLabelText("Loading seasons")).toBeInTheDocument();
    expect(screen.queryByLabelText("Season")).toBeNull();
    expect(fetchFn).not.toHaveBeenCalled();
  });

  it("renders the fallback for tmdbId <= 0 even while loading (no doomed lookup)", () => {
    const onSubmit = vi.fn();
    // Stubbed so a real network call can't escape, not to assert against: this
    // mount passes `seasons`, so prop PRESENCE already disables self-fetching
    // regardless of the tmdbId guard under test. A "fetch was not called"
    // assertion here could never fail and would read as coverage it isn't —
    // removed (Phase-4 review FIX 7). The self-fetching sibling test
    // ("fires no request at all for tmdbId <= 0") is where that guarantee is
    // genuinely exercised, because there the prop is omitted.
    stubFetch(() => jsonResponse({}));
    // The guard PRECEDES the loading check: "no enumeration possible" does not
    // become possible by waiting, so a 0-id call site must never see a skeleton.
    // Caller-managed (`seasons` passed) so `loading` is genuinely true here —
    // that is what makes this a precedence test rather than a vacuous one.
    render(() => (
      <SeasonEpisodePicker
        tmdbId={0}
        selectionMode="single"
        seasons={undefined}
        loading={true}
        onSubmit={onSubmit}
      />
    ));

    expect(screen.queryByLabelText("Loading seasons")).toBeNull();
    fireEvent.input(screen.getByLabelText("Season"), { target: { value: "3" } });
    fireEvent.click(screen.getByText("Go"));
    expect(onSubmit).toHaveBeenCalledWith(3, 0);
  });
});

describe("SeasonEpisodePicker — single-mode submit (T-6.5)", () => {
  it("submits (4, 0) for the whole-season tile and (4, 7) for an episode tile", () => {
    const onSubmit = vi.fn();
    render(() => (
      <SeasonEpisodePicker
        tmdbId={1399}
        selectionMode="single"
        seasons={[season()]}
        onSubmit={onSubmit}
      />
    ));

    fireEvent.click(screen.getByText("Season 4").closest("button")!);

    fireEvent.click(screen.getByText("Whole season"));
    expect(onSubmit).toHaveBeenCalledWith(4, 0);

    fireEvent.click(screen.getByText("E7 · The Long Night").closest("button")!);
    expect(onSubmit).toHaveBeenCalledWith(4, 7);
  });
});

describe("SeasonEpisodePicker — multi mode", () => {
  it("toggles picks instead of submitting, and reflects isSelected", () => {
    const onToggle = vi.fn();
    const onSubmit = vi.fn();
    render(() => (
      <SeasonEpisodePicker
        tmdbId={1399}
        selectionMode="multi"
        seasons={[season()]}
        onSubmit={onSubmit}
        onToggle={onToggle}
        isSelected={(p) => p.season === 4 && p.episode === 7}
      />
    ));

    fireEvent.click(screen.getByText("Season 4").closest("button")!);

    fireEvent.click(screen.getByText("Whole season"));
    expect(onToggle).toHaveBeenCalledWith({ season: 4, episode: 0 });

    fireEvent.click(screen.getByText("E1 · Pilot").closest("button")!);
    expect(onToggle).toHaveBeenCalledWith({ season: 4, episode: 1 });

    // onSubmit is a single-mode-only callback and must never fire here.
    expect(onSubmit).not.toHaveBeenCalled();

    // ring-accent, NOT border-accent: every tile carries hover:border-accent,
    // so a "border-accent" substring check would pass for unselected tiles too.
    const selectedTile = screen
      .getByText("E7 · The Long Night")
      .closest("button")!;
    expect(selectedTile.className).toContain("ring-accent");

    const unselectedTile = screen.getByText("E1 · Pilot").closest("button")!;
    expect(unselectedTile.className).not.toContain("ring-accent");
  });
});

// FE-1b/FE-2 — the self-fetching mode. A mount that OMITS `seasons` owns its
// own request; every test above passes the prop and therefore fetches nothing.
describe("SeasonEpisodePicker — self-fetching mode (FE-2)", () => {
  const detail = (seasons: SeasonSummary[]): Partial<TitleDetail> => ({ seasons });

  it("requests only the season block and renders the grid", async () => {
    const fetchFn = stubFetch(() => jsonResponse(detail([season()])));
    render(() => <SeasonEpisodePicker tmdbId={1399} selectionMode="single" />);

    expect(await screen.findByText("Season 4")).toBeInTheDocument();

    // Series' detail endpoint, scoped to seasons — NOT a new endpoint, and not
    // the full bundle (which would make the server fan out to credits/keywords/
    // providers/recommendations this picker never reads).
    expect(fetchFn).toHaveBeenCalledTimes(1);
    expect(String(fetchFn.mock.calls[0]![0])).toBe(
      "/api/modes/series/discover/detail?tmdbId=1399&sections=seasons",
    );
  });

  it("shows skeletons while its own request is in flight", async () => {
    let release!: (r: Response) => void;
    stubFetch(() => new Promise<Response>((res) => (release = res)));
    render(() => <SeasonEpisodePicker tmdbId={1399} selectionMode="single" />);

    // State 1, not state 4 — an in-flight fetch must never look like a failed
    // one, or the operator sees the degraded picker for a request that is about
    // to succeed.
    expect(screen.getByLabelText("Loading seasons")).toBeInTheDocument();
    expect(screen.queryByLabelText("Season")).toBeNull();

    release(jsonResponse(detail([season()])));
    expect(await screen.findByText("Season 4")).toBeInTheDocument();
  });

  it("falls back to the free-text picker when its own fetch fails", async () => {
    const onSubmit = vi.fn();
    stubFetch(() => new Response("boom", { status: 500 }));
    render(() => (
      <SeasonEpisodePicker
        tmdbId={1399}
        selectionMode="single"
        onSubmit={onSubmit}
      />
    ));

    // The failure resolves to an empty season list rather than rejecting: a
    // rejected resource re-throws on read, mid-render, which is how the shipped
    // GrabDialog hang happened. Grabbing must survive a TMDB outage.
    const seasonInput = await screen.findByLabelText("Season");
    fireEvent.input(seasonInput, { target: { value: "4" } });
    fireEvent.input(screen.getByLabelText("Episode"), { target: { value: "7" } });
    fireEvent.click(screen.getByText("Go"));

    expect(onSubmit).toHaveBeenCalledWith(4, 7);
  });

  it("falls back when the response carries a null seasons list", async () => {
    // TitleDetail.seasons is typed non-nullable, but a Go nil slice serializes
    // to JSON null, so tsc cannot catch a missing null-guard here.
    stubFetch(() => jsonResponse({ seasons: null }));
    render(() => <SeasonEpisodePicker tmdbId={1399} selectionMode="single" />);

    expect(await screen.findByLabelText("Season")).toBeInTheDocument();
  });

  it("fires no request at all for tmdbId <= 0", async () => {
    const fetchFn = stubFetch(() => jsonResponse(detail([season()])));
    render(() => <SeasonEpisodePicker tmdbId={0} selectionMode="single" />);

    // "No enumeration possible" does not become possible by asking TMDB.
    expect(await screen.findByLabelText("Season")).toBeInTheDocument();
    await waitFor(() => expect(fetchFn).not.toHaveBeenCalled());
  });

  it("drills into a fetched season's episodes", async () => {
    stubFetch(() => jsonResponse(detail([season()])));
    render(() => <SeasonEpisodePicker tmdbId={1399} selectionMode="single" />);

    fireEvent.click((await screen.findByText("Season 4")).closest("button")!);

    // The eager server-side fan-out means episodes arrive with the seasons —
    // drilling in triggers no second request.
    expect(screen.getByText("E7 · The Long Night")).toBeInTheDocument();
  });
});
