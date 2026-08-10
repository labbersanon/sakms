// US-005 — Rename's "Delete file" action: the first genuinely destructive,
// irreversible action in this app's UI (no trash/undo anywhere). Split out
// from Rename.test.tsx deliberately, same precedent as Rename.repick.test.tsx
// — one feature area, own file, same harness conventions as Rename.test.tsx
// (stubFetch + parsed-body Call tracking; Rename.repick.test.tsx's raw-string
// variant is not needed here since nothing in this file depends on a literal
// serialized-body detail like a dropped `0`).
//
// Two tests here are the frontend regression-test analogue of this codebase's
// move:-before-dismiss catch-all bug class (see Rename.tsx's own comments on
// PLAN_ACTION_LABEL/planActionDetail and the move:* branch in runRowAction):
//
//   - "ApplyAllConfirm shows DELETE FILE ... never Dismiss proposal" guards
//     against the confirm modal silently mis-describing a permanent deletion
//     as a reversible dismiss (the old ternary-with-implicit-else shape).
//   - "confirming a delete-only plan POSTs delete-batch and never
//     apply-batch or dismiss" guards against delete silently being routed
//     through the wrong endpoint.
//
// Both assert on WHICH endpoint/string appears, including explicit absence
// checks — a positive-only assertion would pass against either regression.
//
// Claude 2026-08-09: new file.
// Reason: US-005 — Delete's just-added, and every existing suite silently
//   racing on the shared ApplyAllConfirm modal is not the same as the delete
//   path having its own dedicated coverage.
// Context: .omc/plans/autopilot-impl.md §3 (see Rename.tsx's own header).

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@solidjs/testing-library";
import type { Proposal } from "@dto";
import { Rename, planActionForRow } from "./Rename";

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

const noContent = (): Response => new Response(null, { status: 204 });

// sourcePath is set by default (unlike Rename.test.tsx's builder) — the
// confirm modal's delete detail and the delete-batch request body both
// carry it, and several tests here assert on its exact value.
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

type Call = { url: string; method: string; body: unknown };
type Handler = (url: string, init?: RequestInit) => Response | Promise<Response>;

const pageOf = (items: Proposal[]) => ({
  items,
  total: items.length,
  limit: 50,
  offset: 0,
});

// Verbatim copy of Rename.test.tsx's stubFetch — same harness, same
// boilerplate-route handling (organize/events, pending-ids, the
// array->ProposalPage auto-wrap for /rename/proposals responses).
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
    // Rename Undo's "Recently Applied" list mounts unconditionally — default
    // it to empty so no handler in this file has to know about it.
    if (url.includes("/rename/recently-applied")) return jsonResponse([]);
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

const deleteBatchCalls = (calls: Call[]) =>
  calls.filter((c) => c.url.includes("/api/proposals/delete-batch"));
const applyBatchCalls = (calls: Call[]) =>
  calls.filter((c) => c.url.includes("/api/proposals/apply-batch"));
const dismissCalls = (calls: Call[]) =>
  calls.filter((c) => /\/api\/proposals\/\d+\/dismiss/.test(c.url));

// Locates the "Detail" cell (3rd <td>) of one ApplyAllConfirm row by its
// Original-name cell — used to prove no cross-contamination between rows in
// a mixed plan. Scoped to the confirm dialog: the same sourceName is also
// rendered in the (still-mounted, underneath) proposal table, so an
// unscoped getByText would match two elements.
const detailCellFor = (sourceName: string): string => {
  const dialog = screen.getByRole("dialog", { name: "Confirm apply all" });
  const row = within(dialog).getByText(sourceName).closest("tr")!;
  const cells = row.querySelectorAll("td");
  return cells[2]?.textContent ?? "";
};

afterEach(() => {
  vi.unstubAllGlobals();
  localStorage.clear();
});

describe("Rename — Delete file option eligibility", () => {
  it("renders enabled for pending and unmatched rows, disabled for applied and dismissed rows", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({ id: 1, status: "pending", sourceName: "Pending.mkv" }),
          proposal({
            id: 2,
            status: "unmatched",
            sourceName: "Unmatched.mkv",
            title: "",
          }),
          proposal({ id: 3, status: "applied", sourceName: "Applied.mkv" }),
          proposal({ id: 4, status: "dismissed", sourceName: "Dismissed.mkv" }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("Pending.mkv");

    // Every option is always rendered (For each={actions()} has no
    // conditional), so eligibility is expressed as disabled/not-disabled —
    // the same convention Rename.test.tsx's own "Adult ... Rename disabled"
    // test uses for this exact select. "Absent" for applied/dismissed means
    // the option is present-but-unusable AND the whole select is disabled
    // (rowActionEnabled is false for every action on those two statuses).
    for (const [sourceName, shouldBeUsable] of [
      ["Pending.mkv", true],
      ["Unmatched.mkv", true],
      ["Applied.mkv", false],
      ["Dismissed.mkv", false],
    ] as const) {
      const row = screen.getByText(sourceName).closest("tr")!;
      const select = within(row).getByRole("combobox");
      const option = within(select).getByRole("option", { name: "Delete file" });
      if (shouldBeUsable) {
        expect(option).not.toBeDisabled();
        expect(select).not.toBeDisabled();
      } else {
        expect(option).toBeDisabled();
        expect(select).toBeDisabled();
      }
    }
  });

  it("is never the pre-selected default action for any seeded row status", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({ id: 1, status: "pending", sourceName: "Pending.mkv" }),
          proposal({
            id: 2,
            status: "unmatched",
            sourceName: "Unmatched.mkv",
            title: "",
          }),
          proposal({ id: 3, status: "applied", sourceName: "Applied.mkv" }),
          proposal({ id: 4, status: "dismissed", sourceName: "Dismissed.mkv" }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("Pending.mkv");

    for (const sourceName of [
      "Pending.mkv",
      "Unmatched.mkv",
      "Applied.mkv",
      "Dismissed.mkv",
    ]) {
      const row = screen.getByText(sourceName).closest("tr")!;
      const select = within(row).getByRole("combobox") as HTMLSelectElement;
      expect(select.value).not.toBe("delete");
    }
    // Pending's match auto-selects Rename — the one non-"" default there is.
    const pendingSelect = within(
      screen.getByText("Pending.mkv").closest("tr")!,
    ).getByRole("combobox") as HTMLSelectElement;
    expect(pendingSelect.value).toBe("rename");
  });
});

describe("Rename — ApplyAllConfirm rendering for delete", () => {
  // CRITICAL — see file header. A positive-only "DELETE FILE" assertion
  // would NOT catch a regression to the old ternary-with-dismiss-as-implicit-
  // else bug; the "Dismiss proposal" absence assertion below is the actual
  // point of this test. Mutation-tested — see MUTATION TESTING NOTES at the
  // end of this file.
  it("shows DELETE FILE and the source path, never Dismiss proposal, for a delete-only plan", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({
            id: 1,
            sourceName: "ToDelete.mkv",
            sourcePath: "/movies/ToDelete.mkv",
          }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    const row = (await screen.findByText("ToDelete.mkv")).closest("tr")!;
    fireEvent.change(within(row).getByRole("combobox"), {
      target: { value: "delete" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Apply all" }));

    expect(
      await screen.findByText(/Apply 1 selected action/),
    ).toBeInTheDocument();
    expect(screen.getByText("DELETE FILE")).toBeInTheDocument();
    expect(
      screen.getByText(
        (content) =>
          content.includes("/movies/ToDelete.mkv") &&
          content.includes("Permanently delete") &&
          content.includes("cannot be undone"),
      ),
    ).toBeInTheDocument();
    // THE point of this test.
    expect(screen.queryByText("Dismiss proposal")).toBeNull();
  });

  it("renders three distinct, correct details for a mixed apply/dismiss/delete plan with no cross-contamination", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({
            id: 1,
            sourceName: "Alpha.mkv",
            sourcePath: "/movies/Alpha.mkv",
            title: "Alpha",
            year: 2020,
          }),
          proposal({
            id: 2,
            sourceName: "Beta.mkv",
            sourcePath: "/movies/Beta.mkv",
            title: "Beta",
            year: 2021,
          }),
          proposal({
            id: 3,
            sourceName: "Gamma.mkv",
            sourcePath: "/movies/Gamma.mkv",
            title: "Gamma",
            year: 2022,
          }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("Alpha.mkv");

    // Alpha keeps its auto-selected Rename default; Beta -> dismiss; Gamma -> delete.
    fireEvent.change(
      within(screen.getByText("Beta.mkv").closest("tr")!).getByRole("combobox"),
      { target: { value: "dismiss" } },
    );
    fireEvent.change(
      within(screen.getByText("Gamma.mkv").closest("tr")!).getByRole("combobox"),
      { target: { value: "delete" } },
    );

    fireEvent.click(screen.getByRole("button", { name: "Apply all" }));
    expect(
      await screen.findByText(/Apply 3 selected actions/),
    ).toBeInTheDocument();

    expect(detailCellFor("Alpha.mkv")).toBe("Alpha (2020)");
    expect(detailCellFor("Beta.mkv")).toBe("Dismiss proposal");
    const gammaDetail = detailCellFor("Gamma.mkv");
    expect(gammaDetail).toContain("/movies/Gamma.mkv");
    expect(gammaDetail).toContain("cannot be undone");
    expect(gammaDetail).not.toBe("Dismiss proposal");
    expect(gammaDetail).not.toContain("Alpha");
    expect(gammaDetail).not.toContain("Beta");
  });
});

describe("Rename — confirming a delete plan dispatches the right endpoint(s)", () => {
  // CRITICAL — the frontend analogue of this codebase's move:-before-dismiss
  // catch-all bug class: asserts WHICH endpoint fired, not merely "no error
  // was thrown." Mutation-tested — see MUTATION TESTING NOTES at the end of
  // this file.
  it("confirming a delete-only plan POSTs delete-batch and never apply-batch or dismiss", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({
            id: 5,
            sourceName: "OnlyDelete.mkv",
            sourcePath: "/movies/OnlyDelete.mkv",
          }),
        ]);
      if (
        url.includes("/api/proposals/delete-batch") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return jsonResponse({ results: [{ id: 5, ok: true }] });
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    const row = (await screen.findByText("OnlyDelete.mkv")).closest("tr")!;
    fireEvent.change(within(row).getByRole("combobox"), {
      target: { value: "delete" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Apply all" }));
    await screen.findByText(/Apply 1 selected action/);
    fireEvent.click(screen.getByRole("button", { name: "Confirm" }));

    await waitFor(() => expect(deleteBatchCalls(calls)).toHaveLength(1));
    expect(deleteBatchCalls(calls)[0]!.body).toEqual({
      items: [{ id: 5, sourcePath: "/movies/OnlyDelete.mkv" }],
    });
    expect(applyBatchCalls(calls)).toHaveLength(0);
    expect(dismissCalls(calls)).toHaveLength(0);
  });

  it("a mixed confirm issues apply-batch, per-id dismiss, and ONE delete-batch call for two delete rows", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({
            id: 1,
            sourceName: "Alpha.mkv",
            sourcePath: "/movies/Alpha.mkv",
          }),
          proposal({
            id: 2,
            sourceName: "Beta.mkv",
            sourcePath: "/movies/Beta.mkv",
          }),
          proposal({
            id: 3,
            sourceName: "Gamma.mkv",
            sourcePath: "/movies/Gamma.mkv",
          }),
          proposal({
            id: 4,
            sourceName: "Delta.mkv",
            sourcePath: "/movies/Delta.mkv",
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
      if (
        url.includes("/api/proposals/delete-batch") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return jsonResponse({
          results: [
            { id: 3, ok: true },
            { id: 4, ok: true },
          ],
        });
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("Alpha.mkv");
    // Alpha keeps its auto-selected Rename default.
    fireEvent.change(
      within(screen.getByText("Beta.mkv").closest("tr")!).getByRole("combobox"),
      { target: { value: "dismiss" } },
    );
    fireEvent.change(
      within(screen.getByText("Gamma.mkv").closest("tr")!).getByRole("combobox"),
      { target: { value: "delete" } },
    );
    fireEvent.change(
      within(screen.getByText("Delta.mkv").closest("tr")!).getByRole("combobox"),
      { target: { value: "delete" } },
    );

    fireEvent.click(screen.getByRole("button", { name: "Apply all" }));
    await screen.findByText(/Apply 4 selected actions/);
    fireEvent.click(screen.getByRole("button", { name: "Confirm" }));

    await waitFor(() => expect(applyBatchCalls(calls)).toHaveLength(1));
    // sourcePath rides along on apply items too (Rename.tsx's id-miss
    // re-resolve safety net) — Alpha's default fixture sets one, so it's
    // present here, unlike Rename.test.tsx's own bulk-apply fixtures, which
    // leave sourcePath undefined (and therefore dropped by JSON.stringify).
    expect(applyBatchCalls(calls)[0]!.body).toEqual({
      items: [{ id: 1, sourcePath: "/movies/Alpha.mkv" }],
    });
    await waitFor(() => expect(dismissCalls(calls)).toHaveLength(1));
    expect(dismissCalls(calls)[0]!.url).toContain("/api/proposals/2/dismiss");
    // Exactly ONE delete-batch call, not two — the whole point of batching.
    await waitFor(() => expect(deleteBatchCalls(calls)).toHaveLength(1));
    const items = (deleteBatchCalls(calls)[0]!.body as { items: unknown[] }).items;
    expect(items).toEqual([
      { id: 3, sourcePath: "/movies/Gamma.mkv" },
      { id: 4, sourcePath: "/movies/Delta.mkv" },
    ]);
  });
});

describe("Rename — single-row Delete goes through the shared confirm modal", () => {
  // CRITICAL — drives the flow through the REAL row control: the select set
  // to "delete", then the button whose aria-label is EXACTLY
  // "Apply selected action for <sourceName>" (the row's only button; its
  // visible text is "Apply", never "Delete" — see Rename.tsx's own NOTE FOR
  // TESTS/DOCS comment on the delete branch of runRowAction).
  it("selecting Delete file then clicking the row's Apply button opens the confirm modal and deletes nothing until Confirm", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({
            id: 8,
            sourceName: "RowDelete.mkv",
            sourcePath: "/movies/RowDelete.mkv",
          }),
        ]);
      if (
        url.includes("/api/proposals/delete-batch") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return jsonResponse({ results: [{ id: 8, ok: true }] });
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    const row = (await screen.findByText("RowDelete.mkv")).closest("tr")!;
    fireEvent.change(within(row).getByRole("combobox"), {
      target: { value: "delete" },
    });
    fireEvent.click(
      within(row).getByRole("button", {
        name: "Apply selected action for RowDelete.mkv",
      }),
    );

    expect(
      await screen.findByText(/Apply 1 selected action/),
    ).toBeInTheDocument();
    // Nothing dispatched yet — opening the modal is not deleting.
    expect(deleteBatchCalls(calls)).toHaveLength(0);
    expect(applyBatchCalls(calls)).toHaveLength(0);
    expect(dismissCalls(calls)).toHaveLength(0);

    fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
    await waitFor(() => expect(deleteBatchCalls(calls)).toHaveLength(1));
    expect(deleteBatchCalls(calls)[0]!.body).toEqual({
      items: [{ id: 8, sourcePath: "/movies/RowDelete.mkv" }],
    });
  });

  it("Cancel on the single-row delete confirm issues zero requests", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({
            id: 9,
            sourceName: "CancelDelete.mkv",
            sourcePath: "/movies/CancelDelete.mkv",
          }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    const row = (await screen.findByText("CancelDelete.mkv")).closest("tr")!;
    fireEvent.change(within(row).getByRole("combobox"), {
      target: { value: "delete" },
    });
    fireEvent.click(
      within(row).getByRole("button", {
        name: "Apply selected action for CancelDelete.mkv",
      }),
    );
    expect(
      await screen.findByText(/Apply 1 selected action/),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() =>
      expect(screen.queryByText(/Apply 1 selected action/)).toBeNull(),
    );

    expect(deleteBatchCalls(calls)).toHaveLength(0);
    expect(applyBatchCalls(calls)).toHaveLength(0);
    expect(dismissCalls(calls)).toHaveLength(0);
  });
});

describe("Rename — delete failure surfaces in the batch result summary", () => {
  it('a failed delete shows "N failed" with the error text visible', async () => {
    stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({
            id: 6,
            sourceName: "Locked.mkv",
            sourcePath: "/movies/Locked.mkv",
          }),
        ]);
      if (
        url.includes("/api/proposals/delete-batch") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return jsonResponse({
          results: [{ id: 6, ok: false, error: "permission denied" }],
        });
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    const row = (await screen.findByText("Locked.mkv")).closest("tr")!;
    fireEvent.change(within(row).getByRole("combobox"), {
      target: { value: "delete" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Apply all" }));
    await screen.findByText(/Apply 1 selected action/);
    fireEvent.click(screen.getByRole("button", { name: "Confirm" }));

    expect(await screen.findByText(/1 failed/)).toBeInTheDocument();
    expect(screen.getByText(/0 applied, 1 failed/)).toBeInTheDocument();
    expect(screen.getByText(/permission denied/)).toBeInTheDocument();
  });
});

describe("Rename — planActionForRow guards stale/invalid selections", () => {
  it('returns null for an ineligible status ("applied") with action="delete"', () => {
    const applied = proposal({ id: 10, status: "applied" });
    expect(planActionForRow(applied, "delete", true)).toBeNull();
    expect(planActionForRow(applied, "delete", false)).toBeNull();
  });
});

// MUTATION TESTING NOTES (US-005 verification, not part of the suite):
//
// 1. "shows DELETE FILE ... never Dismiss proposal" (ApplyAllConfirm block
//    above) — temporarily reverted Rename.tsx's planActionDetail delete case
//    to `return "Dismiss proposal";` (simulating the old implicit-else
//    ternary bug). Result: RED — "Dismiss proposal" assertion failed
//    (queryByText returned the element instead of null). Restored the real
//    switch case: GREEN.
//
// 2. "confirming a delete-only plan POSTs delete-batch and never
//    apply-batch or dismiss" — temporarily changed confirmApplyAll's
//    `toDelete.length > 0` branch to call `applyBatchStreaming` with the
//    delete items rewritten as ApplyBatchItems instead of `deleteBatch`.
//    Result: RED — `applyBatchCalls` was non-empty. Restored the real
//    `deleteBatch` call: GREEN.
//
// Full commands and output are in the executor's final report.
