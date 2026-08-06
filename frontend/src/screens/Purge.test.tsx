// Stage 3 Purge UI tests — the staged scan→propose→apply DELETE queue plus the
// Purge-only tag allowlist, per mode. Purge has two mutating surfaces with
// DIFFERENT bulk policies now, and the tests assert each:
//   - PROPOSALS gained the bounded bulk-apply exception (a deliberate,
//     documented reversal — see ROADMAP.md and the top-level CLAUDE.md): an
//     opt-in multi-select of Pending delete rows applied in ONE apply-batch,
//     behind the same window.confirm the single delete has, worded for the
//     count. Single-item delete still works one row at a time.
//   - The ALLOWLIST stays deliberately bulk-free — one × per chip, one Add per
//     input, no clear-all/remove-all. That half's no-bulk test is unchanged.
//
// Covered: Movies apply-one (behind the confirm guard) + the confirm CANCEL
// branch (no apply fires), Dismiss, Scan→refetch, bulk apply on proposals
// (checkbox gating, confirm guard incl. cancel, one apply-batch not N singles,
// selection clears), the no-bulk invariant on the allowlist (one × per chip =
// one DELETE, one Add acting on one tag, no clear-all / remove-all affordance),
// and Series/Adult allowlist add/remove wiring.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import type { Proposal } from "@dto";
import { Purge } from "./Purge";

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

const asPage = (items: unknown[]) => ({
  items,
  total: items.length,
  limit: 50,
  offset: 0,
});

const noContent = (): Response => new Response(null, { status: 204 });

const proposal = (over: Partial<Proposal>): Proposal => ({
  id: 1,
  status: "pending",
  sourceName: "Some Movie",
  rootFolderPath: "/movies",
  title: "Some Movie",
  year: 2021,
  reason: "matched allowlist tag(s): Trailer",
  draftId: "",
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
    if (url.includes("/purge/proposals") && res.headers.get("Content-Type")?.includes("json")) {
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

// Default allowlist stub so every render's GET .../purge/allowlist resolves.
const emptyAllowlist = (url: string): Response | null =>
  url.includes("/purge/allowlist") ? jsonResponse([]) : null;

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("Purge — Movies (scan → propose → apply one, with confirm guard)", () => {
  it("applies exactly one proposal when the delete confirm is accepted", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const calls = stubFetch((url, init) => {
      const al = emptyAllowlist(url);
      if (al) return al;
      if (url.includes("/api/modes/movies/purge/proposals"))
        return jsonResponse([proposal({ id: 7, title: "Delete Me" })]);
      if (
        url.includes("/api/proposals/7/apply") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Purge />);
    expect(await screen.findByText("Delete Me")).toBeInTheDocument();
    fireEvent.click(await screen.findByText("Apply (Delete)"));

    await waitFor(() => expect(applyCalls(calls)).toHaveLength(1));
    expect(applyCalls(calls)[0]!.url).toContain("/api/proposals/7/apply");
    expect(applyCalls(calls)[0]!.method).toBe("POST");
    expect(window.confirm).toHaveBeenCalledOnce();
  });

  it("does NOT apply when the delete confirm is cancelled (guard branch)", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(false);
    const calls = stubFetch((url) => {
      const al = emptyAllowlist(url);
      if (al) return al;
      if (url.includes("/api/modes/movies/purge/proposals"))
        return jsonResponse([proposal({ id: 7, title: "Keep Me" })]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Purge />);
    await screen.findByText("Keep Me");
    fireEvent.click(screen.getByText("Apply (Delete)"));

    // Confirm was consulted, but no apply request ever fired.
    await waitFor(() => expect(window.confirm).toHaveBeenCalledOnce());
    expect(applyCalls(calls)).toHaveLength(0);
  });

  it("triggers a scan then re-fetches the queue on the Scan button", async () => {
    let scanned = false;
    const calls = stubFetch((url, init) => {
      const al = emptyAllowlist(url);
      if (al) return al;
      if (
        url.includes("/api/modes/movies/purge/scan") &&
        (init?.method ?? "").toUpperCase() === "POST"
      ) {
        scanned = true;
        return noContent();
      }
      if (url.includes("/api/modes/movies/purge/proposals"))
        return jsonResponse(
          scanned ? [proposal({ id: 1, title: "Found After Scan" })] : [],
        );
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Purge />);
    expect(await screen.findByText(/No proposals yet/)).toBeInTheDocument();
    fireEvent.click(screen.getByText("Scan"));
    expect(await screen.findByText("Found After Scan")).toBeInTheDocument();
    expect(
      calls.some((c) => c.url.includes("/purge/scan") && c.method === "POST"),
    ).toBe(true);
  });
});

describe("Purge — Apply double-click guard (in-flight busy state)", () => {
  it("fires exactly one apply request when the same row's Apply button is double-clicked while the first request is still pending", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    let resolveApply: (() => void) | undefined;
    const applyGate = new Promise<void>((resolve) => {
      resolveApply = resolve;
    });
    const calls = stubFetch(async (url, init) => {
      const al = emptyAllowlist(url);
      if (al) return al;
      if (url.includes("/api/modes/movies/purge/proposals"))
        return jsonResponse([proposal({ id: 7, title: "Delete Me" })]);
      if (
        url.includes("/api/proposals/7/apply") &&
        (init?.method ?? "").toUpperCase() === "POST"
      ) {
        await applyGate; // held open until the test resolves it
        return noContent();
      }
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Purge />);
    await screen.findByText("Delete Me");

    // Two rapid clicks on the SAME row's Apply button before the first
    // request resolves — only one apply call should ever fire, and the
    // button should reflect the pending state in between.
    fireEvent.click(screen.getByText("Apply (Delete)"));
    expect(await screen.findByText("Deleting…")).toBeInTheDocument();
    fireEvent.click(screen.getByText("Deleting…"));

    resolveApply?.();
    await waitFor(() => expect(applyCalls(calls)).toHaveLength(1));
    expect(applyCalls(calls)[0]!.url).toContain("/api/proposals/7/apply");
    // The confirm guard is only consulted once too — the second click never
    // reached it, since the in-flight check short-circuits first.
    expect(window.confirm).toHaveBeenCalledOnce();
    // Once the request settles, the row's busy flag clears and the button
    // reverts to its normal label (this stub's GET still returns the same
    // pending row, so the label — not the row's presence — is what proves
    // the busy state cleared rather than being stuck permanently).
    expect(await screen.findByText("Apply (Delete)")).toBeInTheDocument();
  });
});

describe("Purge — Dismiss (single row)", () => {
  it("dismisses exactly one proposal", async () => {
    const calls = stubFetch((url, init) => {
      const al = emptyAllowlist(url);
      if (al) return al;
      if (url.includes("/api/modes/movies/purge/proposals"))
        return jsonResponse([proposal({ id: 4, title: "Dismiss Me" })]);
      if (
        url.includes("/api/proposals/4/dismiss") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Purge />);
    await screen.findByText("Dismiss Me");
    fireEvent.click(screen.getByText("Dismiss"));
    await waitFor(() =>
      expect(
        calls.some((c) => c.url.includes("/api/proposals/4/dismiss")),
      ).toBe(true),
    );
  });
});

describe("Purge — bulk apply on PROPOSALS (opt-in multi-select, confirm-guarded)", () => {
  it("renders a checkbox only for Pending rows, never for a non-pending one", async () => {
    stubFetch((url) => {
      const al = emptyAllowlist(url);
      if (al) return al;
      if (url.includes("/api/modes/movies/purge/proposals"))
        return jsonResponse([
          proposal({ id: 1, title: "A", status: "pending" }),
          proposal({ id: 2, title: "B", status: "pending" }),
          proposal({ id: 3, title: "C", status: "unmatched" }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Purge />);
    await screen.findByText("A");

    expect(screen.getByLabelText("Select A")).toBeInTheDocument();
    expect(screen.getByLabelText("Select B")).toBeInTheDocument();
    expect(screen.queryByLabelText("Select C")).toBeNull();
    // Two pending + select-all + Show history = 4.
    expect(document.querySelectorAll('input[type="checkbox"]')).toHaveLength(4);
  });

  it("deletes several selected rows in ONE apply-batch behind a count-worded confirm, then clears the selection", async () => {
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
    const calls = stubFetch((url, init) => {
      const al = emptyAllowlist(url);
      if (al) return al;
      if (url.includes("/api/modes/movies/purge/proposals"))
        return jsonResponse([
          proposal({ id: 1, title: "A" }),
          proposal({ id: 2, title: "B" }),
          proposal({ id: 3, title: "C" }),
        ]);
      if (
        url.includes("/api/proposals/apply-batch") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return jsonResponse({
          results: [
            { id: 1, ok: true },
            { id: 3, ok: true },
          ],
        });
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Purge />);
    await screen.findByText("A");

    fireEvent.click(screen.getByLabelText("Select A"));
    fireEvent.click(screen.getByLabelText("Select C"));
    fireEvent.click(await screen.findByText("Apply Selected (2)"));

    // Confirm was consulted with the count, then exactly ONE apply-batch fired.
    expect(confirmSpy).toHaveBeenCalledOnce();
    expect(confirmSpy.mock.calls[0]![0]).toContain("Delete 2 items");
    await waitFor(() => expect(batchCalls(calls)).toHaveLength(1));
    expect(singleApplyCalls(calls)).toHaveLength(0);
    expect(batchCalls(calls)[0]!.body).toEqual({
      items: [{ id: 1 }, { id: 3 }],
    });
    expect(await screen.findByText("2 applied, 0 failed")).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByText(/Apply Selected/)).toBeNull());
  });

  it("does NOT fire an apply-batch when the bulk confirm is cancelled", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(false);
    const calls = stubFetch((url) => {
      const al = emptyAllowlist(url);
      if (al) return al;
      if (url.includes("/api/modes/movies/purge/proposals"))
        return jsonResponse([
          proposal({ id: 1, title: "A" }),
          proposal({ id: 2, title: "B" }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Purge />);
    await screen.findByText("A");
    fireEvent.click(screen.getByLabelText("Select A"));
    fireEvent.click(await screen.findByText("Apply Selected (1)"));

    await waitFor(() => expect(window.confirm).toHaveBeenCalledOnce());
    expect(batchCalls(calls)).toHaveLength(0);
    // Selection survives a cancelled confirm — the button is still shown.
    expect(screen.getByText("Apply Selected (1)")).toBeInTheDocument();
  });
});

describe("Purge — no bulk actions on the ALLOWLIST (Acceptance Criterion 6)", () => {
  it("removes exactly one tag per × click and offers no clear-all affordance", async () => {
    const removeCalls: Call[] = [];
    const calls = stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (url.includes("/purge/allowlist/") && method === "DELETE") {
        removeCalls.push({ url, method, body: undefined });
        return noContent();
      }
      if (url.includes("/purge/allowlist") && method === "GET")
        return jsonResponse(["Trailer", "Sample", "Extras"]);
      if (url.includes("/api/modes/movies/purge/proposals"))
        return jsonResponse([]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Purge />);
    await screen.findByText("Trailer");

    // One × per chip, three chips — never a single bulk control.
    const removeButtons = screen.getAllByText("×");
    expect(removeButtons).toHaveLength(3);
    expect(screen.queryByText(/clear all/i)).toBeNull();
    expect(screen.queryByText(/remove all/i)).toBeNull();
    // No selection checkboxes in the allowlist — only the Show history toggle.
    expect(screen.getByText("Show history")).toBeInTheDocument();
    expect(document.querySelectorAll('input[type="checkbox"]')).toHaveLength(1);

    // Removing one chip issues exactly one DELETE, for exactly that tag.
    fireEvent.click(removeButtons[1]!);
    await waitFor(() => expect(removeCalls).toHaveLength(1));
    expect(removeCalls[0]!.url).toContain("/purge/allowlist/Sample");
    expect(removeCalls[0]!.method).toBe("DELETE");
    // No stray DELETE for any other tag.
    expect(calls.filter((c) => c.method === "DELETE")).toHaveLength(1);
  });

  it("adds exactly one tag from the single input (no multi-add path)", async () => {
    let added = false;
    const calls = stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (url.includes("/purge/allowlist") && method === "POST") {
        added = true;
        return noContent();
      }
      if (url.includes("/purge/allowlist") && method === "GET")
        return jsonResponse(added ? ["Behindthescenes"] : []);
      if (url.includes("/api/modes/movies/purge/proposals"))
        return jsonResponse([]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Purge />);
    const input = await screen.findByLabelText("New allowlist tag");
    fireEvent.input(input, { target: { value: "Behindthescenes" } });
    fireEvent.click(screen.getByText("Add"));

    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.url.includes("/purge/allowlist") && c.method === "POST",
        ),
      ).toBe(true),
    );
    const post = calls.find(
      (c) => c.url.includes("/purge/allowlist") && c.method === "POST",
    );
    expect(post?.body).toMatchObject({ tag: "Behindthescenes" });
    // Exactly one POST — the single input never fans out to multiple tags.
    expect(
      calls.filter(
        (c) => c.url.includes("/purge/allowlist") && c.method === "POST",
      ),
    ).toHaveLength(1);
    // The added chip shows after the allowlist refetch.
    expect(await screen.findByText("Behindthescenes")).toBeInTheDocument();
  });
});

describe("Purge — Adult allowlist (per-mode wiring)", () => {
  it("targets the adult allowlist endpoints when the Adult tab is active", async () => {
    const removeCalls: Call[] = [];
    stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (url.includes("/api/modes/movies/purge/")) {
        // Movies renders first; keep both resources empty/quiet.
        return url.includes("proposals") ? jsonResponse([]) : jsonResponse([]);
      }
      if (url.includes("/api/modes/adult/purge/allowlist/") && method === "DELETE") {
        removeCalls.push({ url, method, body: undefined });
        return noContent();
      }
      if (url.includes("/api/modes/adult/purge/allowlist") && method === "GET")
        return jsonResponse(["Compilation"]);
      if (url.includes("/api/modes/adult/purge/proposals"))
        return jsonResponse([]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Purge />);
    fireEvent.click(await screen.findByText("Adult"));
    await screen.findByText("Compilation");
    fireEvent.click(screen.getByText("×"));
    await waitFor(() => expect(removeCalls).toHaveLength(1));
    expect(removeCalls[0]!.url).toContain(
      "/api/modes/adult/purge/allowlist/Compilation",
    );
  });
});
