// Clean-up UI tests — the staged scan→propose→apply DELETE queue, per mode.
// (The screen, its route and its component are all still named `Purge`
// internally; only the operator-facing text says "Clean-up".)
//
// PROPOSALS carry the bounded bulk-apply exception (a deliberate, documented
// reversal — see ROADMAP.md and the top-level CLAUDE.md): an opt-in
// multi-select of Pending delete rows applied in ONE apply-batch, behind the
// same window.confirm the single delete has, worded for the count.
// Single-item delete still works one row at a time.
//
// Claude 2026-08-11: the per-mode tag ALLOWLIST this file used to exercise is
// retired; its section is replaced by <PurgeRulesCard>.
// Reason: tags became a fourth AND'd condition on a pruning rule. The
// allowlist's own AC6 no-bulk invariant was NOT dropped — it moved with the
// mechanism, to PurgeRulesCard.test.tsx's "offers no bulk affordance" block,
// which asserts the same one-×-per-chip/one-Add/no-clear-all property on the
// rule form's tag input.
// Troubleshooting: PurgeRulesCard fetches GET /api/pruning-rules on MOUNT even
// though its <details> is collapsed (createResource does not wait for the
// disclosure), so every render here needs that stub or the fetch mock throws
// on an unexpected URL.
// Review if: Clean-up ever grows a second matching mechanism.
//
// Covered: Movies apply-one (behind the confirm guard) + the confirm CANCEL
// branch (no apply fires), Dismiss, Scan→refetch, bulk apply on proposals
// (checkbox gating, confirm guard incl. cancel, one apply-batch not N singles,
// selection clears), and the Rules card's per-mode scoping.

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
  reason: "Matched rule 'Legacy allowlist': tags: Trailer",
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

// Default rules stub so every render's GET /api/pruning-rules resolves —
// PurgeRulesCard fetches on mount even while its <details> is collapsed.
const emptyRules = (url: string): Response | null =>
  url.includes("/api/pruning-rules") ? jsonResponse([]) : null;

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("Purge — Movies (scan → propose → apply one, with confirm guard)", () => {
  it("applies exactly one proposal when the delete confirm is accepted", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const calls = stubFetch((url, init) => {
      const rules = emptyRules(url);
      if (rules) return rules;
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
      const rules = emptyRules(url);
      if (rules) return rules;
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
      const rules = emptyRules(url);
      if (rules) return rules;
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
      const rules = emptyRules(url);
      if (rules) return rules;
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
      const rules = emptyRules(url);
      if (rules) return rules;
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
      const rules = emptyRules(url);
      if (rules) return rules;
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
      const rules = emptyRules(url);
      if (rules) return rules;
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
      const rules = emptyRules(url);
      if (rules) return rules;
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

// Claude 2026-08-11: the two AC6 "no bulk actions on the ALLOWLIST" tests that
// lived here MOVED to PurgeRulesCard.test.tsx ("offers no bulk affordance —
// one × per chip, one Add, no clear-all") along with the chip input they
// exercised, rather than being deleted.
// Reason: the allowlist section is gone from this screen; the same invariant
// now belongs to the rule form's tags condition, which is where the operator
// types a tag now.
// Review if: any bulk tag affordance is ever proposed for the rules card.

describe("Clean-up — the Rules card (per-mode wiring)", () => {
  it("renders the Adult mode's rules when the Adult tab is active", async () => {
    stubFetch((url) => {
      if (url.includes("/api/pruning-rules"))
        return jsonResponse([
          {
            id: 1,
            name: "Adult trailers",
            mode: "adult",
            tags: ["Trailer"],
            enabled: true,
            createdAt: "",
            updatedAt: "",
          },
          {
            id: 2,
            name: "Movies trailers",
            mode: "movies",
            tags: ["Trailer"],
            enabled: true,
            createdAt: "",
            updatedAt: "",
          },
        ]);
      if (url.includes("/purge/proposals")) return jsonResponse([]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Purge />);
    fireEvent.click(await screen.findByText("Adult"));

    // Scoped by the mode tab, not by a Mode dropdown — and it reads
    // /api/pruning-rules, never a per-mode allowlist route.
    expect(await screen.findByText(/Adult trailers/)).toBeInTheDocument();
    expect(screen.queryByText(/Movies trailers/)).toBeNull();
  });
});
