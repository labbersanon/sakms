// Stage 3 Rename UI tests — scan→propose→apply per mode.
//
// Claude 2026-08-06: No Status column; dropdown Rename/Re-pick/Dismiss with
//   Rename auto-selected on match; Apply all honors per-row selections.
// Reason: Status pill duplicated the action; Rename (not Apply) in the select.
// Review if: Purge/Dedup gain Apply all summary confirm.
//
// Claude 2026-08-08: Re-pick/Move selectors retargeted at SearchTakeover, and
//   the sole end-to-end repick-commit test rewritten for the two-step Series
//   flow (tile click mounts the picker; the commit is a second click).
// Reason: RepickPanel/MoveModePanel are deleted — "Use this" and "Cancel" no
//   longer exist. The candidate tile is aria-labelled `Use <title>` and the
//   back control is aria-label "Back to list" (visible text "Back").
// Troubleshooting: findByText("Use this"/"Cancel") timing out on a
//   repick/move test.
// Review if: SearchTakeover stops rendering a season/episode picker inline.
// Context: .omc/plans/autopilot-impl.md §8.3.
//
// Claude 2026-08-09: SearchTakeover's Series step 2 swapped from
//   SeasonEpisodePicker (grid) to SeasonEpisodeAccordion (collapsible list) —
//   see CLAUDE.md's "AMENDED 2026-08-09" note. The above Review-if condition
//   was NOT met (still an inline picker), so left in place; only the two
//   component-name references below were stale and are corrected.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@solidjs/testing-library";
import type { DiscoverItem, Proposal } from "@dto";
import { Rename } from "./Rename";

const runRowAction = async (
  sourceName: string,
  action: "rename" | "repick" | "dismiss" | "move:movies" | "move:series" | "move:adult",
) => {
  const row = (await screen.findByText(sourceName)).closest("tr, [data-proposal-row]")! as HTMLElement;
  expect(row).toBeTruthy();
  const select = within(row as HTMLElement).getByRole("combobox");
  fireEvent.change(select, { target: { value: action } });
  fireEvent.click(
    within(row as HTMLElement).getByRole("button", {
      name: `Apply selected action for ${sourceName}`,
    }),
  );
};

const clickRowApply = async (sourceName: string) => {
  // Match found → Rename is already selected.
  const row = (await screen.findByText(sourceName)).closest("tr, [data-proposal-row]")! as HTMLElement;
  expect(row).toBeTruthy();
  fireEvent.click(
    within(row as HTMLElement).getByRole("button", {
      name: `Apply selected action for ${sourceName}`,
    }),
  );
};

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

const noContent = (): Response => new Response(null, { status: 204 });

const proposal = (over: Partial<Proposal>): Proposal => ({
  id: 1,
  status: "pending",
  sourceName: "Some.Movie.2021.1080p",
  rootFolderPath: "/movies",
  title: "Some Movie",
  year: 2021,
  reason: "",
  draftId: "",
  ...over,
});

const tmdbItem = (over: Partial<DiscoverItem>): DiscoverItem => ({
  id: 1,
  title: "The Real Movie",
  posterPath: "/p.jpg",
  overview: "",
  releaseDate: "2019-03-01",
  voteAverage: 6.4,
  mediaType: "movie",
  ...over,
});

type Call = { url: string; method: string; body: unknown };
type Handler = (url: string, init?: RequestInit) => Response | Promise<Response>;

const pageOf = (items: Proposal[]) => ({
  items,
  total: items.length,
  limit: 50,
  offset: 0,
});

const stubFetch = (handler: Handler) => {
  const calls: Call[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push({
      url,
      method: (init?.method ?? "GET").toUpperCase(),
      body: init?.body ? JSON.parse(init.body as string) : undefined,
    });
    if (url.includes("/api/organize/events")) return jsonResponse([]);
    if (url.includes("/naming-preset")) return jsonResponse({ preset: "jellyfin" });
    // Rename Undo's "Recently Applied" list mounts unconditionally, so every
    // test in this file now hits it. Same try/fall-back shape as pending-ids
    // below: a test that cares stubs it in its own handler and wins; every
    // other handler stays about the case it was written for.
    if (url.includes("/rename/recently-applied")) {
      try {
        return await handler(url, init);
      } catch {
        return jsonResponse([]);
      }
    }
    if (url.includes("/pending-ids")) {
      try {
        return await handler(url, init);
      } catch {
        return jsonResponse({ ids: [] });
      }
    }
    const res = await handler(url, init);
    if (
      url.includes("/rename/proposals") &&
      !url.includes("pending-ids") &&
      res.headers.get("Content-Type")?.includes("json")
    ) {
      const cloned = res.clone();
      const body = await cloned.json();
      if (Array.isArray(body)) {
        return jsonResponse(pageOf(body as Proposal[]));
      }
    }
    return res;
  });
  vi.stubGlobal("fetch", fn);
  return calls;
};

const batchCalls = (calls: Call[]) =>
  calls.filter((c) => c.url.includes("/apply-batch"));
const singleApplyCalls = (calls: Call[]) =>
  calls.filter(
    (c) => c.url.includes("/apply") && !c.url.includes("/apply-batch"),
  );

afterEach(() => {
  vi.unstubAllGlobals();
  localStorage.clear();
});

describe("Rename — Movies (scan → propose → apply one)", () => {
  it("lists proposals and applies via Rename auto-selected in the dropdown", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([proposal({ id: 7, sourceName: "Movie.A" })]);
      if (
        url.includes("/api/proposals/7/apply") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    expect(await screen.findByText("Movie.A")).toBeInTheDocument();
    expect(screen.queryByText("Status")).toBeNull();
    expect(screen.queryByText("pending")).toBeNull();
    const row = screen.getByText("Movie.A").closest("tr, [data-proposal-row]")! as HTMLElement;
    const select = within(row).getByRole("combobox") as HTMLSelectElement;
    expect(select.value).toBe("rename");
    expect(within(select).getByRole("option", { name: "Rename" })).not.toBeDisabled();
    expect(within(select).queryByRole("option", { name: "Apply" })).toBeNull();
    expect(
      within(row).getAllByRole("button", { name: /Apply/ }),
    ).toHaveLength(1);
    await clickRowApply("Movie.A");

    await waitFor(() => expect(singleApplyCalls(calls)).toHaveLength(1));
    expect(singleApplyCalls(calls)[0]!.url).toContain("/api/proposals/7/apply");
  });

  it("triggers a scan then re-fetches the queue on the Scan button", async () => {
    let scanned = false;
    stubFetch((url, init) => {
      if (
        url.includes("/api/modes/movies/rename/scan") &&
        (init?.method ?? "").toUpperCase() === "POST"
      ) {
        scanned = true;
        return noContent();
      }
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse(
          scanned ? [proposal({ id: 1, sourceName: "Found.After.Scan" })] : [],
        );
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    expect(await screen.findByText(/No proposals yet/)).toBeInTheDocument();
    fireEvent.click(screen.getByText("Scan"));
    expect(await screen.findByText("Found.After.Scan")).toBeInTheDocument();
  });

  it("wraps the proposal table in a frosted panel for wallpaper contrast", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({ id: 1, sourceName: "Contrast.Check.mkv", status: "pending" }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });
    render(() => <Rename />);
    const cell = await screen.findByText("Contrast.Check.mkv");
    const panel = cell.closest(".backdrop-blur-md");
    expect(panel).toBeTruthy();
    expect((panel as HTMLElement).className).toContain("bg-surface/95");
  });

  it("requests view=live by default and view=history when Show history is on", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals")) {
        if (url.includes("view=history")) {
          return jsonResponse([
            proposal({
              id: 9,
              sourceName: "Done.Movie",
              status: "applied",
            }),
          ]);
        }
        return jsonResponse([proposal({ id: 1, sourceName: "Live.Movie" })]);
      }
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    expect(await screen.findByText("Live.Movie")).toBeInTheDocument();
    expect(screen.queryByText("Done.Movie")).toBeNull();
    expect(calls.some((c) => c.url.includes("view=live"))).toBe(true);

    fireEvent.click(screen.getByText("Show history"));
    expect(await screen.findByText("Done.Movie")).toBeInTheDocument();
    expect(screen.queryByText("Live.Movie")).toBeNull();
    expect(calls.some((c) => c.url.includes("view=history"))).toBe(true);
  });
});

describe("Rename — Apply all (summary confirm)", () => {
  it("shows Apply all and has no row selection checkboxes", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({ id: 1, sourceName: "A", status: "pending" }),
          proposal({ id: 2, sourceName: "B", status: "unmatched", title: "" }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("A");
    expect(screen.getByRole("button", { name: "Apply all" })).toBeInTheDocument();
    expect(screen.queryByLabelText("Select A")).toBeNull();
    expect(screen.queryByText(/Apply Selected/)).toBeNull();
    expect(screen.queryByText("Select page")).toBeNull();
    // Show history is the only checkbox.
    expect(document.querySelectorAll('input[type="checkbox"]')).toHaveLength(1);
  });

  it("Apply all opens summary then batch-applies on Confirm (Pending→Apply)", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({ id: 1, sourceName: "A.mkv", title: "Alpha", year: 2020 }),
          proposal({ id: 2, sourceName: "B.mkv", title: "Beta", year: 2021 }),
        ]);
      if (
        url.includes("/api/proposals/apply-batch") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return jsonResponse({
          results: [
            { id: 1, ok: true },
            { id: 2, ok: true },
          ],
        });
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("A.mkv");
    const rowA = screen.getByText("A.mkv").closest("tr, [data-proposal-row]")! as HTMLElement;
    expect((within(rowA).getByRole("combobox") as HTMLSelectElement).value).toBe(
      "rename",
    );
    expect(screen.queryByText("Status")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Apply all" }));

    expect(
      await screen.findByText(/Apply 2 selected actions/),
    ).toBeInTheDocument();
    expect(screen.getByText("Alpha (2020)")).toBeInTheDocument();
    expect(screen.getByText("Beta (2021)")).toBeInTheDocument();
    expect(batchCalls(calls)).toHaveLength(0);

    fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
    await waitFor(() => expect(batchCalls(calls)).toHaveLength(1));
    expect(singleApplyCalls(calls)).toHaveLength(0);
    expect(batchCalls(calls)[0]!.body).toEqual({
      items: [{ id: 1 }, { id: 2 }],
    });
    expect(await screen.findByText("2 applied, 0 failed")).toBeInTheDocument();
  });

  it("disables Apply all while apply-batch is in flight", async () => {
    let release!: (r: Response) => void;
    const held = new Promise<Response>((r) => {
      release = r;
    });
    stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({ id: 1, sourceName: "A.mkv", title: "Alpha", year: 2020 }),
        ]);
      if (
        url.includes("/api/proposals/apply-batch") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return held;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("A.mkv");
    fireEvent.click(screen.getByRole("button", { name: "Apply all" }));
    fireEvent.click(await screen.findByRole("button", { name: "Confirm" }));

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Applying…" })).toBeDisabled();
    });
    expect(screen.getByRole("button", { name: "Scan" })).toBeDisabled();

    release(
      jsonResponse({ results: [{ id: 1, ok: true }] }),
    );
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Apply all" })).not.toBeDisabled();
    });
  });

  it("Apply all follows per-row dismiss selection (not rename-only)", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({ id: 1, sourceName: "Keep.mkv", title: "Keep", year: 2020 }),
          proposal({
            id: 2,
            sourceName: "Drop.mkv",
            title: "Drop",
            year: 2021,
          }),
        ]);
      if (
        url.includes("/api/proposals/apply-batch") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return jsonResponse({ results: [{ id: 1, ok: true }] });
      if (
        url.includes("/api/proposals/2/dismiss") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("Keep.mkv");
    const dropRow = screen.getByText("Drop.mkv").closest("tr, [data-proposal-row]")! as HTMLElement;
    fireEvent.change(within(dropRow).getByRole("combobox"), {
      target: { value: "dismiss" },
    });

    fireEvent.click(screen.getByRole("button", { name: "Apply all" }));
    expect(
      await screen.findByText(/Apply 2 selected actions/),
    ).toBeInTheDocument();
    expect(screen.getByText("Dismiss proposal")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
    await waitFor(() => expect(batchCalls(calls)).toHaveLength(1));
    expect(batchCalls(calls)[0]!.body).toEqual({ items: [{ id: 1 }] });
    await waitFor(() =>
      expect(calls.some((c) => c.url.includes("/api/proposals/2/dismiss"))).toBe(
        true,
      ),
    );
  });

  it("Cancel on summary does not fire apply-batch", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([proposal({ id: 1, sourceName: "A.mkv" })]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("A.mkv");
    fireEvent.click(screen.getByRole("button", { name: "Apply all" }));
    expect(
      await screen.findByText(/Apply 1 selected action/),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() =>
      expect(screen.queryByText(/Apply 1 selected action/)).toBeNull(),
    );
    expect(batchCalls(calls)).toHaveLength(0);
  });

  it("still applies a single row through its Apply button", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({ id: 1, sourceName: "A" }),
          proposal({ id: 2, sourceName: "B" }),
        ]);
      if (
        url.includes("/api/proposals/2/apply") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("A");
    await clickRowApply("B");

    await waitFor(() => expect(singleApplyCalls(calls)).toHaveLength(1));
    expect(singleApplyCalls(calls)[0]!.url).toContain("/api/proposals/2/apply");
    expect(batchCalls(calls)).toHaveLength(0);
  });
});

describe("Rename — Series Re-pick (auto-search → use a new tmdb match)", () => {
  // The ONLY end-to-end repick-commit test that runs through Rename's real row
  // wiring (runRowAction -> SearchTakeover -> commitRepick), so it must survive
  // every refactor of that path rather than being replaced by a component-level
  // render. It does double duty: it is simultaneously that commit guard AND
  // D-5's show-level-escape-hatch guard — leaving both slot fields off is the
  // default, most common Series repick, and the accordion structurally cannot
  // express it (SeasonEpisodeAccordion always requires expanding a season row
  // first).
  it("re-points the proposal at the NEWLY chosen tmdbId, not its current one", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([]);
      if (url.includes("/api/modes/series/rename/proposals"))
        return jsonResponse([
          proposal({
            id: 12,
            sourceName: "Wrong.Match.Show",
            title: "Wrong Show",
            year: 2010,
          }),
        ]);
      // A Series-mode search queries BOTH TMDB catalogs (SearchTakeover merges
      // movie results in), so this branch is required or the Promise.all
      // rejects on the trailing throw. Empty keeps this test's single candidate
      // tile unambiguous.
      if (url.includes("/api/modes/movies/tmdb-search")) return jsonResponse([]);
      if (url.includes("/api/modes/series/tmdb-search"))
        return jsonResponse([
          tmdbItem({ id: 999, title: "The Right Show", releaseDate: "2018-01-01" }),
        ]);
      // SeasonEpisodeAccordion self-fetches its season list. Without this
      // branch the handler's trailing throw would reject it, the accordion
      // would resolve to [] and silently render its degraded free-text
      // fallback — where the show-level button below is still clickable, so
      // the rest of this test would pass while exercising the wrong state
      // entirely.
      if (url.includes("/api/modes/series/discover/detail"))
        return jsonResponse({
          seasons: [
            {
              seasonNumber: 1,
              name: "Season 1",
              airDate: "2018-01-01",
              episodeCount: 1,
              posterPath: "",
              episodes: [],
            },
          ],
        });
      if (
        url.includes("/api/proposals/12/repick") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    fireEvent.click(await screen.findByText("Series"));
    await runRowAction("Wrong.Match.Show", "repick");

    // Step 1 — the auto-search resolves and the candidate tile is clicked. For
    // Series this COMMITS NOTHING; it mounts the season/episode picker.
    fireEvent.click(await screen.findByLabelText("Use The Right Show"));

    // Step 2 — the picker really mounted, in its GRID state (a season tile),
    // not the degraded fallback. Asserting only the loading skeleton would not
    // discriminate: it is shown on the failing path too.
    expect(
      await screen.findByRole("button", { name: /Season 1/ }),
    ).toBeInTheDocument();
    expect(calls.some((c) => c.url.includes("sections=seasons"))).toBe(true);

    fireEvent.click(screen.getByText("Use show-level match only"));

    await waitFor(() =>
      expect(calls.some((c) => c.url.includes("/repick"))).toBe(true),
    );
    const repick = calls.find((c) => c.url.includes("/repick"));
    expect(repick?.body).toMatchObject({
      tmdbId: 999,
      title: "The Right Show",
      year: 2018,
    });
    // toMatchObject is a PARTIAL match and would not notice a stray slot key,
    // so these two negatives — not the match above — are the D-1/D-5 guard:
    // both endpoints 400 on episodeNumber < 1, and a show-level commit must
    // send neither field.
    expect(repick?.body).not.toHaveProperty("seasonNumber");
    expect(repick?.body).not.toHaveProperty("episodeNumber");
  });
});

describe("Rename — Adult Re-pick (scene-search → catalog match)", () => {
  it("posts box/sceneId to /repick when an adult scene is chosen", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([]);
      if (url.includes("/api/modes/series/rename/proposals"))
        return jsonResponse([]);
      if (url.includes("/api/modes/adult/rename/proposals"))
        return jsonResponse([
          proposal({
            id: 21,
            status: "unmatched",
            sourceName: "mystery.mp4",
            title: "",
            reason: "no match",
          }),
        ]);
      if (url.includes("/api/modes/adult/scene-search"))
        return jsonResponse({
          items: [
            {
              box: "stashdb",
              sceneId: "uuid-scene-1",
              title: "Right Scene",
              studio: "Studio A",
              date: "2023-06-15",
            },
          ],
        });
      if (
        url.includes("/api/proposals/21/repick") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    fireEvent.click(await screen.findByText("Adult"));
    await screen.findByText("mystery.mp4");

    await runRowAction("mystery.mp4", "repick");

    expect(
      await screen.findByText(/Search for a new match for “mystery\.mp4”/),
    ).toBeInTheDocument();

    fireEvent.click(await screen.findByLabelText("Use Right Scene"));

    await waitFor(() =>
      expect(calls.some((c) => c.url.includes("/repick"))).toBe(true),
    );
    const repick = calls.find((c) => c.url.includes("/repick"));
    expect(repick?.body).toMatchObject({
      title: "Right Scene",
      box: "stashdb",
      sceneId: "uuid-scene-1",
      studio: "Studio A",
      date: "2023-06-15",
    });
    expect(repick?.body).not.toHaveProperty("tmdbId");
  });
});

// US-001/US-002 (.omc/state/sessions/432c4084-7a92-43fb-9208-320d3452ed3c/prd.json):
// rowActionEnabled treats "repick" (and dismiss/delete/move:*) as valid for
// BOTH "unmatched" and "pending" BY DESIGN, so an already-matched row can
// still be re-picked. That means a stale "repick" selection stays
// "technically valid" straight through a successful Re-pick
// (unmatched -> pending), and the seeding effect's old validity-only check
// never caught it — the dropdown stayed stuck on "Re-pick" and Apply just
// re-opened the takeover instead of applying the fresh match.
describe("Rename — Re-pick selection reset (stale dropdown after a successful match)", () => {
  it("resets the dropdown to Rename once a successful Re-pick moves the row unmatched -> pending", async () => {
    let matched = false;
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/rename/proposals")) {
        return jsonResponse(
          matched
            ? [
                proposal({
                  id: 30,
                  status: "pending",
                  sourceName: "Stale.Selection.mkv",
                  title: "The Real Movie",
                  year: 2019,
                }),
              ]
            : [
                proposal({
                  id: 30,
                  status: "unmatched",
                  sourceName: "Stale.Selection.mkv",
                  title: "",
                  year: 0,
                  reason: "no match",
                }),
              ],
        );
      }
      if (url.includes("/api/modes/movies/tmdb-search"))
        return jsonResponse([
          tmdbItem({ id: 55, title: "The Real Movie", releaseDate: "2019-03-01" }),
        ]);
      if (
        url.includes("/api/proposals/30/repick") &&
        (init?.method ?? "").toUpperCase() === "POST"
      ) {
        matched = true;
        return noContent();
      }
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await runRowAction("Stale.Selection.mkv", "repick");

    // Commit the fresh match — Movies has no season/episode step, so this
    // single click commits immediately (same as the "commits immediately
    // for movies" case in Rename.repick.test.tsx).
    fireEvent.click(await screen.findByLabelText("Use The Real Movie"));

    await waitFor(() =>
      expect(calls.some((c) => c.url.includes("/repick"))).toBe(true),
    );

    // onDone closes the takeover and refetches; the row re-renders from the
    // now-"pending" proposal. Before the fix, the dropdown stayed on
    // "repick" here — this is the assertion that would have failed against
    // the old code (verified by temporarily reverting the fix: FAILS,
    // select.value === "repick").
    await waitFor(() => {
      const row = screen.getByText("Stale.Selection.mkv").closest("tr, [data-proposal-row]")! as HTMLElement;
      const select = within(row).getByRole("combobox") as HTMLSelectElement;
      expect(select.value).toBe("rename");
    });
  });

  it("does not disturb a deliberate manual selection on an already-pending row with no status transition", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({ id: 31, sourceName: "Already.Matched.mkv" }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    const row = (await screen.findByText("Already.Matched.mkv")).closest(
      "tr, [data-proposal-row]",
    )! as HTMLElement;
    const select = within(row).getByRole("combobox") as HTMLSelectElement;
    expect(select.value).toBe("rename"); // seeded default, before the manual override

    fireEvent.change(select, { target: { value: "dismiss" } });
    expect(select.value).toBe("dismiss");

    // Force the seeding effect to re-run against the SAME "pending" status
    // (no unmatched -> pending transition occurs) by toggling the history
    // view twice — createResource refetches a brand-new proposals array
    // containing the same still-"pending" row both times. A regression that
    // reset on every effect run (not just on a genuine transition) would
    // clobber the manual "dismiss" choice back to "rename" here.
    fireEvent.click(screen.getByText("Show history"));
    await waitFor(() =>
      expect(screen.getByText("Already.Matched.mkv")).toBeInTheDocument(),
    );
    fireEvent.click(screen.getByText("Show history"));
    await waitFor(() =>
      expect(screen.getByText("Already.Matched.mkv")).toBeInTheDocument(),
    );

    const selectAfter = within(
      screen.getByText("Already.Matched.mkv").closest("tr, [data-proposal-row]")! as HTMLElement,
    ).getByRole("combobox") as HTMLSelectElement;
    expect(selectAfter.value).toBe("dismiss");
  });
});

describe("Rename — Dismiss (single row)", () => {
  it("dismisses exactly one proposal", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([proposal({ id: 4, sourceName: "Dismiss.Me" })]);
      if (
        url.includes("/api/proposals/4/dismiss") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("Dismiss.Me");
    await runRowAction("Dismiss.Me", "dismiss");
    await waitFor(() =>
      expect(calls.some((c) => c.url.includes("/api/proposals/4/dismiss"))).toBe(
        true,
      ),
    );
  });
});

describe("Rename — mode-specific columns", () => {
  it("Movies shows a Year column and no Series/Adult-only columns", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({ id: 1, sourceName: "Movie.A", year: 1999 }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("Movie.A");

    expect(screen.getByText("Year")).toBeInTheDocument();
    expect(screen.getByText("1999")).toBeInTheDocument();
    expect(screen.queryByText("Season")).toBeNull();
    expect(screen.queryByText("Episode")).toBeNull();
    expect(screen.queryByText("Studio")).toBeNull();
    expect(screen.queryByText("PHash")).toBeNull();
  });

  it("Series shows Year/Season/Episode columns", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([]);
      if (url.includes("/api/modes/series/rename/proposals"))
        return jsonResponse([
          proposal({
            id: 2,
            sourceName: "Show.S02E05",
            title: "Some Show",
            year: 2015,
            seasonNumber: 2,
            episodeNumber: 5,
          }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    fireEvent.click(await screen.findByText("Series"));
    await screen.findByText("Show.S02E05");

    expect(screen.getByText("Year")).toBeInTheDocument();
    expect(screen.getByText("Season")).toBeInTheDocument();
    expect(screen.getByText("Episode")).toBeInTheDocument();
    expect(screen.getByText("2015")).toBeInTheDocument();
    expect(screen.getByText("2")).toBeInTheDocument();
    expect(screen.getByText("5")).toBeInTheDocument();
  });

  it('Series renders a range (e.g. "1-2") in the Episode column for a logical-episode-split proposal', async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([]);
      if (url.includes("/api/modes/series/rename/proposals"))
        return jsonResponse([
          proposal({
            id: 4,
            sourceName: "Show.S01E01-E02",
            title: "Some Show",
            seasonNumber: 1,
            episodeNumber: 1,
            extraEpisodeNumbers: [2],
          }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    fireEvent.click(await screen.findByText("Series"));
    await screen.findByText("Show.S01E01-E02");

    expect(screen.getByText("1-2")).toBeInTheDocument();
    expect(screen.queryByText("2")).toBeNull();
  });

  it("Adult shows Studio/Date/PHash columns, no Year/Season/Episode", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([]);
      if (url.includes("/api/modes/adult/rename/proposals"))
        return jsonResponse([
          proposal({
            id: 3,
            sourceName: "Studio.Scene",
            title: "Scene Title",
            year: 0,
            studio: "Brazzers",
            date: "2021-03-04",
            phash: "abcdef0123456789",
          }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    fireEvent.click(await screen.findByText("Adult"));
    await screen.findByText("Studio.Scene");

    expect(screen.getByText("Studio")).toBeInTheDocument();
    expect(screen.getByText("Date")).toBeInTheDocument();
    expect(screen.getByText("PHash")).toBeInTheDocument();
    expect(screen.getByText("Brazzers")).toBeInTheDocument();
    const hashCell = screen.getByTitle("abcdef0123456789");
    expect(hashCell.textContent).toBe("abcdef0123456789".slice(0, 12) + "…");
    expect(screen.queryByText("Year")).toBeNull();
  });

  it("shows current and proposed file names on adult cards", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/adult/rename/proposals"))
        return jsonResponse([
          proposal({
            id: 4,
            sourceName: "inbox.mp4",
            title: "Frisky Flatmates Chapter 2",
            studio: "MaxineX",
            date: "2024-10-23",
            phash: "abc",
          }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    fireEvent.click(await screen.findByText("Adult"));
    const row = (await screen.findByText("Current name")).closest(
      "[data-proposal-row]",
    )! as HTMLElement;
    expect(within(row).getByText("inbox.mp4")).toBeInTheDocument();
    expect(within(row).getByText("Proposed name")).toBeInTheDocument();
    expect(
      within(row).getByText(
        "MaxineX - Frisky Flatmates Chapter 2 (2024-10-23) [phash-abc].mp4",
      ),
    ).toBeInTheDocument();
  });
});

describe("Rename — Adult (dropdown; no Status column)", () => {
  it("has no Status/Give back; unmatched leaves placeholder (Rename disabled)", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([]);
      if (url.includes("/api/modes/adult/rename/proposals"))
        return jsonResponse([
          proposal({
            id: 21,
            status: "unmatched",
            sourceName: "Studio - Unidentified Scene",
            title: "",
            reason: "no confident match",
          }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    fireEvent.click(await screen.findByText("Adult"));
    await screen.findByText("Studio - Unidentified Scene");

    expect(screen.queryByText("Status")).toBeNull();
    const row = screen.getByText("Studio - Unidentified Scene").closest("tr, [data-proposal-row]")! as HTMLElement;
    const select = within(row).getByRole("combobox") as HTMLSelectElement;
    expect(select.value).toBe("");
    expect(within(select).queryByRole("option", { name: "Give back" })).toBeNull();
    expect(within(select).queryByRole("option", { name: "Apply" })).toBeNull();
    expect(within(select).getByRole("option", { name: "Rename" })).toBeDisabled();
    expect(within(select).getByRole("option", { name: "Search" })).toBeDisabled();
    expect(within(select).getByRole("option", { name: "Dismiss" })).not.toBeDisabled();
    expect(
      within(row).queryByRole("button", {
        name: "Apply selected action for Studio - Unidentified Scene",
      }),
    ).toBeNull();
  });
});

// Wave 4 (AC5) — SearchTakeover wired into the row action dropdown.
// .omc/plans/autopilot-impl-cross-mode-move.md §6.3.
describe("Rename — Move to another section (row action set, AC5)", () => {
  it("offers exactly the two OTHER modes' Move actions, never the current one", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([proposal({ id: 1, sourceName: "Movie.A" })]);
      if (url.includes("/api/modes/series/rename/proposals"))
        return jsonResponse([
          proposal({ id: 2, sourceName: "Show.A", title: "Show A" }),
        ]);
      if (url.includes("/api/modes/adult/rename/proposals"))
        return jsonResponse([
          proposal({ id: 3, sourceName: "Scene.A", title: "Scene A" }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);

    const assertOptions = async (
      sourceName: string,
      expectedLabels: string[],
      excludedLabel: string,
    ) => {
      const row = (await screen.findByText(sourceName)).closest("tr, [data-proposal-row]")! as HTMLElement;
      const select = within(row).getByRole("combobox");
      for (const label of expectedLabels) {
        expect(
          within(select).getByRole("option", { name: label }),
        ).toBeInTheDocument();
      }
      expect(
        within(select).queryByRole("option", { name: excludedLabel }),
      ).toBeNull();
    };

    await assertOptions(
      "Movie.A",
      ["Move to Series", "Move to Adult"],
      "Move to Movies",
    );

    fireEvent.click(screen.getByText("Series"));
    await assertOptions(
      "Show.A",
      ["Move to Movies", "Move to Adult"],
      "Move to Series",
    );

    fireEvent.click(screen.getByText("Adult"));
    await assertOptions(
      "Scene.A",
      ["Move to Movies", "Move to Series"],
      "Move to Adult",
    );
  });

  it("selecting a Move action never dismisses the proposal", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([proposal({ id: 1, sourceName: "Movie.A" })]);
      if (url.includes("/api/modes/series/rename/proposals"))
        return jsonResponse([
          proposal({ id: 2, sourceName: "Show.A", title: "Show A" }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);

    // Movies mode — both other-mode move actions.
    await runRowAction("Movie.A", "move:series");
    expect(
      await screen.findByText(/Move “Movie\.A” to another section/),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByLabelText("Back to list"));

    await runRowAction("Movie.A", "move:adult");
    expect(
      await screen.findByText(/Move “Movie\.A” to another section/),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByLabelText("Back to list"));

    // Series mode — the third move id, move:movies.
    fireEvent.click(screen.getByText("Series"));
    await runRowAction("Show.A", "move:movies");
    expect(
      await screen.findByText(/Move “Show\.A” to another section/),
    ).toBeInTheDocument();

    expect(calls.some((c) => c.url.includes("/dismiss"))).toBe(false);
  });

  // D-1a commit-403 (frontend) — .omc/plans/autopilot-impl-cross-mode-move.md §7's test
  // table. SearchTakeover.test.tsx already covers this branch at the
  // component level, in isolation. This test proves the SAME behavior
  // through Rename's real row-action wiring (runRowAction -> setMoveFor ->
  // <SearchTakeover>), not a standalone panel render — i.e. that the wiring
  // itself surfaces the commit-403 UX, not just that the panel component can
  // theoretically do it. Adult->Movies is used deliberately: the search
  // (movies/tmdb-search) is NOT gated by the Adult lock and succeeds, while
  // the commit is gated because the proposal's own (source) mode is Adult —
  // the one case the section-agnostic search-403 test structurally can't
  // reach, since there the search itself is what fails.
  it("commit-403 (Adult PIN-locked) is a separate branch from search-403 through the real row-action wiring — Adult→Movies, where the search succeeds", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([]);
      if (url.includes("/api/modes/adult/rename/proposals"))
        return jsonResponse([
          proposal({
            id: 9,
            sourceName: "Scene.To.Move",
            title: "Scene To Move",
          }),
        ]);
      if (url.includes("/api/modes/movies/tmdb-search"))
        return jsonResponse([
          tmdbItem({ id: 555, title: "Target Movie", releaseDate: "2020-01-01" }),
        ]);
      if (url.includes("/api/proposals/9/move-mode")) {
        return new Response(
          JSON.stringify({
            error: "section locked",
            code: "section_locked",
            section: "adult-content",
          }),
          { status: 403, headers: { "Content-Type": "application/json" } },
        );
      }
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    fireEvent.click(await screen.findByText("Adult"));
    await runRowAction("Scene.To.Move", "move:movies");
    expect(
      await screen.findByText(/Move “Scene\.To\.Move” to another section/),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByText("Search"));
    fireEvent.click(await screen.findByLabelText("Use Target Movie"));

    // (a) shows the fixed Adult commit-403 copy.
    expect(
      await screen.findByText("Adult is PIN-locked — unlock to continue"),
    ).toBeInTheDocument();
    // (b) never the search-blocked copy — the search succeeded here.
    expect(screen.queryByText(/unlock it to search/)).toBeNull();
    // (c) the panel stays open with the picked candidate still selected and
    // clickable — unlock-and-retry is one click.
    expect(
      screen.getByText(/Move “Scene\.To\.Move” to another section/),
    ).toBeInTheDocument();
    expect(screen.getByText(/Target Movie/)).toBeInTheDocument();
    expect(screen.getByLabelText("Use Target Movie")).toBeInTheDocument();
    // (d) never calls onDone: onDone would setMoveFor(null) (unmounting the
    // panel, contradicting (c) above) AND refetch() the Adult proposal list
    // — confirm that second GET never fired.
    expect(
      calls.filter((c) => c.url.includes("/api/modes/adult/rename/proposals"))
        .length,
    ).toBe(1);
  });
});

// Wave 5 — the full-page search takeover (.omc/plans/autopilot-impl.md §8.5,
// rows N1/N2/N4/N4b/N5). What these add over the blocks above is CONTAINER
// semantics and scroll state, not search/commit behaviour:
//
//   N1/N2  the takeover REPLACES the queue in place — the row table is
//          genuinely un-created, not merely covered by a `fixed inset-0`
//          backdrop the way the deleted RepickPanel/MoveModePanel were.
//   N5     a successful commit returns to the list and refetches EXACTLY once.
//   N4/N4b the scroll restore. Rename's copy of this mechanism is an
//          independent implementation of Dedup's, not a shared module, so it
//          can silently drift out of sync with zero coverage catching it.
//
// `/rename/proposals?` (with the ?) is the page GET throughout: the bare
// `/rename/proposals` prefix also matches `/rename/proposals/pending-ids`,
// which Apply all fires and which must never be counted as a page fetch.
const pageGets = (calls: Call[], mode = "movies") =>
  calls.filter((c) => c.url.includes(`/api/modes/${mode}/rename/proposals?`))
    .length;

describe("Rename — search takeover container semantics (N1, N2, N5)", () => {
  it("N1: Re-pick replaces the row table in place — the proposal row is genuinely gone, not overlaid", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({ id: 70, sourceName: "Vanish.Me.mkv", title: "Vanish Me" }),
        ]);
      // Re-pick auto-searches on mount (autoSearch={kind === "repick"}), so
      // this branch is required — without it the handler's trailing throw
      // would surface an error state instead of the takeover.
      if (url.includes("/api/modes/movies/tmdb-search"))
        return jsonResponse([tmdbItem({ id: 800, title: "Real Movie" })]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("Vanish.Me.mkv");

    await runRowAction("Vanish.Me.mkv", "repick");
    expect(
      await screen.findByText(/Search for a new match for “Vanish\.Me\.mkv”/),
    ).toBeInTheDocument();

    // The whole queue subtree is un-created, not hidden: the proposal's own
    // source cell, the table header, and the queue chrome are all absent.
    // Under the deleted inline RepickPanel every one of these would still be
    // in the document, merely painted over — which is what this row rules out.
    //
    // The source-name negative is EXACT-MATCH on purpose, and must stay that
    // way: queryByText's string form matches a whole text node, and the
    // takeover's own heading renders `Search for a new match for “Vanish.Me.mkv”` — the
    // name IS still on screen, just not as its own cell. "Strengthening" this
    // to a /Vanish\.Me\.mkv/ regex fails on the heading and looks like a
    // regression that is not one. "Source"/"Scan"/"Page size" below are the
    // load-bearing negatives: those are gone outright.
    expect(screen.queryByText("Vanish.Me.mkv")).toBeNull();
    expect(document.querySelectorAll("[data-proposal-row]")).toHaveLength(0);
    expect(screen.queryByText("Scan")).toBeNull();
    expect(screen.queryByLabelText("Page size")).toBeNull();
    // ModeTabs is deliberately NOT gone — it lives outside RenameQueue. This
    // positive is what makes the negatives above mean "replaced in place"
    // rather than "the screen blew up and rendered nothing".
    expect(screen.getByText("Movies")).toBeInTheDocument();
  });

  it("N2: Move replaces the row table in place too — same in-place replacement, different entry point", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({ id: 71, sourceName: "Move.Me.mkv", title: "Move Me" }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("Move.Me.mkv");

    await runRowAction("Move.Me.mkv", "move:series");
    expect(
      await screen.findByText(/Move “Move\.Me\.mkv” to another section/),
    ).toBeInTheDocument();

    // Exact-match on purpose — see N1's comment; the move heading also
    // contains this name, so a regex here would fail for the wrong reason.
    expect(screen.queryByText("Move.Me.mkv")).toBeNull();
    expect(document.querySelectorAll("[data-proposal-row]")).toHaveLength(0);
    expect(screen.queryByText("Scan")).toBeNull();
    expect(screen.queryByLabelText("Page size")).toBeNull();
    expect(screen.getByText("Movies")).toBeInTheDocument();
  });

  it("N5: a successful commit returns to the list and refetches the proposals exactly once", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({ id: 72, sourceName: "Commit.Me.mkv", title: "Commit Me" }),
        ]);
      // Series search queries both catalogs — see the Series Re-pick test above.
      if (url.includes("/api/modes/movies/tmdb-search")) return jsonResponse([]);
      if (url.includes("/api/modes/series/tmdb-search"))
        return jsonResponse([
          tmdbItem({ id: 901, title: "Target Show", releaseDate: "2019-01-01" }),
        ]);
      if (url.includes("/discover/detail")) return jsonResponse({ seasons: [] });
      if (
        url.includes("/api/proposals/72/move-mode") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("Commit.Me.mkv");
    const baseline = pageGets(calls);

    await runRowAction("Commit.Me.mkv", "move:series");
    await screen.findByText(/Move “Commit\.Me\.mkv” to another section/);

    fireEvent.click(screen.getByText("Search"));
    fireEvent.click(await screen.findByLabelText("Use Target Show"));
    // Series target with zero seasons → the picker degrades and the
    // show-level escape hatch is the commit.
    fireEvent.click(await screen.findByText("Use show-level match only"));

    await waitFor(() =>
      expect(calls.some((c) => c.url.includes("/move-mode"))).toBe(true),
    );

    // Back on the list: the takeover is unmounted and the table is rendered.
    await waitFor(() =>
      expect(screen.queryByLabelText("Back to list")).toBeNull(),
    );
    expect(await screen.findByText("Commit.Me.mkv")).toBeInTheDocument();
    expect(document.querySelectorAll("[data-proposal-row]")).toHaveLength(1);

    // EXACTLY one refetch — not zero (a stale list still showing a proposal
    // that has moved to another section) and not two (onDone's refetch racing
    // a second trigger, which would double every commit's server load).
    expect(pageGets(calls)).toBe(baseline + 1);
  });
});

// renderInMain mounts the screen inside a real <main>, which is what Rename's
// scroll-restore actually targets: it reaches its host with
// rootEl.closest("main") (AppShell's <main> is the app's only scroll region —
// the shell root is h-screen overflow-hidden, so window.scrollY is always 0).
// Every other test in this file renders hostless on purpose, exercising the
// documented null-host degrade path.
const renderInMain = (): HTMLElement => {
  const main = document.createElement("main");
  document.body.appendChild(main);
  render(() => <Rename />, { container: main });
  return main;
};

describe("Rename — takeover scroll restore (N4, N4b)", () => {
  // Rename-N4 — the back-arrow path. No refetch happens here, so the list
  // re-renders synchronously from the already-resolved page resource.
  //
  // Move (not Re-pick) is the entry point deliberately: Re-pick auto-searches
  // on mount, which would add a GET and muddy the "Back refetches nothing"
  // assertion at the end.
  it("N4: backing out restores the list scroll offset, with the page size and the table untouched and no extra GET", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({ id: 73, sourceName: "Scroll.Me.mkv", title: "Scroll Me" }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    const main = renderInMain();
    await screen.findByText("Scroll.Me.mkv");

    // A NON-default page size, so the assertion below proves the restore left
    // the operator's list configuration alone rather than resetting it.
    fireEvent.change(screen.getByLabelText("Page size"), {
      target: { value: "100" },
    });
    await waitFor(() =>
      expect(calls.some((c) => c.url.includes("limit=100"))).toBe(true),
    );
    await screen.findByText("Scroll.Me.mkv");
    const baseline = pageGets(calls);

    // Set BEFORE the row action fires: openTakeover reads the host's scrollTop
    // and only then flips the signal.
    main.scrollTop = 400;
    await runRowAction("Scroll.Me.mkv", "move:series");
    await screen.findByText(/Move “Scroll\.Me\.mkv” to another section/);

    // MANDATORY, and the single line that stops this test being vacuous.
    // jsdom has no layout engine: removing the scroll container's children
    // never clamps scrollTop the way a real browser does, so "set 400, open,
    // close, assert 400" passes IDENTICALLY whether the restore works or is
    // absent entirely. Zeroing here simulates the browser's layout collapse,
    // which is what makes the final assertion mean "the restore WROTE this",
    // not "nothing disturbed it".
    main.scrollTop = 0;

    fireEvent.click(screen.getByLabelText("Back to list"));
    // The restore is deferred by a queueMicrotask (the effect runs before Solid
    // has flushed the re-inserted rows), hence waitFor rather than a bare read.
    await waitFor(() => expect(main.scrollTop).toBe(400));

    expect(await screen.findByText("Scroll.Me.mkv")).toBeInTheDocument();
    expect(document.querySelectorAll("[data-proposal-row]")).toHaveLength(1);
    expect(screen.getByText("Scroll Me")).toBeInTheDocument();
    expect(
      (
        screen.getByLabelText("Page size") as HTMLSelectElement
      ).value,
    ).toBe("100");
    // The back path refetches nothing — the list comes back off the resolved
    // resource, which is also why the restore can fire immediately here.
    expect(pageGets(calls)).toBe(baseline);
  });

  // Rename-N4b — the commit path, and the harder of the two. The restore must
  // fire only AFTER page.loading clears; a restore fired straight after
  // setTakeover(null) lands on the "Loading…" DOM, where a real browser clamps
  // it to ~0 and the offset is silently lost. The mid-flight `=== 0` assertion
  // below is the executable proof that Rename.tsx's onDone really does
  // batch(refetch-then-setTakeover) — see the walkthrough in that comment.
  it("N4b: on a successful commit the restore is held until the refetch lands — still 0 mid-flight, 400 only after", async () => {
    let releaseRefetch!: () => void;
    let pageGetCount = 0;
    const row = proposal({
      id: 74,
      sourceName: "Commit.Scroll.mkv",
      title: "Commit Scroll",
    });

    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/rename/proposals?")) {
        pageGetCount += 1;
        if (pageGetCount === 1) return jsonResponse([row]);
        // The post-commit refetch is held PENDING and resolved by hand, so the
        // test can observe the in-flight window rather than only the end state.
        return new Promise<Response>((resolve) => {
          releaseRefetch = () => resolve(jsonResponse([row]));
        });
      }
      // Series search queries both catalogs — see the Series Re-pick test above.
      if (url.includes("/api/modes/movies/tmdb-search")) return jsonResponse([]);
      if (url.includes("/api/modes/series/tmdb-search"))
        return jsonResponse([
          tmdbItem({ id: 902, title: "Target Show", releaseDate: "2019-01-01" }),
        ]);
      if (url.includes("/discover/detail")) return jsonResponse({ seasons: [] });
      if (
        url.includes("/api/proposals/74/move-mode") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    const main = renderInMain();
    await screen.findByText("Commit.Scroll.mkv");

    main.scrollTop = 400;
    await runRowAction("Commit.Scroll.mkv", "move:series");
    await screen.findByText(/Move “Commit\.Scroll\.mkv” to another section/);

    // Same mandatory zeroing as N4 — see that test's comment.
    main.scrollTop = 0;

    fireEvent.click(screen.getByText("Search"));
    fireEvent.click(await screen.findByLabelText("Use Target Show"));
    fireEvent.click(await screen.findByText("Use show-level match only"));

    await waitFor(() =>
      expect(
        calls.some((c) => c.url.includes("/api/proposals/74/move-mode")),
      ).toBe(true),
    );
    // The takeover is closed and the refetch is in flight: the queue shows the
    // Loading… fallback, NOT the real table.
    await waitFor(() =>
      expect(screen.getByText("Loading…")).toBeInTheDocument(),
    );

    // THE POINT OF THIS TEST. A plain synchronous read, never wrapped in
    // waitFor — a retrying matcher on a negative proves nothing. If onDone
    // reverted to `setTakeover(null); void refetch();` (unbatched, setter
    // first), the restore effect would observe page.loading === false, queue
    // `scrollTop = 400` via queueMicrotask, and clear savedScrollTop — and
    // microtasks drain before waitFor's next poll, so this line would read 400
    // and fail. Under the shipped batch(refetch-then-close) ordering the effect
    // observes one consistent state with loading already true, returns early,
    // and the value is still held.
    expect(main.scrollTop).toBe(0);
    expect(screen.queryByText("Commit.Scroll.mkv")).toBeNull();

    releaseRefetch();

    // Only now, with the real table back, does the restore fire.
    expect(await screen.findByText("Commit.Scroll.mkv")).toBeInTheDocument();
    await waitFor(() => expect(main.scrollTop).toBe(400));
    expect(document.querySelectorAll("[data-proposal-row]")).toHaveLength(1);
    expect(screen.getByText("Commit Scroll")).toBeInTheDocument();
  });
});
