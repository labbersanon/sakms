// MoveModePanel — Wave 2 (W2c) coverage for the behavioral requirements
// called out in .omc/plans/autopilot-impl.md §6.2:
//   1. Search-403 handling is SECTION-AGNOSTIC (a non-Adult section name
//      renders through the shared handler, not hardcoded "Adult" copy).
//   2. Commit-403 (adult-content section_locked) is a SEPARATE branch from
//      search-403, with its own distinct copy, and does NOT call onDone().
//   3. Season/episode `!= null` semantics: season 0 (Specials) is a real,
//      submittable value, not dropped by a truthiness check.
//   4. Cancel issues ZERO network calls — a structural guarantee, verified by
//      spying on fetch.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { MoveModePanel } from "./MoveModePanel";

afterEach(() => vi.unstubAllGlobals());

const jsonResponse = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });

describe("MoveModePanel — search 403 is section-agnostic", () => {
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
      <MoveModePanel
        proposalId={1}
        sourceName="Some Movie"
        targetMode="movies"
        workflow="rename"
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
      <MoveModePanel
        proposalId={1}
        sourceName="Some Scene"
        targetMode="adult"
        workflow="rename"
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
      <MoveModePanel
        proposalId={1}
        sourceName="Some Movie"
        targetMode="movies"
        workflow="rename"
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

describe("MoveModePanel — commit 403 is a SEPARATE branch from search 403", () => {
  it("a successful Adult search followed by a locked commit renders the commit-specific copy, keeps the panel open, and never calls onDone", async () => {
    const candidate = {
      box: "stashdb",
      sceneId: "abc123",
      title: "A Scene Title",
      studio: "A Studio",
      date: "2026-01-01",
    };
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

    render(() => (
      <MoveModePanel
        proposalId={1}
        sourceName="Some File"
        targetMode="adult"
        workflow="rename"
        onDone={onDone}
        onCancel={vi.fn()}
      />
    ));

    fireEvent.click(screen.getByText("Search"));
    const useButton = await screen.findByText("Use this");
    fireEvent.click(useButton);

    expect(
      await screen.findByText("Adult is PIN-locked — unlock to continue"),
    ).toBeInTheDocument();
    // Distinct from the search-403 copy — the search succeeded here.
    expect(
      screen.queryByText(/unlock it to search/),
    ).toBeNull();
    // Nothing was written: onDone must never fire.
    expect(onDone).not.toHaveBeenCalled();
    // The panel stays open with the candidate still selected/clickable —
    // unlock-and-retry is one click.
    expect(await screen.findByText("A Scene Title")).toBeInTheDocument();
    expect(screen.getByText("Use this")).toBeInTheDocument();
  });
});

describe("MoveModePanel — season/episode `!= null` semantics", () => {
  it("season 0 (Specials) is retained across input and submitted, not dropped by truthiness", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.startsWith("/api/modes/series/tmdb-search")) {
        return jsonResponse([{ id: 42, title: "A Show", releaseDate: "2020-01-01" }]);
      }
      if (url === "/api/proposals/1/move-mode") {
        const body = JSON.parse(String(init?.body));
        // season 0 must ride along as a real 0, not be omitted.
        expect(body.seasonNumber).toBe(0);
        expect(body.episodeNumber).toBe(3);
        return new Response(null, { status: 204 });
      }
      throw new Error("unexpected fetch: " + url);
    });
    vi.stubGlobal("fetch", fetchMock);
    const onDone = vi.fn();

    render(() => (
      <MoveModePanel
        proposalId={1}
        sourceName="Some File"
        targetMode="series"
        workflow="rename"
        onDone={onDone}
        onCancel={vi.fn()}
      />
    ));

    fireEvent.click(screen.getByText("Search"));
    await screen.findByText(/A Show/);

    fireEvent.input(screen.getByLabelText("Season number"), {
      target: { value: "0" },
    });
    fireEvent.input(screen.getByLabelText("Episode number"), {
      target: { value: "3" },
    });
    // The season input's displayed value reflects 0, not a blank field —
    // `season() ?? ""` renders "0", not "" (which is what `if (season())`
    // truthiness would have produced for a stored 0).
    expect(
      (screen.getByLabelText("Season number") as HTMLInputElement).value,
    ).toBe("0");

    fireEvent.click(screen.getByText("Use this"));

    await waitFor(() => expect(onDone).toHaveBeenCalled());
  });

  it("a half-filled season/episode pair shows the inline error and blocks submit", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.startsWith("/api/modes/series/tmdb-search")) {
        return jsonResponse([{ id: 42, title: "A Show", releaseDate: "2020-01-01" }]);
      }
      throw new Error("unexpected fetch: " + url);
    });
    vi.stubGlobal("fetch", fetchMock);

    render(() => (
      <MoveModePanel
        proposalId={1}
        sourceName="Some File"
        targetMode="series"
        workflow="rename"
        onDone={vi.fn()}
        onCancel={vi.fn()}
      />
    ));

    fireEvent.click(screen.getByText("Search"));
    await screen.findByText(/A Show/);

    fireEvent.input(screen.getByLabelText("Season number"), {
      target: { value: "2" },
    });

    expect(
      await screen.findByText(
        "Enter both a season and an episode, or leave both blank.",
      ),
    ).toBeInTheDocument();
  });
});

describe("MoveModePanel — Cancel issues zero network calls", () => {
  it("clicking Cancel calls onCancel and never touches fetch", async () => {
    const fetchMock = vi.fn(async () => {
      throw new Error("fetch must not be called");
    });
    vi.stubGlobal("fetch", fetchMock);
    const onCancel = vi.fn();

    render(() => (
      <MoveModePanel
        proposalId={1}
        sourceName="Some Movie"
        targetMode="movies"
        workflow="rename"
        onDone={vi.fn()}
        onCancel={onCancel}
      />
    ));

    fireEvent.click(screen.getByText("Cancel"));

    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
