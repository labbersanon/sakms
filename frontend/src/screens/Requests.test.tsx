// Requests tests: the cross-mode worklist renders one row per title and filters
// by status chip. Mirrors this repo's Discover test conventions
// (stubGlobal("fetch") + jsonResponse).

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import type { RequestStatusItem, RequestStatusResponse } from "@dto";
import { Requests } from "./Requests";

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

// noContent mirrors the real POST /api/requests/exclude → 204 (api() returns
// null on a 204, which excludeTitle types as void).
const noContent = (): Response => new Response(null, { status: 204 });

type Call = { url: string; method: string; body: unknown };

// stubReqFetch captures every request so a test can assert exact exclude bodies,
// mirroring Discover.grab.test.tsx's stubFetch harness.
const stubReqFetch = (handler: (url: string) => Response): Call[] => {
  const calls: Call[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push({
      url,
      method: (init?.method ?? "GET").toUpperCase(),
      body: init?.body ? JSON.parse(init.body as string) : undefined,
    });
    return handler(url);
  });
  vi.stubGlobal("fetch", fn);
  return calls;
};

const item = (over: Partial<RequestStatusItem> = {}): RequestStatusItem => ({
  mode: "movies",
  title: "A Title",
  tmdbId: 1,
  status: "In Library",
  grabId: 0,
  missingCount: 0,
  ...over,
});

// emptyPreview is the full 4×4×2 all-nil availability grid the real handler
// emits when nothing qualifies — DetailPopup reads named grid cells, so a bare
// {} would break it.
const emptyTier = () => ({ usenet: undefined, torrent: undefined });
const emptyRes = () => ({
  low: emptyTier(),
  medium: emptyTier(),
  high: emptyTier(),
  lossless: emptyTier(),
});
const emptyPreview = () => ({
  res2160: emptyRes(),
  res1080: emptyRes(),
  res720: emptyRes(),
  res480: emptyRes(),
});

const stubRequests = (resp: RequestStatusResponse) => {
  const fn = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.includes("/api/requests")) return jsonResponse(resp);
    throw new Error("unexpected fetch: " + url);
  });
  vi.stubGlobal("fetch", fn);
};

afterEach(() => vi.unstubAllGlobals());

describe("Requests", () => {
  it("renders one row per title with its status and missing count", async () => {
    stubRequests({
      items: [
        item({ title: "Owned Movie", status: "In Library" }),
        item({
          mode: "series",
          title: "Grabbing Show",
          tmdbId: 7,
          status: "Downloading",
        }),
        // "In Library" with a count, not a "Missing" status: Missing is an
        // annotation the backend hangs off a real status, never a status itself.
        item({
          mode: "series",
          title: "Incomplete Show",
          tmdbId: 8,
          status: "In Library",
          missingCount: 3,
        }),
      ],
    });

    render(() => <Requests />);

    expect(await screen.findByText("Owned Movie")).toBeInTheDocument();
    expect(screen.getByText("Grabbing Show")).toBeInTheDocument();
    expect(screen.getByText("Incomplete Show")).toBeInTheDocument();
    // Missing count surfaced.
    expect(screen.getByText(/3 missing/)).toBeInTheDocument();
  });

  it("filters rows by status chip", async () => {
    stubRequests({
      items: [
        item({ title: "Owned Movie", status: "In Library" }),
        item({ title: "Grabbing Movie", tmdbId: 2, status: "Downloading" }),
      ],
    });

    render(() => <Requests />);
    await screen.findByText("Owned Movie");

    // Clicking the "Downloading" status chip hides the In Library row.
    fireEvent.click(screen.getByRole("button", { name: "Downloading" }));
    expect(screen.queryByText("Owned Movie")).not.toBeInTheDocument();
    expect(screen.getByText("Grabbing Movie")).toBeInTheDocument();

    // "All" restores both.
    fireEvent.click(screen.getByRole("button", { name: "All" }));
    expect(screen.getByText("Owned Movie")).toBeInTheDocument();
    expect(screen.getByText("Grabbing Movie")).toBeInTheDocument();
  });

  // Base rows for the "Has Missing Episodes" cases: a movie and a series with
  // nothing missing, plus one series that is missing 3 episodes.
  const missingRows = () => [
    item({ title: "Complete Movie", status: "In Library" }),
    item({ mode: "series", title: "Complete Show", tmdbId: 7, status: "In Library" }),
    item({
      mode: "series",
      title: "Incomplete Show",
      tmdbId: 8,
      status: "In Library",
      missingCount: 3,
    }),
  ];

  it("the missing chip filters to rows with missing episodes", async () => {
    stubRequests({ items: missingRows() });

    render(() => <Requests />);
    await screen.findByText("Incomplete Show");

    fireEvent.click(screen.getByRole("button", { name: "Has Missing Episodes" }));
    expect(screen.getByText("Incomplete Show")).toBeInTheDocument();
    expect(screen.queryByText("Complete Movie")).not.toBeInTheDocument();
    expect(screen.queryByText("Complete Show")).not.toBeInTheDocument();
  });

  it("the missing chip toggles back off, restoring every row", async () => {
    stubRequests({ items: missingRows() });

    render(() => <Requests />);
    await screen.findByText("Incomplete Show");

    const chip = () => screen.getByRole("button", { name: "Has Missing Episodes" });
    fireEvent.click(chip());
    expect(screen.queryByText("Complete Movie")).not.toBeInTheDocument();

    // A boolean toggle, not a one-of selector: the same chip clears it (there is
    // no "All" of its own to fall back to).
    fireEvent.click(chip());
    expect(screen.getByText("Complete Movie")).toBeInTheDocument();
    expect(screen.getByText("Complete Show")).toBeInTheDocument();
    expect(screen.getByText("Incomplete Show")).toBeInTheDocument();
  });

  it("the missing chip is an independent filter, not a fourth status", async () => {
    // Both extra rows are reachable backend states: the In-Library pass sets
    // MissingCount on the series row, and the grab pass then overwrites only
    // Status — so a Downloading/Pending Retry series keeps its count.
    stubRequests({
      items: [
        ...missingRows(),
        item({
          mode: "series",
          title: "Grabbing Show",
          tmdbId: 9,
          status: "Downloading",
          missingCount: 2,
        }),
        item({
          mode: "series",
          title: "Retrying Show",
          tmdbId: 10,
          status: "Pending Retry",
          missingCount: 2,
        }),
      ],
    });

    render(() => <Requests />);
    await screen.findByText("Incomplete Show");

    fireEvent.click(screen.getByRole("button", { name: "Has Missing Episodes" }));
    expect(screen.getByText("Grabbing Show")).toBeInTheDocument();
    expect(screen.getByText("Retrying Show")).toBeInTheDocument();
    expect(screen.getByText("Incomplete Show")).toBeInTheDocument();

    // Adding a status chip intersects with the missing chip rather than
    // replacing it.
    fireEvent.click(screen.getByRole("button", { name: "In Library" }));
    expect(screen.getByText("Incomplete Show")).toBeInTheDocument();
    expect(screen.queryByText("Grabbing Show")).not.toBeInTheDocument();
    expect(screen.queryByText("Retrying Show")).not.toBeInTheDocument();
    expect(screen.queryByText("Complete Show")).not.toBeInTheDocument();
    expect(screen.queryByText("Complete Movie")).not.toBeInTheDocument();
  });

  it("single Remove calls the exclude endpoint with the correct body, after a confirm", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const calls = stubReqFetch((url) => {
      if (url.includes("/api/requests/exclude")) return noContent();
      if (url.includes("/api/requests"))
        return jsonResponse({ items: [item({ title: "Owned Movie", tmdbId: 5 })] });
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Requests />);
    await screen.findByText("Owned Movie");
    // The per-row Remove button (there is exactly one row).
    fireEvent.click(screen.getByText("Remove"));

    expect(window.confirm).toHaveBeenCalled();
    await waitFor(() =>
      expect(calls.some((c) => c.url.endsWith("/api/requests/exclude"))).toBe(
        true,
      ),
    );
    const exclude = calls.find((c) => c.url.endsWith("/api/requests/exclude"))!;
    expect(exclude.method).toBe("POST");
    expect(exclude.body).toMatchObject({
      mode: "movies",
      tmdbId: 5,
      title: "Owned Movie",
    });
  });

  it("does NOT call exclude when the confirm is cancelled", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(false);
    const calls = stubReqFetch((url) => {
      if (url.includes("/api/requests/exclude")) return noContent();
      if (url.includes("/api/requests"))
        return jsonResponse({ items: [item({ title: "Owned Movie", tmdbId: 5 })] });
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Requests />);
    await screen.findByText("Owned Movie");
    fireEvent.click(screen.getByText("Remove"));

    expect(window.confirm).toHaveBeenCalled();
    expect(calls.some((c) => c.url.includes("/api/requests/exclude"))).toBe(
      false,
    );
  });

  it("bulk Remove calls exclude-batch with all selected items", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const calls = stubReqFetch((url) => {
      if (url.includes("/api/requests/exclude-batch"))
        return jsonResponse({
          results: [
            { index: 0, mode: "movies", title: "Owned Movie", ok: true },
            { index: 1, mode: "adult", title: "A Scene", ok: true },
          ],
        });
      if (url.includes("/api/requests"))
        return jsonResponse({
          items: [
            item({ title: "Owned Movie", tmdbId: 5 }),
            item({ mode: "adult", title: "A Scene", tmdbId: 0 }),
          ],
        });
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Requests />);
    await screen.findByText("Owned Movie");

    // Select both rows via each row's checkbox, then Remove Selected.
    fireEvent.click(screen.getByLabelText("Select Owned Movie"));
    fireEvent.click(screen.getByLabelText("Select A Scene"));
    fireEvent.click(screen.getByRole("button", { name: /Remove Selected \(2\)/ }));

    expect(window.confirm).toHaveBeenCalled();
    await waitFor(() =>
      expect(
        calls.some((c) => c.url.endsWith("/api/requests/exclude-batch")),
      ).toBe(true),
    );
    const batch = calls.find((c) =>
      c.url.endsWith("/api/requests/exclude-batch"),
    )!;
    expect(batch.method).toBe("POST");
    // Adult row sends title only (tmdbId 0 dropped); movie sends tmdbId + title.
    expect(batch.body).toEqual({
      items: [
        { mode: "movies", tmdbId: 5, title: "Owned Movie" },
        { mode: "adult", title: "A Scene" },
      ],
    });
  });

  it("renders a Pending Retry row's retryAfter/retryReason as a human-readable line", async () => {
    const inSixHours = new Date(Date.now() + 6 * 60 * 60 * 1000).toISOString();
    stubRequests({
      items: [
        item({
          title: "No Match Movie",
          tmdbId: 9,
          status: "Pending Retry",
          retryAfter: inSixHours,
          retryReason: "no candidate cleared the quality floor",
        }),
      ],
    });

    render(() => <Requests />);

    expect(await screen.findByText("No Match Movie")).toBeInTheDocument();
    // Status badge reads the honest state, not "Downloading" (there are two
    // "Pending Retry" texts on screen — the filter chip button and the row's
    // status badge span — so scope to the non-button one).
    expect(
      screen.getByText("Pending Retry", { selector: "span" }),
    ).toBeInTheDocument();
    // Human-readable countdown + reason, never a raw ISO timestamp.
    expect(
      screen.getByText(/Retrying in 6h — no candidate cleared the quality floor/),
    ).toBeInTheDocument();
    expect(screen.queryByText(inSixHours)).not.toBeInTheDocument();
  });

  it("renders a Pending Retry row with only a reason (no scheduled retryAfter yet)", async () => {
    stubRequests({
      items: [
        item({
          title: "No Match Movie",
          tmdbId: 9,
          status: "Pending Retry",
          retryReason: "no candidate cleared the quality floor",
        }),
      ],
    });

    render(() => <Requests />);

    expect(await screen.findByText("No Match Movie")).toBeInTheDocument();
    expect(
      screen.getByText("no candidate cleared the quality floor"),
    ).toBeInTheDocument();
  });

  // A held Calendar pre-release request. The backend half of this shipped
  // correct — grabStatusLabel returns "Scheduled" for a future hold_until and
  // RequestStatusItem carries holdUntil — but the frontend never rendered it,
  // so the row showed a bare "Scheduled" chip with no date and no explanation:
  // exactly the outcome plan §5.6 exists to prevent. An operator seeing it
  // against a film six months out has no way to tell it from a stuck request.
  it("renders a Scheduled row's holdUntil date, not a bare chip", async () => {
    stubRequests({
      items: [
        item({
          title: "Unreleased Blockbuster",
          tmdbId: 9,
          status: "Scheduled",
          // A held row is born with an EMPTY retryAfter — that emptiness IS the
          // hold — so holdUntil is the only date this row has to show.
          retryReason: "held until its release date",
          holdUntil: "2026-12-25T00:00:00Z",
        }),
      ],
    });

    render(() => <Requests />);

    expect(
      await screen.findByText("Unreleased Blockbuster"),
    ).toBeInTheDocument();
    expect(
      screen.getByText("Scheduled", { selector: "span" }),
    ).toBeInTheDocument();
    // The release date, rendered — the whole point of the fix.
    expect(screen.getByText(/2026-12-25/)).toBeInTheDocument();
    // …and never the raw ISO timestamp, matching the Pending Retry line's own
    // "no raw timestamps" convention directly above.
    expect(
      screen.queryByText("2026-12-25T00:00:00Z"),
    ).not.toBeInTheDocument();
  });

  // COPY HONESTY, and the reason the obvious wording is wrong. Upcoming.tsx's
  // §5.7 note is explicit that promising an automatic search is a LIE while
  // usenet_autograb_enabled is off — the default on every install, where the
  // retry cycle never starts and nothing will ever promote this row on its own.
  // Requests does not read that toggle, so its copy must be true in BOTH states:
  // state the hold, never promise the search.
  it("does not promise an automatic search it cannot guarantee", async () => {
    stubRequests({
      items: [
        item({
          title: "Unreleased Blockbuster",
          tmdbId: 9,
          status: "Scheduled",
          holdUntil: "2026-12-25T00:00:00Z",
        }),
      ],
    });

    render(() => <Requests />);
    await screen.findByText("Unreleased Blockbuster");

    expect(
      screen.queryByText(/searched? automatically|automatically search/i),
    ).not.toBeInTheDocument();
  });

  // The one case where the two fields DISAGREE, and the only way to get the
  // blurb wrong now. holdUntil is never cleared — it is permanent provenance
  // that outlives the hold (CLAUDE.md; TestReleaseDueGrabsPromotesTheSameRow) —
  // so a PROMOTED pre-release row carries a PAST holdUntil while genuinely
  // being on the retry track. grabStatusLabel already reports that honestly as
  // "Pending Retry", and the blurb must follow the status, not the presence of
  // holdUntil: rendering "Held until <a date last month>" for a row that is
  // actively retrying would be a straight UI lie.
  it("renders the retry line, not the held line, for a promoted row whose holdUntil has passed", async () => {
    const inSixHours = new Date(Date.now() + 6 * 60 * 60 * 1000).toISOString();
    stubRequests({
      items: [
        item({
          title: "Now Released Film",
          tmdbId: 9,
          status: "Pending Retry",
          retryAfter: inSixHours,
          retryReason: "no candidate cleared the quality floor",
          // In the past: this row promoted, was searched, and found nothing.
          holdUntil: "2026-01-05T00:00:00Z",
        }),
      ],
    });

    render(() => <Requests />);
    await screen.findByText("Now Released Film");

    expect(
      screen.getByText(
        /Retrying in 6h — no candidate cleared the quality floor/,
      ),
    ).toBeInTheDocument();
    expect(screen.queryByText(/Held until/)).not.toBeInTheDocument();
    expect(screen.queryByText(/2026-01-05/)).not.toBeInTheDocument();
  });

  // A Scheduled row with no holdUntil must not render a line with a blank or
  // "Invalid Date" where the date should be. Defensive because RetryAfter and
  // HoldUntil are independent optional fields on the DTO.
  it("renders no blurb for a Scheduled row carrying no holdUntil", async () => {
    stubRequests({
      items: [
        item({ title: "Dateless Request", tmdbId: 9, status: "Scheduled" }),
      ],
    });

    render(() => <Requests />);
    await screen.findByText("Dateless Request");

    expect(screen.queryByText(/Invalid Date|NaN/)).not.toBeInTheDocument();
  });

  it("a Pending Retry row is still excludable via Remove", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const calls = stubReqFetch((url) => {
      if (url.includes("/api/requests/exclude")) return noContent();
      if (url.includes("/api/requests"))
        return jsonResponse({
          items: [
            item({
              title: "No Match Movie",
              tmdbId: 9,
              status: "Pending Retry",
              retryReason: "no candidate cleared the quality floor",
            }),
          ],
        });
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Requests />);
    await screen.findByText("No Match Movie");
    fireEvent.click(screen.getByText("Remove"));

    expect(window.confirm).toHaveBeenCalled();
    await waitFor(() =>
      expect(calls.some((c) => c.url.endsWith("/api/requests/exclude"))).toBe(
        true,
      ),
    );
    const exclude = calls.find((c) => c.url.endsWith("/api/requests/exclude"))!;
    expect(exclude.body).toMatchObject({ mode: "movies", tmdbId: 9 });
  });

  it("opens the DetailPopup for a Movies/Series row click", async () => {
    // Row click mounts DetailPopup, which fires its own detail/availability/
    // trailer/quality-prefs fetches — stub them benignly so nothing throws.
    const fn = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/api/requests"))
        return jsonResponse({ items: [item({ title: "Owned Movie", tmdbId: 42 })] });
      if (url.includes("/discover/availability")) return jsonResponse(emptyPreview());
      if (url.includes("/discover/detail")) return jsonResponse({});
      if (url.includes("/discover/trailer")) return jsonResponse({ url: "" });
      if (url.includes("/quality-prefs"))
        return jsonResponse({ tier: "high", maxResolution: 0, protocol: "" });
      throw new Error("unexpected fetch: " + url);
    });
    vi.stubGlobal("fetch", fn);

    render(() => <Requests />);
    fireEvent.click(await screen.findByText("Owned Movie"));

    // The popup mounts (its Close button appears).
    expect(await screen.findByRole("button", { name: "Close" })).toBeInTheDocument();
  });
});
