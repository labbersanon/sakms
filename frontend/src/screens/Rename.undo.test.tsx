// Rename Undo — the "Recently Applied" section and its Undo button
// (deep-interview-rename-undo, frontend pass §5). Own file, same precedent and
// same harness conventions as Rename.delete.test.tsx / Rename.repick.test.tsx:
// one feature area, one file.
//
// Three of these tests guard properties that a positive-only assertion would
// pass straight through, so each asserts an ABSENCE as well as a presence:
//
//   - "a drifted undo reads differently from a clean one" — a UI that always
//     rendered "Undo complete" would satisfy "the API was called" and still be
//     exactly the silent-looking success the spec's best-effort policy forbids.
//   - "a 409 keeps the row and a 404 clears it" — the two are one `catch` apart
//     in Rename.tsx, and getting them backwards either hides a valid retry path
//     or leaves a permanently dead row no click can clear.
//   - "the list refetches after Apply too" — covering only the single-row Apply
//     and forgetting "Apply Selected" passes casual manual testing and is still
//     wrong, so both paths are asserted here.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@solidjs/testing-library";
import type { Proposal, RecentlyAppliedEntry } from "@dto";
import { Rename, formatAppliedAt } from "./Rename";

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

// textError mirrors the backend exactly: internal/api/rename_undo.go refuses
// through http.Error, which writes PLAIN TEXT (plus a trailing newline), not
// JSON. Stubbing these as JSON would let a client.ts message-extraction
// regression pass unnoticed.
const textError = (message: string, status: number): Response =>
  new Response(message + "\n", { status });

const proposal = (over: Partial<Proposal>): Proposal => ({
  id: 1,
  status: "pending",
  sourceName: "Some.Movie.2021.1080p.mkv",
  sourcePath: "/movies/Some.Movie.2021.1080p.mkv",
  rootFolderPath: "/movies",
  title: "Some Movie",
  year: 2021,
  reason: "",
  draftId: "",
  ...over,
});

const entry = (over: Partial<RecentlyAppliedEntry>): RecentlyAppliedEntry => ({
  undoId: 10,
  proposalId: 1,
  mode: "movies",
  appliedAt: "2026-08-10 09:15:00",
  sourceName: "Some.Movie.2021.1080p.mkv",
  title: "Some Movie",
  viaAlternateFold: false,
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

// Same shape as Rename.test.tsx's stubFetch (organize/events + pending-ids
// boilerplate, array -> ProposalPage auto-wrap). recently-applied is NOT
// short-circuited here — every test in this file is about that route.
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

const recentGets = (calls: Call[]) =>
  calls.filter(
    (c) => c.method === "GET" && c.url.includes("/rename/recently-applied"),
  );
const proposalGets = (calls: Call[]) =>
  calls.filter(
    (c) =>
      c.method === "GET" &&
      c.url.includes("/rename/proposals") &&
      !c.url.includes("pending-ids"),
  );
const undoPosts = (calls: Call[]) =>
  calls.filter((c) => c.method === "POST" && /\/undo$/.test(c.url));

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("formatAppliedAt", () => {
  it("formats legacy space-separated timestamps", () => {
    expect(formatAppliedAt("2026-08-10 09:15:00")).toBe(
      "Aug 10, 2026, 9:15 AM",
    );
  });

  it("formats RFC3339 nano timestamps from the API", () => {
    const raw = "2026-08-12T02:14:09.924763727Z";
    expect(formatAppliedAt(raw)).toBe(
      new Date(raw).toLocaleString("en-US", {
        month: "short",
        day: "numeric",
        year: "numeric",
        hour: "numeric",
        minute: "2-digit",
        hour12: true,
      }),
    );
  });
});

describe("Rename — Recently Applied list", () => {
  it("renders one row per entry, with the applied timestamp", async () => {
    stubFetch((url) => {
      if (url.includes("/rename/recently-applied"))
        return jsonResponse([
          entry({ undoId: 1, proposalId: 11, sourceName: "One.mkv", title: "One" }),
          entry({ undoId: 2, proposalId: 12, sourceName: "Two.mkv", title: "Two" }),
        ]);
      if (url.includes("/rename/proposals")) return jsonResponse([]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    // Wait on a ROW, not the static "Recently applied" heading — that heading
    // renders before the resource resolves, so keying on it races the fetch.
    const row = await screen.findByText("One.mkv");
    const list = row.closest("ul")!;
    expect(within(list).getByText("One.mkv")).toBeInTheDocument();
    expect(within(list).getByText("Two.mkv")).toBeInTheDocument();
    expect(
      within(list).getAllByText(/Applied Aug 10, 2026, 9:15 AM/),
    ).toHaveLength(2);
    expect(
      within(list).getByRole("button", { name: "Undo One.mkv" }),
    ).toBeInTheDocument();
  });

  // A Solid resource RE-THROWS on read once its fetcher errored, so a naive
  // `recent() ?? []` would throw mid-render and blank the entire screen — the
  // same failure shape CLAUDE.md records for GrabDialog's sibling <Show>
  // blocks. Asserting the PROPOSAL TABLE survives is the point; asserting only
  // that the error line appears would pass against a screen that rendered
  // nothing else.
  it("keeps the proposal table alive when the list fetch 500s, and says so", async () => {
    stubFetch((url) => {
      if (url.includes("/rename/recently-applied"))
        return new Response("undo archive unavailable", { status: 500 });
      if (url.includes("/rename/proposals"))
        return jsonResponse([
          proposal({ id: 21, sourceName: "Survivor.mkv", title: "Survivor" }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    expect(await screen.findByText("Survivor.mkv")).toBeInTheDocument();
    expect(
      await screen.findByText(/Could not load recently applied/),
    ).toBeInTheDocument();
  });

  it("shows the alternate-fold caveat on a folded row and NOT on a normal one", async () => {
    stubFetch((url) => {
      if (url.includes("/rename/recently-applied"))
        return jsonResponse([
          entry({
            undoId: 1,
            proposalId: 11,
            sourceName: "Folded.mkv",
            title: "Folded",
            viaAlternateFold: true,
          }),
          entry({
            undoId: 2,
            proposalId: 12,
            sourceName: "Plain.mkv",
            title: "Plain",
          }),
        ]);
      if (url.includes("/rename/proposals")) return jsonResponse([]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    const foldedRow = (await screen.findByText("Folded.mkv")).closest("li")!;
    const caveat = within(foldedRow).getByText(/file won’t be moved back on undo/);
    expect(caveat).toBeInTheDocument();
    // Announced as a DESCRIPTION of the Undo button, not folded into its name
    // (which every other assertion in this file queries by).
    expect(
      within(foldedRow).getByRole("button", { name: "Undo Folded.mkv" }),
    ).toHaveAttribute("aria-describedby", caveat.id);

    const plainRow = screen.getByText("Plain.mkv").closest("li")!;
    expect(
      within(plainRow).queryByText(/file won’t be moved back on undo/),
    ).toBeNull();
  });
});

describe("Rename — Undo button", () => {
  it("calls the endpoint and refetches BOTH lists on success", async () => {
    let undone = false;
    const calls = stubFetch((url, init) => {
      if (url.includes("/rename/recently-applied"))
        return jsonResponse(
          undone ? [] : [entry({ proposalId: 42, sourceName: "Undo.Me.mkv" })],
        );
      if (url.includes("/rename/proposals"))
        return jsonResponse(
          undone
            ? [proposal({ id: 42, sourceName: "Undo.Me.mkv", title: "Undo Me" })]
            : [],
        );
      if (url.includes("/api/proposals/42/undo") &&
        (init?.method ?? "").toUpperCase() === "POST") {
        undone = true;
        return jsonResponse({
          proposalId: 42,
          mode: "movies",
          fileRestored: true,
          fileMessage: "moved back to /inbox/Undo.Me.mkv",
          restoredPath: "/inbox/Undo.Me.mkv",
          driftDetected: false,
          rowsReverted: 1,
          giveBackNotRetractable: false,
        });
      }
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByRole("button", { name: "Undo Undo.Me.mkv" });
    const recentBefore = recentGets(calls).length;
    const proposalsBefore = proposalGets(calls).length;

    fireEvent.click(screen.getByRole("button", { name: "Undo Undo.Me.mkv" }));

    await waitFor(() => expect(undoPosts(calls)).toHaveLength(1));
    // BOTH lists refetch: the entry is consumed, and the proposal is Pending
    // again and belongs back in the main table.
    await waitFor(() =>
      expect(recentGets(calls).length).toBeGreaterThan(recentBefore),
    );
    await waitFor(() =>
      expect(proposalGets(calls).length).toBeGreaterThan(proposalsBefore),
    );
    // The entry is gone from Recently Applied…
    await waitFor(() =>
      expect(
        screen.queryByRole("button", { name: "Undo Undo.Me.mkv" }),
      ).toBeNull(),
    );
    // …and the proposal is back in the live queue.
    await waitFor(() =>
      expect(screen.getByText("Undo Me")).toBeInTheDocument(),
    );
    expect(screen.getByText(/Undo complete/)).toBeInTheDocument();
  });

  it("renders a drifted / not-restored 200 differently from a clean one", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/rename/recently-applied"))
        return jsonResponse([entry({ proposalId: 7, sourceName: "Drifty.mkv" })]);
      if (url.includes("/rename/proposals")) return jsonResponse([]);
      if (url.includes("/api/proposals/7/undo") &&
        (init?.method ?? "").toUpperCase() === "POST")
        return jsonResponse({
          proposalId: 7,
          mode: "movies",
          fileRestored: false,
          fileMessage: "the applied file is no longer at its expected path",
          driftDetected: true,
          driftWarnings: ["the library row changed since this Apply"],
          rowsReverted: 1,
          giveBackNotRetractable: false,
        });
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    fireEvent.click(await screen.findByRole("button", { name: "Undo Drifty.mkv" }));

    await waitFor(() => expect(undoPosts(calls)).toHaveLength(1));
    await screen.findByText(/Undo completed with warnings/);
    expect(
      screen.getByText(/the applied file is no longer at its expected path/),
    ).toBeInTheDocument();
    expect(
      screen.getByText("the library row changed since this Apply"),
    ).toBeInTheDocument();
    // The clean-success wording must NOT be what an operator sees here.
    expect(screen.queryByText(/^Undo complete —/)).toBeNull();
  });

  it("states plainly that an Adult give-back submission cannot be retracted", async () => {
    stubFetch((url, init) => {
      if (url.includes("/rename/recently-applied"))
        return jsonResponse([entry({ proposalId: 9, sourceName: "Scene.mp4" })]);
      if (url.includes("/rename/proposals")) return jsonResponse([]);
      if (url.includes("/api/proposals/9/undo") &&
        (init?.method ?? "").toUpperCase() === "POST")
        return jsonResponse({
          proposalId: 9,
          mode: "adult",
          fileRestored: true,
          fileMessage: "moved back to /inbox/Scene.mp4",
          driftDetected: false,
          rowsReverted: 1,
          giveBackNotRetractable: true,
        });
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    fireEvent.click(await screen.findByRole("button", { name: "Undo Scene.mp4" }));
    await screen.findByText(/that submission cannot be retracted/);
    // A factual note, not an alarm: the undo itself still reads as complete.
    expect(screen.getByText(/Undo complete/)).toBeInTheDocument();
  });

  it("surfaces a 409's exact backend message and KEEPS the row (no refetch)", async () => {
    const message =
      "proposal 88 is already queued for \"/inbox/Clash.mkv\" — undo would put two live proposals at the same source path; dismiss or apply that one first";
    const calls = stubFetch((url, init) => {
      if (url.includes("/rename/recently-applied"))
        return jsonResponse([entry({ proposalId: 5, sourceName: "Clash.mkv" })]);
      if (url.includes("/rename/proposals")) return jsonResponse([]);
      if (url.includes("/api/proposals/5/undo") &&
        (init?.method ?? "").toUpperCase() === "POST")
        return textError(message, 409);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByRole("button", { name: "Undo Clash.mkv" });
    const recentBefore = recentGets(calls).length;

    fireEvent.click(screen.getByRole("button", { name: "Undo Clash.mkv" }));

    // Verbatim, not rewrapped or genericized.
    await screen.findByText(message);
    await waitFor(() => expect(undoPosts(calls)).toHaveLength(1));
    // The entry is STILL undoable — the operator resolves the colliding
    // proposal and retries, so the row must not be cleared.
    expect(
      screen.getByRole("button", { name: "Undo Clash.mkv" }),
    ).toBeInTheDocument();
    expect(recentGets(calls).length).toBe(recentBefore);
  });

  it("refetches the list on a 404, clearing a row whose proposal has moved on", async () => {
    let stale = true;
    const calls = stubFetch((url, init) => {
      if (url.includes("/rename/recently-applied"))
        return jsonResponse(
          stale ? [entry({ proposalId: 6, sourceName: "Ghost.mkv" })] : [],
        );
      if (url.includes("/rename/proposals")) return jsonResponse([]);
      if (url.includes("/api/proposals/6/undo") &&
        (init?.method ?? "").toUpperCase() === "POST") {
        stale = false;
        return textError("nothing to undo for proposal 6", 404);
      }
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByRole("button", { name: "Undo Ghost.mkv" });
    const recentBefore = recentGets(calls).length;

    fireEvent.click(screen.getByRole("button", { name: "Undo Ghost.mkv" }));

    await screen.findByText("nothing to undo for proposal 6");
    await waitFor(() =>
      expect(recentGets(calls).length).toBeGreaterThan(recentBefore),
    );
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "Undo Ghost.mkv" })).toBeNull(),
    );
  });
});

// resetOnModeChange clears the undo banner, but the undo's own async
// continuation runs LATER and would happily write into the mode the operator
// switched to. Asserting the banner is ABSENT is the whole test — a positive-
// only "the new mode's list rendered" assertion passes either way.
describe("Rename — an in-flight Undo does not leak across a mode switch", () => {
  it("drops its result when the mode changed while the request was in flight", async () => {
    let releaseUndo!: (r: Response) => void;
    const undoInFlight = new Promise<Response>((res) => {
      releaseUndo = res;
    });

    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/rename/recently-applied"))
        return jsonResponse([entry({ proposalId: 55, sourceName: "Slow.mkv" })]);
      if (url.includes("/api/modes/series/rename/recently-applied"))
        return jsonResponse([]);
      if (url.includes("/rename/proposals")) return jsonResponse([]);
      if (url.includes("/api/proposals/55/undo") &&
        (init?.method ?? "").toUpperCase() === "POST")
        return undoInFlight;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    fireEvent.click(await screen.findByRole("button", { name: "Undo Slow.mkv" }));
    await waitFor(() => expect(undoPosts(calls)).toHaveLength(1));

    // The operator switches modes before the undo comes back.
    fireEvent.click(screen.getByText("Series"));
    await waitFor(() =>
      expect(screen.getByText("Nothing to undo yet.")).toBeInTheDocument(),
    );

    releaseUndo(
      jsonResponse({
        proposalId: 55,
        mode: "movies",
        fileRestored: true,
        fileMessage: "moved back to /inbox/Slow.mkv",
        driftDetected: false,
        rowsReverted: 1,
        giveBackNotRetractable: false,
      }),
    );
    // Drain the continuation BEFORE asserting absence, and drain it with real
    // macrotasks rather than `await Promise.resolve()`. This is the difference
    // between a test that catches the regression and one that passes
    // vacuously: the chain from the released fetch to setUndoResult is several
    // microtasks long (fetch resolve -> Response.json() -> the await -> the
    // setter -> Solid's render), so a single microtask tick asserts absence
    // before the leak has had a chance to happen. Verified by removing the
    // guard: with a microtask flush this test still passed; with these two
    // macrotask flushes it fails, which is what it is for.
    await new Promise((r) => setTimeout(r, 0));
    await new Promise((r) => setTimeout(r, 0));
    expect(undoPosts(calls)).toHaveLength(1);

    expect(screen.queryByText(/Undo complete/)).toBeNull();
    expect(screen.queryByText(/moved back to \/inbox\/Slow\.mkv/)).toBeNull();
    // Still Series' own (empty) list, not Movies' row resurrected into it.
    expect(screen.getByText("Nothing to undo yet.")).toBeInTheDocument();
  });
});

describe("Rename — Recently Applied refreshes after Apply, not only after Undo", () => {
  it("refetches after a single-row Apply", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/rename/recently-applied")) return jsonResponse([]);
      if (url.includes("/rename/proposals"))
        return jsonResponse([
          proposal({ id: 3, sourceName: "Fresh.mkv", title: "Fresh" }),
        ]);
      if (url.includes("/api/proposals/3/apply") &&
        (init?.method ?? "").toUpperCase() === "POST")
        return jsonResponse({});
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    const row = (await screen.findByText("Fresh.mkv")).closest("tr")!;
    const before = recentGets(calls).length;
    fireEvent.click(
      within(row).getByRole("button", {
        name: "Apply selected action for Fresh.mkv",
      }),
    );

    await waitFor(() =>
      expect(recentGets(calls).length).toBeGreaterThan(before),
    );
  });

  it("refetches after the batch Apply Selected path too", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/rename/recently-applied")) return jsonResponse([]);
      if (url.includes("/rename/proposals"))
        return jsonResponse([
          proposal({ id: 4, sourceName: "Batched.mkv", title: "Batched" }),
        ]);
      if (url.includes("/api/proposals/apply-batch") &&
        (init?.method ?? "").toUpperCase() === "POST")
        return jsonResponse({ results: [{ id: 4, ok: true }] });
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("Batched.mkv");
    const before = recentGets(calls).length;

    fireEvent.click(screen.getByRole("button", { name: "Apply all" }));
    fireEvent.click(await screen.findByRole("button", { name: "Confirm" }));

    await waitFor(() =>
      expect(
        calls.some((c) => c.url.includes("/api/proposals/apply-batch")),
      ).toBe(true),
    );
    await waitFor(() =>
      expect(recentGets(calls).length).toBeGreaterThan(before),
    );
  });
});
