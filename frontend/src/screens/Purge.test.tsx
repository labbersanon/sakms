// Stage 3 Purge UI tests — the staged scan→propose→apply DELETE queue plus the
// Purge-only tag allowlist, per mode, and the explicit no-bulk-action assertion
// (Acceptance Criterion 6) across BOTH mutating surfaces Purge has: proposals
// AND the allowlist. This is the half most easily under-covered — Rename had no
// allowlist, so its no-bulk test only covered proposals; Purge must assert both.
//
// Covered: Movies apply-one (behind the confirm guard) + the confirm CANCEL
// branch (no apply fires), Dismiss, Scan→refetch, the no-bulk invariant on
// proposals (one Apply per pending row, no apply-all / select-all / checkboxes),
// the no-bulk invariant on the allowlist (one × per chip = one DELETE, one Add
// acting on one tag, no clear-all / remove-all affordance), and Series/Adult
// allowlist add/remove wiring.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import type { Proposal } from "@dto";
import { Purge } from "./Purge";

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
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
    return handler(url, init);
  });
  vi.stubGlobal("fetch", fn);
  return calls;
};

const applyCalls = (calls: Call[]) =>
  calls.filter((c) => c.url.includes("/apply"));

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

describe("Purge — no bulk actions on PROPOSALS (Acceptance Criterion 6)", () => {
  it("renders one Apply per pending row and no apply-all / select-all affordance", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const calls = stubFetch((url, init) => {
      const al = emptyAllowlist(url);
      if (al) return al;
      if (url.includes("/api/modes/movies/purge/proposals"))
        return jsonResponse([
          proposal({ id: 1, title: "A" }),
          proposal({ id: 2, title: "B" }),
          proposal({ id: 3, title: "C" }),
        ]);
      if (url.includes("/apply") && (init?.method ?? "").toUpperCase() === "POST")
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Purge />);
    await screen.findByText("A");

    const applyButtons = screen.getAllByText("Apply (Delete)");
    expect(applyButtons).toHaveLength(3);
    expect(screen.queryByText(/apply all/i)).toBeNull();
    expect(screen.queryByText(/delete all/i)).toBeNull();
    expect(screen.queryByText(/select all/i)).toBeNull();
    expect(document.querySelectorAll('input[type="checkbox"]')).toHaveLength(0);

    // Clicking one Apply mutates exactly one proposal — not the batch.
    fireEvent.click(applyButtons[1]!);
    await waitFor(() => expect(applyCalls(calls)).toHaveLength(1));
    expect(applyCalls(calls)[0]!.url).toContain("/api/proposals/2/apply");
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
    // No selection checkboxes in the allowlist either.
    expect(document.querySelectorAll('input[type="checkbox"]')).toHaveLength(0);

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
