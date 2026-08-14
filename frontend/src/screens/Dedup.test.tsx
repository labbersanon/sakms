// Stage 3 Dedup UI tests — the staged scan→propose→apply DEDUPLICATION queue,
// per mode, plus the bounded bulk-apply exception.
//
// Dedup is structurally different from Rename/Purge: a proposal is a GROUP of
// candidate files, and Apply carries a body ({keepIndex} or {keepAll}) picking
// which file to keep. These tests pin the two correctness traps a stubbed fetch
// is the only place they CAN'T be caught end-to-end but CAN be pinned at the
// body-shape level:
//   1. keepIndex is the array index of the SELECTED radio, in received order —
//      re-picking a non-winner sends that candidate's index, not the winner's.
//   2. keepIndex === 0 must still be sent (falsy-guard trap) — picking the
//      index-0 candidate when it isn't the winner sends {keepIndex: 0}, never an
//      empty body (which would let the backend delete the operator's keeper).
//
// Bulk apply (a deliberate, documented reversal of the original
// no-apply-everything rule — see ROADMAP.md and the top-level CLAUDE.md) adds an
// opt-in multi-select of Pending groups, applied in ONE apply-batch. Its own
// correctness trap: a batched group sends keepIndex ONLY when the operator
// changed that group's radio (an explicit keepSel override) — otherwise the item
// omits keepIndex so the backend keeps its own auto-winner. The bulk tests pin
// that per-item shape plus: checkboxes only on Pending cards, "Apply Selected"
// only with a non-empty selection, one apply-batch (not N single applies), and
// selection-clears-after-batch.
//
// Covered: Movies apply-one (default winner index), Series re-pick a NON-winner
// (keepIndex = chosen index), the index-0 pick, Keep All ({keepAll: true}, no
// keepIndex), Dismiss, Scan→refetch, bulk apply (checkbox gating, per-item
// keepIndex-only-on-override, one batch call, selection clears), and Adult
// per-mode endpoint wiring.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import type { Candidate, Proposal } from "@dto";
import { Dedup } from "./Dedup";
import { jsonResponse, asPage, noContent } from "../testing/http";

// MockEventSource mirrors Dashboard.test.tsx / BrowserNotifications.test.tsx:
// jsdom has no EventSource, and Dedup now opens one on mount (the scan-progress
// stream), so it MUST be stubbed for every test in this file — not just the new
// streaming ones. The most recently constructed instance is captured so a test
// can fire scan frames at the active mode's stream.
class MockEventSource {
  static last: MockEventSource | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  url: string;
  closed = false;

  constructor(url: string) {
    this.url = url;
    MockEventSource.last = this;
  }

  close() {
    this.closed = true;
  }

  // emit fires a scan frame the way the real SSE onmessage path does: the server
  // sends `data: {"type":...}`, so the frame's `data` string is the JSON-encoded
  // dedupscan.Event.
  emit(frame: unknown) {
    this.onmessage?.({ data: JSON.stringify(frame) } as MessageEvent);
  }
}


// accepted mirrors the scan POST's new 202 (no body) contract.
const accepted = (): Response => new Response(null, { status: 202 });

beforeEach(() => {
  MockEventSource.last = null;
  // jsdom's localStorage persists across tests in a file; clear it so a
  // per-mode view preference or skipped-id set from one test never leaks into
  // another (and so the default card view / empty skip set is the baseline).
  localStorage.clear();
  vi.stubGlobal("EventSource", MockEventSource);
});

const candidate = (over: Partial<Candidate>): Candidate => ({
  label: "file.mkv",
  path: "/movies/file.mkv",
  resolution: 1080,
  codec: "h264",
  bitRate: 8_000_000,
  winner: false,
  ...over,
});

// A two-candidate group: index 0 tracked (winner by default), index 1 orphan.
const dedupProposal = (over: Partial<Proposal>): Proposal => ({
  id: 1,
  status: "pending",
  sourceName: "Some Movie",
  rootFolderPath: "/movies",
  title: "Some Movie",
  reason: "2 copies identified as \"Some Movie\"",
  candidates: [
    candidate({ label: "tracked", path: "/movies/keep.mkv", winner: true }),
    candidate({ label: "orphan.mkv", path: "/movies/dupe.mkv", winner: false }),
  ],
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
    if (url.includes("/api/organize/events")) return jsonResponse([]);
    const res = await handler(url, init);
    if (url.includes("/dedup/proposals") && res.headers.get("Content-Type")?.includes("json")) {
      const body = await res.clone().json();
      if (Array.isArray(body)) return jsonResponse(asPage(body));
    }
    return res;
  });
  vi.stubGlobal("fetch", fn);
  return calls;
};

const applyCalls = (calls: Call[]) =>
  calls.filter((c) => c.url.includes("/apply"));

// batchCalls / singleApplyCalls disambiguate the two apply routes: "/apply-batch"
// also matches ".includes('/apply')", so bulk tests match "/apply-batch"
// precisely and exclude it when counting single-item applies.
const batchCalls = (calls: Call[]) =>
  calls.filter((c) => c.url.includes("/apply-batch"));
const singleApplyCalls = (calls: Call[]) =>
  calls.filter(
    (c) => c.url.includes("/apply") && !c.url.includes("/apply-batch"),
  );

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("Dedup — Movies (scan → propose → apply the pre-selected winner)", () => {
  it("applies exactly one group, keeping the flagged winner by default", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([dedupProposal({ id: 7, title: "Dupe Group" })]);
      if (
        url.includes("/api/proposals/7/apply") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    expect(await screen.findByText("Dupe Group")).toBeInTheDocument();
    // Both candidate rows render, in wire order.
    expect(screen.getByText("tracked")).toBeInTheDocument();
    expect(screen.getByText("orphan.mkv")).toBeInTheDocument();

    fireEvent.click(screen.getByText("Apply"));

    await waitFor(() => expect(applyCalls(calls)).toHaveLength(1));
    const call = applyCalls(calls)[0]!;
    expect(call.url).toContain("/api/proposals/7/apply");
    expect(call.method).toBe("POST");
    // Winner is candidate index 0 → default keepIndex 0 (sent, not dropped).
    expect(call.body).toEqual({ keepIndex: 0 });
  });

  it("kicks off a scan (202 POST) then re-fetches the queue on the done frame", async () => {
    // The scan is now async: the POST returns 202 and the queue only refetches
    // when the SSE `done` frame arrives — NOT right after the POST resolves.
    let done = false;
    const calls = stubFetch((url, init) => {
      if (
        url.includes("/api/modes/movies/dedup/scan") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return accepted();
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse(
          done ? [dedupProposal({ id: 1, title: "Found After Scan" })] : [],
        );
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    expect(
      await screen.findByText(/No duplicate groups yet/),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByText("Scan"));
    // The POST fired, the button flips to Scanning…, and the stale empty-state
    // text is gone — but the queue has NOT refetched yet (no done frame).
    expect(
      calls.some((c) => c.url.includes("/dedup/scan") && c.method === "POST"),
    ).toBe(true);
    await waitFor(() =>
      expect(screen.queryByText(/No duplicate groups yet/)).toBeNull(),
    );
    expect(screen.getByText("Scanning…")).toBeInTheDocument();

    // The done frame drives the refetch that repopulates the queue.
    done = true;
    MockEventSource.last!.emit({ type: "done", mode: "movies", count: 1, total: 0 });
    expect(await screen.findByText("Found After Scan")).toBeInTheDocument();
    // Scanning cleared: the button returns to its idle label.
    expect(screen.getByText("Scan")).toBeInTheDocument();
  });
});

describe("Dedup — Series (re-pick a non-winner keeper)", () => {
  it("sends the SELECTED candidate's index, not the winner's", async () => {
    // Winner is index 0; the operator re-picks index 1 (the orphan). The Apply
    // body must carry keepIndex 1, or the backend would delete the file the
    // operator chose to keep.
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/series/dedup/proposals"))
        return jsonResponse([
          dedupProposal({
            id: 9,
            title: "Show S01E02",
            candidates: [
              candidate({ label: "tracked", path: "/tv/keep.mkv", winner: true }),
              candidate({ label: "better.mkv", path: "/tv/better.mkv", winner: false }),
            ],
          }),
        ]);
      if (
        url.includes("/api/proposals/9/apply") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    fireEvent.click(await screen.findByText("Series"));
    await screen.findByText("Show S01E02");

    // Pick the non-winner keeper (index 1).
    fireEvent.click(screen.getByLabelText("Keep better.mkv"));
    fireEvent.click(screen.getByText("Apply"));

    await waitFor(() => expect(applyCalls(calls)).toHaveLength(1));
    expect(applyCalls(calls)[0]!.body).toEqual({ keepIndex: 1 });
  });

  it("sends keepIndex 0 (not an empty body) when index-0 is picked but isn't the winner", async () => {
    // Falsy-guard trap: winner sits at index 1, operator explicitly picks the
    // index-0 candidate. The body MUST be { keepIndex: 0 } — an empty body would
    // let the backend fall back to its auto-winner (index 1) and delete the
    // operator's keeper.
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/series/dedup/proposals"))
        return jsonResponse([
          dedupProposal({
            id: 11,
            title: "Zero Pick",
            candidates: [
              candidate({ label: "first.mkv", path: "/tv/first.mkv", winner: false }),
              candidate({ label: "tracked", path: "/tv/keep.mkv", winner: true }),
            ],
          }),
        ]);
      if (
        url.includes("/api/proposals/11/apply") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    fireEvent.click(await screen.findByText("Series"));
    await screen.findByText("Zero Pick");

    fireEvent.click(screen.getByLabelText("Keep first.mkv"));
    fireEvent.click(screen.getByText("Apply"));

    await waitFor(() => expect(applyCalls(calls)).toHaveLength(1));
    const body = applyCalls(calls)[0]!.body as Record<string, unknown>;
    expect(body).toEqual({ keepIndex: 0 });
    expect("keepIndex" in body).toBe(true);
  });
});

describe("Dedup — Keep All and Dismiss", () => {
  it("Keep All sends keepAll:true with no keepIndex", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([dedupProposal({ id: 3, title: "Keep Both" })]);
      if (
        url.includes("/api/proposals/3/apply") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("Keep Both");
    fireEvent.click(screen.getByText("Keep All"));

    await waitFor(() => expect(applyCalls(calls)).toHaveLength(1));
    const body = applyCalls(calls)[0]!.body as Record<string, unknown>;
    expect(body).toEqual({ keepAll: true });
    expect("keepIndex" in body).toBe(false);
  });

  it("Dismiss drops exactly one group without applying", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([dedupProposal({ id: 4, title: "Dismiss Me" })]);
      if (
        url.includes("/api/proposals/4/dismiss") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("Dismiss Me");
    fireEvent.click(screen.getByText("Dismiss"));

    await waitFor(() =>
      expect(
        calls.some((c) => c.url.includes("/api/proposals/4/dismiss")),
      ).toBe(true),
    );
    // Dismiss is not an apply.
    expect(applyCalls(calls)).toHaveLength(0);
  });
});

describe("Dedup — single-group apply is still one group per click", () => {
  it("resolves exactly the clicked group's proposal id, per-group Apply/Keep All/Dismiss present", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([
          dedupProposal({ id: 1, title: "A" }),
          dedupProposal({ id: 2, title: "B" }),
          dedupProposal({ id: 3, title: "C" }),
        ]);
      if (
        url.includes("/api/proposals/2/apply") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("A");

    // Each group keeps its own Apply / Keep All / Dismiss controls.
    expect(screen.getAllByText("Apply")).toHaveLength(3);
    expect(screen.getAllByText("Keep All")).toHaveLength(3);
    expect(screen.getAllByText("Dismiss")).toHaveLength(3);

    // Clicking one group's Apply resolves exactly that one proposal id — one
    // single-item apply, no batch.
    fireEvent.click(screen.getAllByText("Apply")[1]!);
    await waitFor(() => expect(singleApplyCalls(calls)).toHaveLength(1));
    expect(singleApplyCalls(calls)[0]!.url).toContain("/api/proposals/2/apply");
    expect(batchCalls(calls)).toHaveLength(0);
  });
});

describe("Dedup — bulk apply (opt-in multi-select of Pending groups)", () => {
  it("renders a checkbox only for Pending cards, never for a non-pending one", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([
          dedupProposal({ id: 1, title: "Pending One", status: "pending" }),
          dedupProposal({ id: 2, title: "Pending Two", status: "pending" }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("Pending One");

    expect(screen.getByLabelText("Select Pending One")).toBeInTheDocument();
    expect(screen.getByLabelText("Select Pending Two")).toBeInTheDocument();
    // Total checkboxes = 2 pending-select + 1 select-all + 2 "Also keep" + Show history.
    expect(document.querySelectorAll('input[type="checkbox"]')).toHaveLength(6);
  });

  it("shows 'Apply Selected' only once a group is selected", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([dedupProposal({ id: 1, title: "Only" })]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("Only");

    expect(screen.queryByText(/Apply Selected/)).toBeNull();
    fireEvent.click(screen.getByLabelText("Select Only"));
    expect(await screen.findByText("Apply Selected (1)")).toBeInTheDocument();
  });

  it("sends ONE apply-batch, with keepIndex only for a group whose radio was overridden, then clears the selection", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([
          dedupProposal({
            id: 1,
            title: "Group One",
            candidates: [
              candidate({ label: "one-keep", winner: true }),
              candidate({ label: "one-dupe", winner: false }),
            ],
          }),
          dedupProposal({
            id: 2,
            title: "Group Two",
            candidates: [
              candidate({ label: "two-keep", winner: true }),
              candidate({ label: "two-better", winner: false }),
            ],
          }),
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

    render(() => <Dedup />);
    await screen.findByText("Group One");

    // Select all pending; override only Group Two's keeper radio (index 1).
    fireEvent.click(screen.getByLabelText("Select all pending"));
    fireEvent.click(screen.getByLabelText("Keep two-better"));
    fireEvent.click(await screen.findByText("Apply Selected (2)"));

    await waitFor(() => expect(batchCalls(calls)).toHaveLength(1));
    expect(singleApplyCalls(calls)).toHaveLength(0);
    // Group One kept its auto-winner → no keepIndex; Group Two overridden → 1.
    expect(batchCalls(calls)[0]!.body).toEqual({
      items: [{ id: 1 }, { id: 2, keepIndex: 1 }],
    });
    expect(await screen.findByText("2 applied, 0 failed")).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByText(/Apply Selected/)).toBeNull());
  });
});

describe("Dedup — Adult (per-mode endpoint wiring)", () => {
  it("targets the adult dedup endpoints when the Adult tab is active", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([]); // Movies renders first; keep it quiet.
      if (url.includes("/api/modes/adult/dedup/proposals"))
        return jsonResponse([
          dedupProposal({
            id: 21,
            title: "Studio - Scene",
            candidates: [
              candidate({ label: "tracked", path: "/adult/keep.mp4", winner: true }),
              candidate({ label: "dupe.mp4", path: "/adult/dupe.mp4", winner: false }),
            ],
          }),
        ]);
      if (
        url.includes("/api/proposals/21/apply") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    fireEvent.click(await screen.findByText("Adult"));
    await screen.findByText("Studio - Scene");
    fireEvent.click(screen.getByText("Apply"));

    await waitFor(() => expect(applyCalls(calls)).toHaveLength(1));
    expect(applyCalls(calls)[0]!.url).toContain("/api/proposals/21/apply");
    expect(
      calls.some((c) => c.url.includes("/api/modes/adult/dedup/proposals")),
    ).toBe(true);
  });
});

describe("Dedup — live scan progress stream", () => {
  it("populates the log box on progress and clears + refetches on done, empty-state hidden while scanning", async () => {
    let done = false;
    stubFetch((url, init) => {
      if (
        url.includes("/api/modes/movies/dedup/scan") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return accepted();
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse(
          done ? [dedupProposal({ id: 1, title: "Resolved Dupe" })] : [],
        );
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText(/No duplicate groups yet/);

    fireEvent.click(screen.getByText("Scan"));
    const es = MockEventSource.last!;

    // A progress frame appears as a "current/total · name · phase" log line, and
    // the stale "No duplicate groups yet" text is gone while scanning.
    es.emit({
      type: "progress",
      mode: "movies",
      current: 1,
      total: 3,
      name: "a.mkv",
      phase: "hashing",
    });
    expect(await screen.findByText(/1\/3 · a\.mkv · hashing/)).toBeInTheDocument();
    expect(screen.queryByText(/No duplicate groups yet/)).toBeNull();

    es.emit({
      type: "progress",
      mode: "movies",
      current: 2,
      total: 3,
      name: "b.mkv",
      phase: "hashing",
    });
    expect(await screen.findByText(/2\/3 · b\.mkv · hashing/)).toBeInTheDocument();

    // The done frame clears scanning, clears the log box, and refetches the
    // resolved proposal list.
    done = true;
    es.emit({ type: "done", mode: "movies", count: 1, total: 3 });
    expect(await screen.findByText("Resolved Dupe")).toBeInTheDocument();
    // Log lines are gone (box cleared) and the button is idle again.
    expect(screen.queryByText(/2\/3 · b\.mkv/)).toBeNull();
    expect(screen.getByText("Scan")).toBeInTheDocument();
  });

  it("renders a neutral 'Starting scan…' state for the synthetic starting seed (no 0/0 log row)", async () => {
    stubFetch((url, init) => {
      if (
        url.includes("/api/modes/movies/dedup/scan") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return accepted();
      if (url.includes("/api/modes/movies/dedup/proposals")) return jsonResponse([]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText(/No duplicate groups yet/);
    fireEvent.click(screen.getByText("Scan"));

    // The reconnect-priming seed carries phase:"starting" with current/total
    // ABSENT (omitempty) — it must show "Starting scan…", never a "0/0" row.
    MockEventSource.last!.emit({ type: "progress", mode: "movies", phase: "starting" });
    expect(await screen.findByText(/Starting scan/)).toBeInTheDocument();
    expect(screen.queryByText(/0\/0/)).toBeNull();
  });

  it("surfaces an error frame and clears scanning — the UI is not left stuck", async () => {
    stubFetch((url, init) => {
      if (
        url.includes("/api/modes/movies/dedup/scan") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return accepted();
      if (url.includes("/api/modes/movies/dedup/proposals")) return jsonResponse([]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText(/No duplicate groups yet/);
    fireEvent.click(screen.getByText("Scan"));
    expect(screen.getByText("Scanning…")).toBeInTheDocument();

    MockEventSource.last!.emit({
      type: "error",
      mode: "movies",
      error: "root folder unreadable",
    });
    expect(
      await screen.findByText(/root folder unreadable/),
    ).toBeInTheDocument();
    // Scanning cleared — button is idle, not stuck on "Scanning…".
    expect(screen.getByText("Scan")).toBeInTheDocument();
  });

  it("recovers a stuck scan via the liveness backstop when the terminal frame is DROPPED", async () => {
    // The scan runs but NO terminal frame is ever delivered on the stream (it was
    // dropped/delayed). The quiet-window timer must fire, reconcile against the
    // status endpoint (inflight:false ⇒ the scan really finished), and clear
    // scanning + refetch — proving the terminal state is recoverable without a
    // page reload even when the terminal SSE frame itself is lost.
    let recovered = false;
    let statusChecks = 0;
    stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/dedup/scan/status")) {
        statusChecks++;
        return jsonResponse({ inflight: false });
      }
      if (
        url.includes("/api/modes/movies/dedup/scan") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return accepted();
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse(
          recovered ? [dedupProposal({ id: 9, title: "Recovered Group" })] : [],
        );
      throw new Error("unexpected fetch: " + url);
    });

    // Real timers for the initial mount/resource; fake timers only for the
    // 15s quiet window (armed inside initiate below).
    render(() => <Dedup />);
    await screen.findByText(/No duplicate groups yet/);

    vi.useFakeTimers();
    try {
      // The proposals refetch that the backstop triggers must now return groups.
      recovered = true;
      fireEvent.click(screen.getByText("Scan"));
      expect(screen.getByText("Scanning…")).toBeInTheDocument();

      // No progress and no done/error frame is ever emitted. Advancing past the
      // quiet window fires the backstop, which reconciles via the status endpoint
      // and recovers. advanceTimersByTimeAsync also flushes the reconcile's async
      // status-fetch + refetch microtasks.
      await vi.advanceTimersByTimeAsync(15_000);
      expect(statusChecks).toBeGreaterThan(0);
    } finally {
      vi.useRealTimers();
    }

    // Scanning cleared and the queue repopulated purely from the status reconcile
    // — the terminal frame was never delivered.
    expect(await screen.findByText("Recovered Group")).toBeInTheDocument();
    expect(screen.getByText("Scan")).toBeInTheDocument();
  });
});

// vmafComputing is the poll-shaped "computing" body the card view's VMAF badge
// gets on a cache miss; quiets the many VMAF fetches card-view tiles fire so a
// non-VMAF test's fetch stub doesn't have to enumerate them.
const vmafComputing = () =>
  jsonResponse({ status: "computing", candidateIndex: 1, referenceIndex: 0 });

describe("Dedup — list/card view toggle (persisted per mode)", () => {
  it("defaults to card view, switches to list, and persists the choice", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([dedupProposal({ id: 1, title: "Grp" })]);
      if (url.includes("/vmaf")) return vmafComputing();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("Grp");
    // Card default: a <video> preview tile renders; the list table header does not.
    expect(document.querySelector("video")).toBeTruthy();
    expect(screen.queryByText("Path")).toBeNull();

    // Switch to list: the table header appears, the video tiles are gone, and
    // the choice is persisted for this mode.
    fireEvent.click(screen.getByText("List"));
    expect(await screen.findByText("Path")).toBeInTheDocument();
    expect(document.querySelector("video")).toBeNull();
    expect(localStorage.getItem("sakms.dedup.viewmode.movies")).toBe("list");
  });

  it("reads the persisted list view on mount (survives a reload)", async () => {
    localStorage.setItem("sakms.dedup.viewmode.movies", "list");
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([dedupProposal({ id: 1, title: "Grp" })]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("Grp");
    // Straight into list view: table header present, no video, no VMAF fetch
    // (the stub would throw on one).
    expect(screen.getByText("Path")).toBeInTheDocument();
    expect(document.querySelector("video")).toBeNull();
  });
});

describe("Dedup — Skip (client-side, localStorage)", () => {
  it("hides a skipped pending group and persists the id per mode", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([
          dedupProposal({ id: 5, title: "Keep Me" }),
          dedupProposal({ id: 6, title: "Skip Me" }),
        ]);
      if (url.includes("/vmaf")) return vmafComputing();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("Skip Me");
    // Skip the second group: it disappears; the first stays.
    fireEvent.click(screen.getAllByText("Skip")[1]!);
    await waitFor(() => expect(screen.queryByText("Skip Me")).toBeNull());
    expect(screen.getByText("Keep Me")).toBeInTheDocument();
    expect(localStorage.getItem("sakms.dedup.skipped.movies")).toContain("6");
  });

  it("survives a reload, then self-empties once a scan rotates the ids", async () => {
    localStorage.setItem("sakms.dedup.skipped.movies", JSON.stringify([6]));
    let rotated = false;
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse(
          rotated
            ? [dedupProposal({ id: 99, title: "Fresh Group" })]
            : [
                dedupProposal({ id: 6, title: "Skip Me" }),
                dedupProposal({ id: 5, title: "Keep Me" }),
              ],
        );
      if (url.includes("/vmaf")) return vmafComputing();
      throw new Error("unexpected fetch: " + url);
    });

    const first = render(() => <Dedup />);
    await screen.findByText("Keep Me");
    // The persisted skip id (6) hides that group across the reload.
    expect(screen.queryByText("Skip Me")).toBeNull();
    first.unmount();

    // A rescan rotates proposal ids (6 no longer exists) → the skip set prunes
    // itself to empty against the live Pending ids.
    rotated = true;
    render(() => <Dedup />);
    await screen.findByText("Fresh Group");
    await waitFor(() =>
      expect(localStorage.getItem("sakms.dedup.skipped.movies")).toBe("[]"),
    );
  });
});

describe("Dedup — multi-keep Apply request shape", () => {
  it("sends additionalKeepIndices for extra checked keepers", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([
          dedupProposal({
            id: 8,
            title: "Trio",
            candidates: [
              candidate({ label: "a", winner: true }),
              candidate({ label: "b", winner: false }),
              candidate({ label: "c", winner: false }),
            ],
          }),
        ]);
      if (url.includes("/vmaf")) return vmafComputing();
      if (
        url.includes("/api/proposals/8/apply") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("Trio");
    // Primary defaults to the winner (index 0 "a"); also-keep "c" (index 2).
    fireEvent.click(screen.getByLabelText("Also keep c"));
    fireEvent.click(screen.getByText("Apply"));

    await waitFor(() => expect(applyCalls(calls)).toHaveLength(1));
    expect(applyCalls(calls)[0]!.body).toEqual({
      keepIndex: 0,
      additionalKeepIndices: [2],
    });
  });

  it("OMITS additionalKeepIndices entirely when no extra keeper is checked", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([dedupProposal({ id: 8, title: "Solo" })]);
      if (url.includes("/vmaf")) return vmafComputing();
      if (
        url.includes("/api/proposals/8/apply") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("Solo");
    fireEvent.click(screen.getByText("Apply"));

    await waitFor(() => expect(applyCalls(calls)).toHaveLength(1));
    const body = applyCalls(calls)[0]!.body as Record<string, unknown>;
    expect(body).toEqual({ keepIndex: 0 });
    expect("additionalKeepIndices" in body).toBe(false);
  });
});

describe("Dedup — Keep All vs checking every box are distinct (AC15)", () => {
  it("Keep All tracks nothing; checking every box tracks the primary", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([
          dedupProposal({
            id: 12,
            title: "Both",
            candidates: [
              candidate({ label: "a", winner: true }),
              candidate({ label: "b", winner: false }),
            ],
          }),
        ]);
      if (url.includes("/vmaf")) return vmafComputing();
      if (
        url.includes("/api/proposals/12/apply") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("Both");

    // Check every non-primary box, then Apply → track primary, delete nothing.
    fireEvent.click(screen.getByLabelText("Also keep b"));
    fireEvent.click(screen.getByText("Apply"));
    await waitFor(() => expect(applyCalls(calls)).toHaveLength(1));
    expect(applyCalls(calls)[0]!.body).toEqual({
      keepIndex: 0,
      additionalKeepIndices: [1],
    });

    // Keep All is the SEPARATE control: track nothing, delete nothing — a
    // distinct wire shape ({keepAll:true}) with no keepIndex/additional set.
    fireEvent.click(screen.getByText("Keep All"));
    await waitFor(() => expect(applyCalls(calls)).toHaveLength(2));
    expect(applyCalls(calls)[1]!.body).toEqual({ keepAll: true });
  });
});

describe("Dedup — bulk apply threads additionalKeepIndices (AC13)", () => {
  it("includes keepIndex + the multi-keep set per batched group that has extras", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([
          dedupProposal({
            id: 1,
            title: "G1",
            candidates: [
              candidate({ label: "a1", winner: true }),
              candidate({ label: "b1", winner: false }),
              candidate({ label: "c1", winner: false }),
            ],
          }),
          dedupProposal({
            id: 2,
            title: "G2",
            candidates: [
              candidate({ label: "a2", winner: true }),
              candidate({ label: "b2", winner: false }),
            ],
          }),
        ]);
      if (url.includes("/vmaf")) return vmafComputing();
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

    render(() => <Dedup />);
    await screen.findByText("G1");
    fireEvent.click(screen.getByLabelText("Select all pending"));
    // G1: also-keep c1 (index 2) — the batch item must carry keepIndex 0 AND
    // additionalKeepIndices [2] (the backend rejects a nil keepIndex with a
    // non-empty additional set). G2 has no extras → bare {id:2}.
    fireEvent.click(screen.getByLabelText("Also keep c1"));
    fireEvent.click(await screen.findByText("Apply Selected (2)"));

    await waitFor(() => expect(batchCalls(calls)).toHaveLength(1));
    expect(batchCalls(calls)[0]!.body).toEqual({
      items: [{ id: 1, keepIndex: 0, additionalKeepIndices: [2] }, { id: 2 }],
    });
  });
});

describe("Dedup — primary keeper invariant (safety-critical, AC16)", () => {
  it("Apply always carries a keepIndex — the kept set can never be empty", async () => {
    // The primary is a radio with exactly one selection at all times, so a
    // 0-checked state (which would let the backend delete the whole group) is
    // structurally unreachable — Apply always sends a keepIndex.
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([dedupProposal({ id: 4, title: "Always" })]);
      if (url.includes("/vmaf")) return vmafComputing();
      if (
        url.includes("/api/proposals/4/apply") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("Always");
    // Exactly one primary radio is checked in the group.
    const checkedRadios = Array.from(
      document.querySelectorAll<HTMLInputElement>('input[type="radio"]'),
    ).filter((r) => r.checked);
    expect(checkedRadios).toHaveLength(1);

    fireEvent.click(screen.getByText("Apply"));
    await waitFor(() => expect(applyCalls(calls)).toHaveLength(1));
    expect("keepIndex" in (applyCalls(calls)[0]!.body as object)).toBe(true);
  });

  it("changing the primary in multi-keep mode preserves the whole kept set", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([
          dedupProposal({
            id: 7,
            title: "Repoint",
            candidates: [
              candidate({ label: "a", winner: true }),
              candidate({ label: "b", winner: false }),
              candidate({ label: "c", winner: false }),
            ],
          }),
        ]);
      if (url.includes("/vmaf")) return vmafComputing();
      if (
        url.includes("/api/proposals/7/apply") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("Repoint");
    // Keep all three (primary a=0, also-keep b=1 and c=2).
    fireEvent.click(screen.getByLabelText("Also keep b"));
    fireEvent.click(screen.getByLabelText("Also keep c"));
    // Re-point the tracked copy to b (index 1). The old primary a (index 0)
    // must stay kept (moved into the additional set), NOT be dropped/deleted.
    fireEvent.click(screen.getByLabelText("Keep b"));
    fireEvent.click(screen.getByText("Apply"));

    await waitFor(() => expect(applyCalls(calls)).toHaveLength(1));
    const body = applyCalls(calls)[0]!.body as {
      keepIndex: number;
      additionalKeepIndices: number[];
    };
    expect(body.keepIndex).toBe(1);
    // Whole kept set {0,1,2} preserved: primary 1 tracked, {0,2} also kept.
    expect(new Set(body.additionalKeepIndices)).toEqual(new Set([0, 2]));
  });

  it("promoting the SOLE also-kept candidate to primary preserves the old primary, never drops it", async () => {
    // Regression test for a real data-loss bug: onPickPrimary's multi-keep
    // check originally read additionalKeep's size AFTER deleting the
    // promoted candidate from it, so with exactly one additional keeper,
    // promoting that specific candidate emptied the set before the check
    // ran — misclassifying it as sole-keep mode and silently dropping the
    // old primary out of the kept set entirely (deleted on Apply, despite
    // the operator never unchecking it). The existing multi-keep test above
    // uses two additional keepers, so `add.size > 0` still held after the
    // delete and never exercised this path — this test specifically pins
    // the one-additional-keeper case.
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([
          dedupProposal({
            id: 8,
            title: "SoleAdditional",
            candidates: [
              candidate({ label: "a", winner: true }),
              candidate({ label: "b", winner: false }),
            ],
          }),
        ]);
      if (url.includes("/vmaf")) return vmafComputing();
      if (
        url.includes("/api/proposals/8/apply") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("SoleAdditional");
    // Primary is a (index 0). Check "Also keep b" — the ONLY additional keeper.
    fireEvent.click(screen.getByLabelText("Also keep b"));
    // Promote b (the sole also-kept candidate) to primary.
    fireEvent.click(screen.getByLabelText("Keep b"));
    fireEvent.click(screen.getByText("Apply"));

    await waitFor(() => expect(applyCalls(calls)).toHaveLength(1));
    const body = applyCalls(calls)[0]!.body as {
      keepIndex: number;
      additionalKeepIndices?: number[];
    };
    expect(body.keepIndex).toBe(1);
    // The old primary (a, index 0) must be preserved as an additional
    // keeper, not silently dropped — this is the exact assertion that fails
    // against the pre-fix code (additionalKeepIndices would be omitted/empty).
    expect(new Set(body.additionalKeepIndices ?? [])).toEqual(new Set([0]));
  });

  it("re-picking the primary in sole-keep mode keeps ONLY the picked candidate", async () => {
    // With no additional keepers checked, picking a new primary is a plain
    // dedup re-pick: keep only that one, delete the rest (matches the named
    // Series re-pick test's semantics, asserted here in the multi-keep model).
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([
          dedupProposal({
            id: 15,
            title: "Sole",
            candidates: [
              candidate({ label: "a", winner: true }),
              candidate({ label: "b", winner: false }),
            ],
          }),
        ]);
      if (url.includes("/vmaf")) return vmafComputing();
      if (
        url.includes("/api/proposals/15/apply") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("Sole");
    fireEvent.click(screen.getByLabelText("Keep b"));
    fireEvent.click(screen.getByText("Apply"));

    await waitFor(() => expect(applyCalls(calls)).toHaveLength(1));
    const body = applyCalls(calls)[0]!.body as Record<string, unknown>;
    expect(body).toEqual({ keepIndex: 1 });
    expect("additionalKeepIndices" in body).toBe(false);
  });
});

// Claude 2026-08-04: new describe block — AC6 (spec: phash similarity %
// displayed on the card) had NO test referencing pHashSimilarity anywhere in
// this file before this change, despite the badge/confidence-label rendering
// logic existing at Dedup.tsx:99-109/628-633 since 50dd970. Reason: see the
// residual-gap plan's Wave 2 (T2.4) — the backend persistence half of this
// same defect is fixed by migration 0060 (internal/proposals/proposals.go).
// Troubleshooting: n/a — new coverage, no prior bug in this describe block.
// Review if: the confidence-band boundaries change in Dedup.tsx's
// similarityLabel — these thresholds (0.9 / 0.7) are read directly from that
// function, not from the spec's original (superseded) 0.95/0.78/0.62 numbers.
describe("Dedup — phash similarity badge and confidence label (AC6)", () => {
  it("renders the similarity percentage and 'high confidence' label at/above 0.9", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([
          dedupProposal({ id: 1, title: "High Confidence", pHashSimilarity: 0.95 }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("High Confidence");
    expect(screen.getByText("95% similar · high confidence duplicate")).toBeInTheDocument();
  });

  it("renders 'likely duplicate' for a mid-band similarity (>= 0.7, < 0.9)", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([
          dedupProposal({ id: 2, title: "Likely", pHashSimilarity: 0.8 }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("Likely");
    expect(screen.getByText("80% similar · likely duplicate")).toBeInTheDocument();
  });

  it("renders 'possible duplicate — review carefully' below the 0.7 boundary", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([
          dedupProposal({ id: 3, title: "Possible", pHashSimilarity: 0.5 }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("Possible");
    expect(
      screen.getByText("50% similar · possible duplicate — review carefully"),
    ).toBeInTheDocument();
  });

  it("hides the badge entirely when pHashSimilarity is absent (legacy/Adult proposals)", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([dedupProposal({ id: 4, title: "No Score" })]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("No Score");
    expect(screen.queryByText(/% similar/)).toBeNull();
  });
});

describe("Dedup — VMAF card wiring (AC17)", () => {
  const vmafGroup = () =>
    dedupProposal({
      id: 3,
      title: "V",
      pHashSimilarity: 0.8,
      candidates: [
        candidate({ label: "tracked", path: "/m/keep.mkv", winner: true }),
        candidate({ label: "orphan.mkv", path: "/m/dupe.mkv", winner: false }),
      ],
    });

  it("shows a computing indicator, then the score on ready", async () => {
    let ready = false;
    stubFetch((url) => {
      if (
        url.includes("/api/modes/movies/dedup/proposals") &&
        !url.includes("/vmaf") &&
        !url.includes("/video")
      )
        return jsonResponse([vmafGroup()]);
      if (url.includes("/vmaf"))
        return jsonResponse(
          ready
            ? { status: "ready", score: 88.5, candidateIndex: 1, referenceIndex: 0 }
            : { status: "computing", candidateIndex: 1, referenceIndex: 0 },
        );
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("V");
    // The non-primary tile shows the pending indicator first.
    expect(await screen.findByText("VMAF…")).toBeInTheDocument();
    // Flip the backend to ready; the bounded re-poll picks up the real score.
    ready = true;
    expect(
      await screen.findByText("VMAF 88.5", undefined, { timeout: 4000 }),
    ).toBeInTheDocument();
  });

  it("shows a non-blocking error indicator; Apply stays enabled", async () => {
    stubFetch((url, init) => {
      if (
        url.includes("/api/modes/movies/dedup/proposals") &&
        !url.includes("/vmaf") &&
        !url.includes("/video")
      )
        return jsonResponse([vmafGroup()]);
      if (url.includes("/vmaf"))
        return jsonResponse({
          status: "error",
          error: "ffmpeg failed",
          candidateIndex: 1,
          referenceIndex: 0,
        });
      if (
        url.includes("/api/proposals/3/apply") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("V");
    expect(await screen.findByText("VMAF n/a")).toBeInTheDocument();
    // The error never disables the group's actions.
    const applyBtn = screen.getByText("Apply") as HTMLButtonElement;
    expect(applyBtn.disabled).toBe(false);
  });

  it("re-fetches against the new referenceIndex when the primary changes", async () => {
    const calls = stubFetch((url) => {
      if (
        url.includes("/api/modes/movies/dedup/proposals") &&
        !url.includes("/vmaf") &&
        !url.includes("/video")
      )
        return jsonResponse([vmafGroup()]);
      if (url.includes("/vmaf"))
        return jsonResponse({ status: "computing", candidateIndex: 0, referenceIndex: 0 });
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("V");
    // Initially primary=0 → the non-primary tile (index 1) scores against ref 0.
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.url.includes("/vmaf") &&
            c.url.includes("candidateIndex=1") &&
            c.url.includes("referenceIndex=0"),
        ),
      ).toBe(true),
    );
    // Re-point primary to index 1 → tile 0 becomes the non-primary and scores
    // against the NEW reference (index 1) — no special-case logic, just a new
    // query key.
    fireEvent.click(screen.getByLabelText("Keep orphan.mkv"));
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.url.includes("/vmaf") &&
            c.url.includes("candidateIndex=0") &&
            c.url.includes("referenceIndex=1"),
        ),
      ).toBe(true),
    );
  });

  it("stops polling once the tiles unmount", async () => {
    const calls = stubFetch((url) => {
      if (
        url.includes("/api/modes/movies/dedup/proposals") &&
        !url.includes("/vmaf") &&
        !url.includes("/video")
      )
        return jsonResponse([vmafGroup()]);
      if (url.includes("/vmaf")) return vmafComputing();
      throw new Error("unexpected fetch: " + url);
    });

    const { unmount } = render(() => <Dedup />);
    await screen.findByText("V");
    await waitFor(() =>
      expect(calls.filter((c) => c.url.includes("/vmaf")).length).toBeGreaterThan(
        0,
      ),
    );
    unmount();
    const afterUnmount = calls.filter((c) => c.url.includes("/vmaf")).length;
    // Past one full poll interval, no further VMAF fetches fire.
    await new Promise((r) => setTimeout(r, 2800));
    expect(calls.filter((c) => c.url.includes("/vmaf")).length).toBe(afterUnmount);
  });

  it("does not request VMAF for low-confidence possible duplicates", async () => {
    const calls = stubFetch((url) => {
      if (
        url.includes("/api/modes/movies/dedup/proposals") &&
        !url.includes("/vmaf") &&
        !url.includes("/video")
      )
        return jsonResponse([
          dedupProposal({
            id: 30,
            title: "Possible Only",
            pHashSimilarity: 0.5,
            candidates: [
              candidate({ label: "primary.mkv", path: "/m/primary.mkv", winner: true }),
              candidate({ label: "alt.mkv", path: "/m/alt.mkv", winner: false }),
            ],
          }),
        ]);
      if (url.includes("/vmaf")) throw new Error("low-confidence group must not request VMAF");
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("Possible Only");
    await new Promise((r) => setTimeout(r, 50));

    expect(calls.some((c) => c.url.includes("/vmaf"))).toBe(false);
  });
});

// Wave 4 — wiring the shared SearchTakeover into Dedup (AC6, see
// .omc/plans/autopilot-impl-cross-mode-move.md §6.4). These tests cover exactly what this
// wave owns: the per-group "Move to…" select's option set, its
// pending-or-unmatched visibility (a deliberate decision — an Unmatched
// group is precisely the one an operator most needs to move), the
// reset-to-placeholder onChange contract, and that this wiring never touches
// the candidate grid. SearchTakeover's own search/commit/error-branch
// behavior is covered by SearchTakeover.test.tsx, not duplicated here.
describe("Dedup — Move to another mode (AC6)", () => {
  it("renders exactly the 2 other-mode options for a Movies group, never the group's own mode", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([dedupProposal({ id: 30, title: "Movable" })]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("Movable");

    const select = screen.getByLabelText(
      "Move group Movable to another mode",
    ) as HTMLSelectElement;
    const optionLabels = Array.from(select.options).map((o) => o.textContent);
    expect(optionLabels).toEqual(["Move to…", "Move to Series", "Move to Adult"]);
  });

  it("renders exactly the 2 other-mode options for an Adult group, never the group's own mode", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([]);
      if (url.includes("/api/modes/adult/dedup/proposals"))
        return jsonResponse([dedupProposal({ id: 31, title: "Adult Movable" })]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    fireEvent.click(await screen.findByText("Adult"));
    await screen.findByText("Adult Movable");

    const select = screen.getByLabelText(
      "Move group Adult Movable to another mode",
    ) as HTMLSelectElement;
    const optionLabels = Array.from(select.options).map((o) => o.textContent);
    expect(optionLabels).toEqual(["Move to…", "Move to Movies", "Move to Series"]);
  });

  it("shows the affordance for an Unmatched group too, not only Pending — the deliberate pending-or-unmatched gate", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([
          dedupProposal({ id: 33, title: "Stuck", status: "unmatched" }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("Stuck");
    expect(
      screen.getByLabelText("Move group Stuck to another mode"),
    ).toBeInTheDocument();
    // Unmatched groups get no Apply/Keep All/Skip/Dismiss row (pending-only)
    // — proving the select's own Show is a SEPARATE guard, not nested inside
    // the pending-only one.
    expect(screen.queryByText("Apply")).toBeNull();
    expect(screen.queryByText("Keep All")).toBeNull();
  });

  it("resets the select back to the placeholder immediately after choosing a target", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([dedupProposal({ id: 34, title: "Reset Me" })]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("Reset Me");

    const select = screen.getByLabelText(
      "Move group Reset Me to another mode",
    ) as HTMLSelectElement;
    fireEvent.change(select, { target: { value: "series" } });
    // Reset immediately, so re-selecting the SAME target later still fires
    // onChange (a select that kept its "selected" state would not re-fire).
    expect(select.value).toBe("");
  });

  it("moving a group leaves its candidate rows byte-identical after the move commits", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([
          dedupProposal({
            id: 32,
            title: "Move Me",
            candidates: [
              candidate({
                label: "tracked",
                path: "/movies/keep.mkv",
                winner: true,
                resolution: 1080,
                codec: "h264",
              }),
              candidate({
                label: "orphan.mkv",
                path: "/movies/dupe.mkv",
                winner: false,
                resolution: 720,
                codec: "h265",
              }),
            ],
          }),
        ]);
      // A Series-mode search queries BOTH TMDB catalogs (SearchTakeover merges
      // movie results in), so this branch is required or the Promise.all
      // rejects on the trailing throw. Empty keeps the single candidate tile
      // unambiguous.
      if (url.includes("/api/modes/movies/tmdb-search")) return jsonResponse([]);
      if (url.includes("/api/modes/series/tmdb-search"))
        return jsonResponse([
          { id: 555, title: "New Show", releaseDate: "2020-01-01" },
        ]);
      // A series target drills into SeasonEpisodePicker, which self-fetches
      // its season list. Empty seasons routes it to its degraded free-text
      // fallback, which is irrelevant here — this test commits show-level.
      if (url.includes("/discover/detail")) return jsonResponse({ seasons: [] });
      if (
        url.includes("/api/proposals/32/move-mode") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("Move Me");
    // List view exercises the exact table this wave must never touch.
    fireEvent.click(screen.getByText("List"));
    expect(await screen.findByText("Path")).toBeInTheDocument();
    expect(screen.getByText("/movies/keep.mkv")).toBeInTheDocument();
    expect(screen.getByText("/movies/dupe.mkv")).toBeInTheDocument();

    const select = screen.getByLabelText("Move group Move Me to another mode");
    fireEvent.change(select, { target: { value: "series" } });

    // The takeover is a plain <section aria-label="Search"> now, not a
    // role="dialog" — it REPLACES the list in place. Its heading is the
    // stable identity probe. Curly quotes wrap the title, so the regex is
    // deliberately quote-agnostic.
    await screen.findByText(/Move .+ to another section/);
    fireEvent.click(screen.getByText("Search"));
    fireEvent.click(await screen.findByLabelText("Use New Show"));
    // A SERIES target drills into the slot step instead of committing on the
    // tile click (SearchTakeover's useCatalogItem). "Use show-level match
    // only" is the no-slot commit — it sends the identical
    // {tmdbId,title,year} body the old single-click "Use this" sent, so this
    // is still a selector-level change: nothing about what is asserted moved.
    fireEvent.click(await screen.findByText("Use show-level match only"));

    await waitFor(() =>
      expect(
        calls.some((c) => c.url.includes("/api/proposals/32/move-mode")),
      ).toBe(true),
    );
    // Commit succeeded → onDone closed the takeover and refetched.
    await waitFor(() =>
      expect(screen.queryByLabelText("Back to list")).toBeNull(),
    );

    // The candidate grid this feature must never touch is byte-identical
    // after the move commits — same labels, paths, resolution, and codec.
    // findByText (not getByText) for the first assertion: the refetch onDone
    // triggers is async, so the queue briefly shows "Loading…" again.
    expect(await screen.findByText("tracked")).toBeInTheDocument();
    expect(screen.getByText("orphan.mkv")).toBeInTheDocument();
    expect(screen.getByText("/movies/keep.mkv")).toBeInTheDocument();
    expect(screen.getByText("/movies/dupe.mkv")).toBeInTheDocument();
    expect(screen.getByText("1080p")).toBeInTheDocument();
    expect(screen.getByText("h264")).toBeInTheDocument();
    expect(screen.getByText("720p")).toBeInTheDocument();
    expect(screen.getByText("h265")).toBeInTheDocument();
    expect(screen.getAllByText("8000 kbps")).toHaveLength(2);
  });

  it("selecting Move to Series then Back issues no /move-mode POST", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([dedupProposal({ id: 35, title: "Cancel Me" })]);
      if (url.includes("/api/modes/series/tmdb-search")) return jsonResponse([]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("Cancel Me");

    const select = screen.getByLabelText("Move group Cancel Me to another mode");
    fireEvent.change(select, { target: { value: "series" } });
    await screen.findByText(/Move .+ to another section/);

    fireEvent.click(screen.getByLabelText("Back to list"));
    await waitFor(() =>
      expect(screen.queryByLabelText("Back to list")).toBeNull(),
    );

    expect(calls.some((c) => c.url.includes("/move-mode"))).toBe(false);
    // The group's own row is untouched — still there, unresolved.
    expect(screen.getByText("Cancel Me")).toBeInTheDocument();
  });

  // D-1a commit-403 (frontend) — .omc/plans/autopilot-impl-cross-mode-move.md §7's test
  // table. SearchTakeover.test.tsx already covers this branch at the
  // component level, in isolation. This test proves the SAME behavior
  // through Dedup's real per-group "Move to…" select + SearchTakeover
  // wiring, not a standalone panel render. Adult->Movies is used
  // deliberately: the search (movies/tmdb-search) is NOT gated by the Adult
  // lock and succeeds, while the commit is gated because the group's own
  // (source) mode is Adult — the one case the section-agnostic search-403
  // test structurally can't reach, since there the search itself is what
  // fails.
  it("commit-403 (Adult PIN-locked) is a separate branch from search-403 through the real wiring, stays open with the candidate grid untouched, and never commits — Adult→Movies, where the search succeeds", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([]);
      if (url.includes("/api/modes/adult/dedup/proposals"))
        return jsonResponse([
          dedupProposal({
            id: 40,
            title: "Adult Move Me",
            candidates: [
              candidate({
                label: "tracked",
                path: "/adult/keep.mp4",
                winner: true,
              }),
              candidate({
                label: "orphan.mp4",
                path: "/adult/dupe.mp4",
                winner: false,
              }),
            ],
          }),
        ]);
      if (url.includes("/api/modes/movies/tmdb-search"))
        return jsonResponse([
          { id: 777, title: "Target Movie", releaseDate: "2019-05-01" },
        ]);
      if (url.includes("/api/proposals/40/move-mode")) {
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

    render(() => <Dedup />);
    fireEvent.click(await screen.findByText("Adult"));
    await screen.findByText("Adult Move Me");

    const select = screen.getByLabelText(
      "Move group Adult Move Me to another mode",
    );
    fireEvent.change(select, { target: { value: "movies" } });

    await screen.findByText(/Move .+ to another section/);
    fireEvent.click(screen.getByText("Search"));
    // Adult→MOVIES: searchMode is "movies", so a tile click commits directly
    // (no series slot step).
    fireEvent.click(await screen.findByLabelText("Use Target Movie"));

    // (a) shows the fixed Adult commit-403 copy.
    expect(
      await screen.findByText("Adult is PIN-locked — unlock to continue"),
    ).toBeInTheDocument();
    // (b) never the search-blocked copy — the search succeeded here.
    expect(screen.queryByText(/unlock it to search/)).toBeNull();
    // (c) the takeover stays open with the picked candidate still selected and
    // clickable — unlock-and-retry is one click.
    expect(
      screen.getByText(/Move .+ to another section/),
    ).toBeInTheDocument();
    // The tile renders its title twice (TextPoster art fallback + caption).
    expect(screen.getAllByText("Target Movie").length).toBeGreaterThan(0);
    expect(screen.getByLabelText("Use Target Movie")).toBeInTheDocument();
    // (d) never commits: onDone would close the takeover AND refetch the Adult
    // group list — confirm that second GET never fired. Matched on
    // "proposals?" (the list query string), not a bare substring — a bare
    // "adult/dedup/proposals" substring also matches the per-candidate VMAF
    // endpoint (".../proposals/40/vmaf?..."), which fires independently of
    // this wave's wiring and would falsely inflate the count.
    expect(
      calls.filter((c) => c.url.includes("/api/modes/adult/dedup/proposals?"))
        .length,
    ).toBe(1);
    // (e) the candidate grid this feature must never touch is byte-identical.
    // It is asserted AFTER backing out, not while the takeover is up: the
    // takeover REPLACES the list in place now (it is not the old layered
    // `fixed inset-0` overlay), so the grid is legitimately absent from the
    // document while it is open. Backing out re-renders it from the SAME
    // already-resolved page resource — no refetch — so this still proves the
    // failed commit disturbed nothing.
    fireEvent.click(screen.getByLabelText("Back to list"));
    await waitFor(() =>
      expect(screen.queryByLabelText("Back to list")).toBeNull(),
    );
    expect(screen.getByText("tracked")).toBeInTheDocument();
    expect(screen.getByText("orphan.mp4")).toBeInTheDocument();
    expect(
      calls.filter((c) => c.url.includes("/api/modes/adult/dedup/proposals?"))
        .length,
    ).toBe(1);
  });

  // §6.1/§6.4 of .omc/plans/autopilot-impl-rename-preview.md — SearchTakeover's
  // preview slot is a caller-supplied JSX.Element, and Dedup's Move mount passes
  // NOTHING for it, deliberately: a Dedup proposal carries Candidates[] and no
  // SourcePath at all, so wiring a preview here would 400 on every Move
  // ("candidateIndex is required for this proposal") and render a silent dead
  // player. GUARD THE GUARD FIRST: Dedup's card view renders <video> tiles
  // unconditionally, so if the takeover never actually opened, an absence
  // assertion against `document.querySelector("video")` would pass against a
  // list that also happens to render no video — a double negative proving
  // nothing. The takeover's own heading is the positive anchor that rules that
  // out (it replaces the group list in place, Dedup.tsx).
  it("Dedup's Move takeover renders no video preview", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([dedupProposal({ id: 40, title: "No Preview Here" })]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("No Preview Here");

    const select = screen.getByLabelText(
      "Move group No Preview Here to another mode",
    );
    fireEvent.change(select, { target: { value: "series" } });

    // Positive anchor first: the takeover actually opened.
    expect(
      await screen.findByText(/Move .+ to another section/),
    ).toBeInTheDocument();
    // Now the absence assertions this test exists for.
    expect(document.querySelector("video")).toBeNull();
    expect(screen.queryByText("Preview source file")).toBeNull();
  });
});

// Wave 5 — the full-page search takeover (.omc/plans/autopilot-impl.md §8.5,
// rows N3/N8/N9, plus Dedup copies of N4/N4b). What is new here relative to the
// block above is CONTAINER semantics, not search/commit behaviour:
//
//   N3  the takeover REPLACES the list in place — the group card is genuinely
//       gone, not merely covered by a `fixed inset-0` backdrop.
//   N8  because ModeTabs stays visible and clickable above the takeover (it
//       lives in the Dedup shell, outside DedupView), a mode switch MUST close
//       it — otherwise it stays mounted holding a proposal id from the previous
//       mode and committing would move the wrong group (§1.4's regression).
//   N9  the takeover's stable selectors are the SAME ones SearchTakeover.test
//       and Rename.test assert, proving all three entry points render one
//       component rather than three lookalikes.
//   N4  / N4b — the scroll restore. Dedup's copy of this mechanism is an
//       independent implementation of Rename's, not a shared module, so it can
//       silently drift out of sync with zero coverage catching it. These two
//       are what stop that.
describe("Dedup — search takeover container semantics (N3, N8, N9)", () => {
  it("N3: opening Move replaces the group list in place — the group card is genuinely gone, not overlaid", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([dedupProposal({ id: 50, title: "Vanish Me" })]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("Vanish Me");
    expect(screen.getByText("tracked")).toBeInTheDocument();

    fireEvent.change(
      screen.getByLabelText("Move group Vanish Me to another mode"),
      { target: { value: "series" } },
    );
    await screen.findByText(/Move .+ to another section/);

    // The whole list subtree is un-created, not hidden: the group's own card,
    // its candidate rows, and the queue chrome are all absent. Under the old
    // MoveModeOverlay every one of these would still be in the document,
    // merely painted over — which is exactly what this row exists to rule out.
    expect(
      screen.queryByLabelText("Move group Vanish Me to another mode"),
    ).toBeNull();
    expect(screen.queryByText("tracked")).toBeNull();
    expect(screen.queryByText("orphan.mkv")).toBeNull();
    expect(screen.queryByText("Scan")).toBeNull();
    expect(screen.queryByLabelText("Page size")).toBeNull();
    // ModeTabs is deliberately NOT gone — it lives outside DedupView, which is
    // precisely why N8 below is required.
    expect(screen.getByText("Movies")).toBeInTheDocument();
  });

  it("N8: switching mode while the takeover is open closes it and shows the new mode's queue", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([dedupProposal({ id: 51, title: "Movies Group" })]);
      if (url.includes("/api/modes/series/dedup/proposals"))
        return jsonResponse([dedupProposal({ id: 52, title: "Series Group" })]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("Movies Group");

    fireEvent.change(
      screen.getByLabelText("Move group Movies Group to another mode"),
      { target: { value: "adult" } },
    );
    await screen.findByText(/Move .+ to another section/);

    // ModeTabs is reachable above the takeover, so this is a real user path.
    fireEvent.click(screen.getByText("Series"));

    // resetOnModeChange must clear moveFor. Without that one line the takeover
    // stays mounted holding proposal 51 — a MOVIES id — while the screen is on
    // Series, and committing it would move the wrong group.
    await waitFor(() =>
      expect(screen.queryByLabelText("Back to list")).toBeNull(),
    );
    expect(screen.queryByText(/Move .+ to another section/)).toBeNull();
    expect(await screen.findByText("Series Group")).toBeInTheDocument();
    expect(screen.queryByText("Movies Group")).toBeNull();
  });

  it("N9: the takeover opened from Dedup exposes the same shared stable selectors as every other entry point", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([dedupProposal({ id: 53, title: "Shared Me" })]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("Shared Me");

    fireEvent.change(
      screen.getByLabelText("Move group Shared Me to another mode"),
      { target: { value: "series" } },
    );

    // Exactly the selectors SearchTakeover.test.tsx and Rename.test.tsx use.
    // If Dedup ever grew its own lookalike panel, these would drift and this
    // test is what would catch it.
    expect(await screen.findByLabelText("Back to list")).toBeInTheDocument();
    const input = screen.getByLabelText(
      "Catalog search query",
    ) as HTMLInputElement;
    expect(input).toBeInTheDocument();
    expect(input.value).toBe("Shared Me");
    expect(
      screen.getByRole("region", { name: "Search" }),
    ).toBeInTheDocument();
    // ...and it is NOT a modal: SearchTakeover has no role="dialog" at all.
    expect(screen.queryByRole("dialog")).toBeNull();
  });
});

// renderInMain mounts the screen inside a real <main>, which is what
// Dedup's scroll-restore actually targets: it reaches its host with
// rootEl.closest("main") (AppShell's <main> is the app's only scroll region —
// the shell root is h-screen overflow-hidden, so window.scrollY is always 0).
// Every other test in this file renders hostless on purpose, exercising the
// documented null-host degrade path.
const renderInMain = (): HTMLElement => {
  const main = document.createElement("main");
  document.body.appendChild(main);
  render(() => <Dedup />, { container: main });
  return main;
};

describe("Dedup — takeover scroll restore (N4, N4b)", () => {
  // Dedup-N4 — the back-arrow path. No refetch happens here, so the list
  // re-renders synchronously from the already-resolved page resource.
  it("N4: backing out restores the list scroll offset, with the page size and the grid untouched and no extra GET", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/vmaf")) return jsonResponse({});
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([dedupProposal({ id: 60, title: "Scroll Me" })]);
      throw new Error("unexpected fetch: " + url);
    });

    const main = renderInMain();
    await screen.findByText("Scroll Me");

    // A NON-default page size, so the assertion below proves the restore left
    // the operator's list configuration alone rather than resetting it.
    fireEvent.change(screen.getByLabelText("Page size"), {
      target: { value: "100" },
    });
    await waitFor(() =>
      expect(calls.some((c) => c.url.includes("limit=100"))).toBe(true),
    );
    await screen.findByText("Scroll Me");

    const pageGets = () =>
      calls.filter((c) => c.url.includes("/dedup/proposals?")).length;
    const baseline = pageGets();

    // Set BEFORE the select fires: openTakeover reads the host's scrollTop and
    // only then flips the signal.
    main.scrollTop = 400;
    fireEvent.change(
      screen.getByLabelText("Move group Scroll Me to another mode"),
      { target: { value: "series" } },
    );
    await screen.findByText(/Move .+ to another section/);

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

    expect(await screen.findByText("Scroll Me")).toBeInTheDocument();
    expect(screen.getByText("tracked")).toBeInTheDocument();
    expect(screen.getByText("orphan.mkv")).toBeInTheDocument();
    expect(
      (screen.getByLabelText("Page size") as HTMLSelectElement).value,
    ).toBe("100");
    // The back path refetches nothing — the list comes back off the resolved
    // resource, which is also why the restore can fire immediately here.
    expect(pageGets()).toBe(baseline);
  });

  // Dedup-N4b — the commit path, and the harder of the two. The restore must
  // fire only AFTER page.loading clears; a restore fired straight after
  // setMoveFor(null) lands on the "Loading…" DOM, where a real browser clamps
  // it to ~0 and the offset is silently lost. The mid-flight `=== 0` assertion
  // below is the executable proof that Dedup.tsx's onDone really does
  // batch(refetch-then-setMoveFor) — see the walkthrough in that comment.
  it("N4b: on a successful commit the restore is held until the refetch lands — still 0 mid-flight, 400 only after", async () => {
    let releaseRefetch!: () => void;
    let pageGetCount = 0;
    const group = dedupProposal({ id: 61, title: "Commit Scroll" });

    const calls = stubFetch((url, init) => {
      if (url.includes("/vmaf")) return jsonResponse({});
      if (url.includes("/api/modes/movies/dedup/proposals?")) {
        pageGetCount += 1;
        if (pageGetCount === 1) return jsonResponse([group]);
        // The post-commit refetch is held PENDING and resolved by hand, so the
        // test can observe the in-flight window rather than only the end state.
        return new Promise<Response>((resolve) => {
          releaseRefetch = () => resolve(jsonResponse([group]));
        });
      }
      // Series search queries both catalogs — see the AC6 move test above.
      if (url.includes("/api/modes/movies/tmdb-search")) return jsonResponse([]);
      if (url.includes("/api/modes/series/tmdb-search"))
        return jsonResponse([
          { id: 999, title: "New Show", releaseDate: "2021-01-01" },
        ]);
      if (url.includes("/discover/detail")) return jsonResponse({ seasons: [] });
      if (
        url.includes("/api/proposals/61/move-mode") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    const main = renderInMain();
    await screen.findByText("Commit Scroll");

    main.scrollTop = 400;
    fireEvent.change(
      screen.getByLabelText("Move group Commit Scroll to another mode"),
      { target: { value: "series" } },
    );
    await screen.findByText(/Move .+ to another section/);

    // Same mandatory zeroing as N4 — see that test's comment.
    main.scrollTop = 0;

    fireEvent.click(screen.getByText("Search"));
    fireEvent.click(await screen.findByLabelText("Use New Show"));
    fireEvent.click(await screen.findByText("Use show-level match only"));

    await waitFor(() =>
      expect(
        calls.some((c) => c.url.includes("/api/proposals/61/move-mode")),
      ).toBe(true),
    );
    // The takeover is closed and the refetch is in flight: the queue shows the
    // Loading… fallback, NOT the real table.
    await waitFor(() =>
      expect(screen.getByText("Loading…")).toBeInTheDocument(),
    );

    // THE POINT OF THIS TEST. A plain synchronous read, never wrapped in
    // waitFor — a retrying matcher on a negative proves nothing. If onDone
    // reverted to `setMoveFor(null); void refetch();` (unbatched, setter
    // first), the restore effect would observe page.loading === false, queue
    // `scrollTop = 400` via queueMicrotask, and clear savedScrollTop — and
    // microtasks drain before waitFor's next poll, so this line would read 400
    // and fail. Under the shipped batch(refetch-then-close) ordering the effect
    // observes one consistent state with loading already true, returns early,
    // and the value is still held.
    expect(main.scrollTop).toBe(0);
    expect(screen.queryByText("Commit Scroll")).toBeNull();

    releaseRefetch();

    // Only now, with the real table back, does the restore fire.
    expect(await screen.findByText("Commit Scroll")).toBeInTheDocument();
    await waitFor(() => expect(main.scrollTop).toBe(400));
    expect(screen.getByText("tracked")).toBeInTheDocument();
    expect(screen.getByText("orphan.mkv")).toBeInTheDocument();
  });
});
