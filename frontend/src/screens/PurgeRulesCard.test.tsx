// PurgeRulesCard tests — the Clean-up screen's collapsible Rules card.
//
// Claude 2026-08-11: MOVED here from settings/PruningRules.test.tsx when the
// rules builder relocated off the (now deleted) Settings "Pruning" tab. Every
// block below except the retired scan-interval one is carried forward, adapted
// for the `mode` prop and the removed Mode <select>, and extended for the new
// tags condition.
// Reason: the card is the only rules UI now, so its coverage has to move with
// it rather than be rewritten from scratch.
// Review if: the rules builder ever moves back into Settings.
//
// Conventions mirror RssFeedAdmin.test.tsx/SliderAdmin.test.tsx
// (stubFetch/jsonResponse/Call harness re-declared per test file, not shared).

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { PurgeRulesCard } from "./PurgeRulesCard";
import type { PruningRule } from "../api/pruningRules";

const jsonResponse = (obj: unknown, status = 200): Response =>
  new Response(JSON.stringify(obj), {
    status,
    headers: { "Content-Type": "application/json" },
  });
const noContent = (): Response => new Response(null, { status: 204 });
const errorResponse = (status: number, msg: string): Response =>
  new Response(msg, { status });

type Call = { url: string; method: string; body: unknown };
type Override = (
  url: string,
  init?: RequestInit,
) => Response | undefined | Promise<Response | undefined>;

const rule = (over: Partial<PruningRule> = {}): PruningRule => ({
  id: 1,
  name: "Old low-quality movies",
  mode: "movies",
  ageDays: 0,
  sizeBytes: 0,
  qualityTierFloor: "",
  enabled: true,
  createdAt: "2026-08-01T00:00:00Z",
  updatedAt: "2026-08-01T00:00:00Z",
  ...over,
});

// isRuleList matches exactly GET/POST /api/pruning-rules — never the
// per-id PUT/DELETE (/api/pruning-rules/{id}) nor /preview.
const isRuleList = (url: string) => url.endsWith("/api/pruning-rules");

const stubFetch = (override?: Override) => {
  const calls: Call[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = (init?.method ?? "GET").toUpperCase();
    calls.push({
      url,
      method,
      body: init?.body ? JSON.parse(init.body as string) : undefined,
    });
    if (override) {
      const r = await override(url, init);
      if (r) return r;
    }
    if (method === "GET" && isRuleList(url)) return jsonResponse([]);
    // No purge-scan-interval branch: this tab no longer fetches it (moved to
    // Settings -> Organize), and the absence test below asserts exactly that.
    if (method === "POST" && url.endsWith("/api/pruning-rules/preview"))
      return jsonResponse({ matchCount: 0 });
    return noContent();
  });
  vi.stubGlobal("fetch", fn);
  vi.stubGlobal("confirm", vi.fn(() => true));
  return calls;
};

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("PurgeRulesCard — list", () => {
  it("shows the empty state with no rules", async () => {
    stubFetch();
    render(() => <PurgeRulesCard mode="movies" />);
    expect(
      await screen.findByText("No rules for this mode yet."),
    ).toBeInTheDocument();
  });
});

describe("PurgeRulesCard — create", () => {
  it("creates a rule with a single age condition", async () => {
    const calls = stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (method === "POST" && isRuleList(url))
        return jsonResponse(rule({ id: 5, name: "Stale movies", ageDays: 400 }));
      return undefined;
    });
    render(() => <PurgeRulesCard mode="movies" />);
    fireEvent.click(await screen.findByText("+ New rule"));

    fireEvent.input(screen.getByLabelText("Rule name"), {
      target: { value: "Stale movies" },
    });
    fireEvent.click(screen.getByLabelText("Enable age condition"));
    fireEvent.input(screen.getByLabelText("Age in days"), {
      target: { value: "400" },
    });
    fireEvent.click(screen.getByText("Create rule"));

    await waitFor(() =>
      expect(calls.some((c) => c.method === "POST" && isRuleList(c.url))).toBe(
        true,
      ),
    );
    const post = calls.find((c) => c.method === "POST" && isRuleList(c.url))!;
    expect(post.body).toEqual({
      name: "Stale movies",
      mode: "movies",
      ageDays: 400,
      sizeBytes: 0,
      qualityTierFloor: "",
      tags: [],
      enabled: true,
    });
  });

  it("converts the size input from GB to bytes in the POST body", async () => {
    const calls = stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (method === "POST" && isRuleList(url))
        return jsonResponse(
          rule({ id: 6, name: "Big movies", sizeBytes: 2147483648 }),
        );
      return undefined;
    });
    render(() => <PurgeRulesCard mode="movies" />);
    fireEvent.click(await screen.findByText("+ New rule"));

    fireEvent.input(screen.getByLabelText("Rule name"), {
      target: { value: "Big movies" },
    });
    fireEvent.click(screen.getByLabelText("Enable size condition"));
    fireEvent.input(screen.getByLabelText("Size in GB"), {
      target: { value: "2" },
    });
    fireEvent.click(screen.getByText("Create rule"));

    await waitFor(() =>
      expect(calls.some((c) => c.method === "POST" && isRuleList(c.url))).toBe(
        true,
      ),
    );
    const post = calls.find((c) => c.method === "POST" && isRuleList(c.url))!;
    // 2 GiB, 1024-based — matches internal/pruning's humanBytes convention.
    expect(post.body).toEqual({
      name: "Big movies",
      mode: "movies",
      ageDays: 0,
      sizeBytes: 2147483648,
      qualityTierFloor: "",
      tags: [],
      enabled: true,
    });
  });
});

describe("PurgeRulesCard — edit", () => {
  it("edits an existing rule", async () => {
    const calls = stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (method === "GET" && isRuleList(url))
        return jsonResponse([rule({ id: 7, name: "Old", ageDays: 200 })]);
      if (method === "PUT" && url.endsWith("/api/pruning-rules/7"))
        return jsonResponse(rule({ id: 7, name: "Renamed", ageDays: 200 }));
      return undefined;
    });
    render(() => <PurgeRulesCard mode="movies" />);
    fireEvent.click(await screen.findByLabelText("Edit Old"));

    const nameInput = (await screen.findByLabelText(
      "Rule name",
    )) as HTMLInputElement;
    expect(nameInput.value).toBe("Old");
    fireEvent.input(nameInput, { target: { value: "Renamed" } });
    fireEvent.click(screen.getByText("Save changes"));

    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.method === "PUT" && c.url.endsWith("/api/pruning-rules/7"),
        ),
      ).toBe(true),
    );
    const put = calls.find(
      (c) => c.method === "PUT" && c.url.endsWith("/api/pruning-rules/7"),
    )!;
    expect(put.body).toEqual({
      name: "Renamed",
      mode: "movies",
      ageDays: 200,
      sizeBytes: 0,
      qualityTierFloor: "",
      tags: [],
      enabled: true,
    });
  });
});

describe("PurgeRulesCard — delete", () => {
  it("deletes a rule after confirmation", async () => {
    const calls = stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (method === "GET" && isRuleList(url))
        return jsonResponse([rule({ id: 8, name: "Doomed", ageDays: 30 })]);
      return undefined;
    });
    render(() => <PurgeRulesCard mode="movies" />);
    await screen.findByText(/Doomed/);
    fireEvent.click(screen.getByText("Delete"));

    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.method === "DELETE" && c.url.endsWith("/api/pruning-rules/8"),
        ),
      ).toBe(true),
    );
  });
});

// --- AC1's client half ------------------------------------------------------

describe("PurgeRulesCard — client-side validation (AC1)", () => {
  it("blocks submit when no condition is enabled", async () => {
    const calls = stubFetch();
    render(() => <PurgeRulesCard mode="movies" />);
    fireEvent.click(await screen.findByText("+ New rule"));
    fireEvent.input(screen.getByLabelText("Rule name"), {
      target: { value: "Everything" },
    });
    fireEvent.click(screen.getByText("Create rule"));

    await screen.findByText("select at least one condition");
    expect(calls.some((c) => c.method === "POST" && isRuleList(c.url))).toBe(
      false,
    );
  });

  it("blocks submit when the name is blank", async () => {
    const calls = stubFetch();
    render(() => <PurgeRulesCard mode="movies" />);
    fireEvent.click(await screen.findByText("+ New rule"));
    fireEvent.click(screen.getByLabelText("Enable age condition"));
    fireEvent.input(screen.getByLabelText("Age in days"), {
      target: { value: "10" },
    });
    fireEvent.click(screen.getByText("Create rule"));

    await screen.findByText("name is required");
    expect(calls.some((c) => c.method === "POST" && isRuleList(c.url))).toBe(
      false,
    );
  });
});

describe("PurgeRulesCard — enabled toggle", () => {
  it("toggling enabled fires an immediate update", async () => {
    const calls = stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (method === "GET" && isRuleList(url))
        return jsonResponse([
          rule({ id: 9, name: "Toggle me", ageDays: 100, enabled: true }),
        ]);
      return undefined;
    });
    render(() => <PurgeRulesCard mode="movies" />);
    const toggle = (await screen.findByLabelText(
      "Toggle me enabled",
    )) as HTMLInputElement;
    expect(toggle.checked).toBe(true);
    fireEvent.click(toggle);

    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.method === "PUT" && c.url.endsWith("/api/pruning-rules/9"),
        ),
      ).toBe(true),
    );
    const put = calls.find(
      (c) => c.method === "PUT" && c.url.endsWith("/api/pruning-rules/9"),
    )!;
    expect(put.body).toEqual({
      name: "Toggle me",
      mode: "movies",
      ageDays: 100,
      sizeBytes: 0,
      qualityTierFloor: "",
      tags: [],
      enabled: false,
    });
  });
});

describe("PurgeRulesCard — row mutation error", () => {
  it("surfaces a row-mutation error without clearing the list", async () => {
    stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (method === "GET" && isRuleList(url))
        return jsonResponse([rule({ id: 10, name: "Fragile", ageDays: 50 })]);
      if (method === "PUT" && url.endsWith("/api/pruning-rules/10"))
        return errorResponse(500, "database is locked");
      return undefined;
    });
    render(() => <PurgeRulesCard mode="movies" />);
    const toggle = await screen.findByLabelText("Fragile enabled");
    fireEvent.click(toggle);

    expect(await screen.findByText("database is locked")).toBeInTheDocument();
    // The row survives the failed mutation — the list is never cleared.
    expect(screen.getByText(/Fragile/)).toBeInTheDocument();
  });
});

// --- §13.1 soft preview banner ----------------------------------------------

describe("PurgeRulesCard — soft preview banner (§13.1)", () => {
  it("shows a debounced match-count banner once a condition is configured, and never blocks a large-count save", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const calls = stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (method === "POST" && url.endsWith("/api/pruning-rules/preview"))
        return jsonResponse({ matchCount: 142 });
      if (method === "POST" && isRuleList(url))
        return jsonResponse(rule({ id: 11, name: "Stale", ageDays: 400 }));
      return undefined;
    });
    render(() => <PurgeRulesCard mode="movies" />);
    fireEvent.click(await screen.findByText("+ New rule"));
    fireEvent.click(screen.getByLabelText("Enable age condition"));
    fireEvent.input(screen.getByLabelText("Age in days"), {
      target: { value: "400" },
    });

    // Nothing fires until the debounce elapses.
    expect(
      calls.some((c) => c.url.endsWith("/api/pruning-rules/preview")),
    ).toBe(false);
    await vi.advanceTimersByTimeAsync(300);
    expect(
      calls.some((c) => c.url.endsWith("/api/pruning-rules/preview")),
    ).toBe(true);
    expect(screen.getByTestId("pruning-preview").textContent).toContain(
      "This rule would currently match 142 items.",
    );
    vi.useRealTimers();

    // A high count is a soft warning, never a hard block — Save still works.
    fireEvent.input(screen.getByLabelText("Rule name"), {
      target: { value: "Stale" },
    });
    fireEvent.click(screen.getByText("Create rule"));
    await waitFor(() =>
      expect(calls.some((c) => c.method === "POST" && isRuleList(c.url))).toBe(
        true,
      ),
    );
  });
});

// --- The card shell, mode scoping, and the tags condition (2026-08-11) ------

describe("PurgeRulesCard — the card shell", () => {
  it("is a collapsed <details> with a Rules summary", async () => {
    stubFetch();
    const { container } = render(() => <PurgeRulesCard mode="movies" />);
    await screen.findByText("No rules for this mode yet.");

    const details = container.querySelector("details");
    expect(details).not.toBeNull();
    // Collapsed by default — same as OrganizeChrome's Activity log panel.
    expect((details as HTMLDetailsElement).open).toBe(false);
    expect(details!.querySelector("summary")!.textContent).toBe("Rules");
  });
});

describe("PurgeRulesCard — mode scoping", () => {
  it("renders only rules whose mode matches the prop", async () => {
    stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (method === "GET" && isRuleList(url))
        return jsonResponse([
          rule({ id: 20, name: "Movies rule", mode: "movies", ageDays: 10 }),
          rule({ id: 21, name: "Series rule", mode: "series", ageDays: 10 }),
          rule({ id: 22, name: "Adult rule", mode: "adult", ageDays: 10 }),
        ]);
      return undefined;
    });
    render(() => <PurgeRulesCard mode="adult" />);

    expect(await screen.findByText(/Adult rule/)).toBeInTheDocument();
    expect(screen.queryByText(/Movies rule/)).toBeNull();
    expect(screen.queryByText(/Series rule/)).toBeNull();
  });

  it("creates rules for the prop's mode, with no Mode select to get it wrong", async () => {
    const calls = stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (method === "POST" && isRuleList(url))
        return jsonResponse(rule({ id: 23, name: "S", mode: "series" }));
      return undefined;
    });
    render(() => <PurgeRulesCard mode="series" />);
    fireEvent.click(await screen.findByText("+ New rule"));

    // The dropdown is gone: the card is mounted per-mode, so a rule can no
    // longer be created into a mode whose list is not on screen.
    expect(screen.queryByLabelText("Rule mode")).toBeNull();

    fireEvent.input(screen.getByLabelText("Rule name"), {
      target: { value: "S" },
    });
    fireEvent.click(screen.getByLabelText("Enable age condition"));
    fireEvent.input(screen.getByLabelText("Age in days"), {
      target: { value: "5" },
    });
    fireEvent.click(screen.getByText("Create rule"));

    await waitFor(() =>
      expect(calls.some((c) => c.method === "POST" && isRuleList(c.url))).toBe(
        true,
      ),
    );
    const post = calls.find((c) => c.method === "POST" && isRuleList(c.url))!;
    expect((post.body as { mode: string }).mode).toBe("series");
  });
});

describe("PurgeRulesCard — the tags condition", () => {
  it("adds and removes tags one at a time, firing NO request until Save", async () => {
    const calls = stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (method === "POST" && isRuleList(url))
        return jsonResponse(rule({ id: 30, name: "Tagged" }));
      return undefined;
    });
    render(() => <PurgeRulesCard mode="movies" />);
    fireEvent.click(await screen.findByText("+ New rule"));
    fireEvent.click(screen.getByLabelText("Enable tags condition"));

    const addTag = (value: string) => {
      fireEvent.input(screen.getByLabelText("New tag"), { target: { value } });
      fireEvent.click(screen.getByText("Add"));
    };
    addTag("BDSM");
    addTag("Rope");
    // A duplicate is a no-op, not an error — and case-insensitively so, matching
    // pruning.matchedTags' own comparison.
    addTag("bdsm");

    expect(await screen.findByText("BDSM")).toBeInTheDocument();
    expect(screen.getByText("Rope")).toBeInTheDocument();
    expect(screen.queryByText("bdsm")).toBeNull();

    // Add is CLIENT-SIDE ONLY: unlike the retired allowlist's Add, nothing is
    // persisted until the rule itself is saved.
    expect(
      calls.some((c) => c.method === "POST" && isRuleList(c.url)),
    ).toBe(false);

    fireEvent.click(screen.getByLabelText("Remove Rope"));
    expect(screen.queryByText("Rope")).toBeNull();

    fireEvent.input(screen.getByLabelText("Rule name"), {
      target: { value: "Tagged" },
    });
    fireEvent.click(screen.getByText("Create rule"));

    await waitFor(() =>
      expect(calls.some((c) => c.method === "POST" && isRuleList(c.url))).toBe(
        true,
      ),
    );
    const post = calls.find((c) => c.method === "POST" && isRuleList(c.url))!;
    expect(post.body).toEqual({
      name: "Tagged",
      mode: "movies",
      ageDays: 0,
      sizeBytes: 0,
      qualityTierFloor: "",
      tags: ["BDSM"],
      enabled: true,
    });
  });

  // The tag chip input is the retired Allowlist's AC6 no-bulk invariant in its
  // new home: one × per chip, one Add per input, no clear-all/remove-all.
  it("offers no bulk affordance — one × per chip, one Add, no clear-all", async () => {
    stubFetch();
    render(() => <PurgeRulesCard mode="movies" />);
    fireEvent.click(await screen.findByText("+ New rule"));
    fireEvent.click(screen.getByLabelText("Enable tags condition"));

    for (const value of ["BDSM", "Rope"]) {
      fireEvent.input(screen.getByLabelText("New tag"), { target: { value } });
      fireEvent.click(screen.getByText("Add"));
    }
    await screen.findByText("BDSM");

    expect(screen.getAllByLabelText(/^Remove /)).toHaveLength(2);
    expect(screen.getAllByLabelText("New tag")).toHaveLength(1);
    expect(screen.getAllByText("Add")).toHaveLength(1);
    for (const bulk of [/clear all/i, /remove all/i, /select all/i]) {
      expect(screen.queryByText(bulk)).toBeNull();
    }
  });
});

describe("PurgeRulesCard — client-side validation, tags-only", () => {
  it("accepts a rule whose ONLY condition is tags (the four-way AC1 rule)", async () => {
    const calls = stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (method === "POST" && isRuleList(url))
        return jsonResponse(rule({ id: 31, name: "Legacy allowlist" }));
      return undefined;
    });
    render(() => <PurgeRulesCard mode="movies" />);
    fireEvent.click(await screen.findByText("+ New rule"));
    fireEvent.input(screen.getByLabelText("Rule name"), {
      target: { value: "Legacy allowlist" },
    });
    fireEvent.click(screen.getByLabelText("Enable tags condition"));
    fireEvent.input(screen.getByLabelText("New tag"), {
      target: { value: "Trailer" },
    });
    fireEvent.click(screen.getByText("Add"));
    fireEvent.click(screen.getByText("Create rule"));

    await waitFor(() =>
      expect(calls.some((c) => c.method === "POST" && isRuleList(c.url))).toBe(
        true,
      ),
    );
    expect(screen.queryByText("select at least one condition")).toBeNull();
  });
});

describe("PurgeRulesCard — the enabled toggle preserves tags", () => {
  // The single most likely silent data-loss bug in this feature: toggleEnabled
  // builds a WHOLE-RULE upsert body, so omitting tags would clear a rule's tags
  // every time an operator flipped its enabled checkbox.
  it("carries tags through an enable/disable toggle", async () => {
    const calls = stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (method === "GET" && isRuleList(url))
        return jsonResponse([
          rule({
            id: 32,
            name: "Tagged toggle",
            tags: ["BDSM", "Rope"],
            enabled: true,
          }),
        ]);
      return undefined;
    });
    render(() => <PurgeRulesCard mode="movies" />);
    fireEvent.click(await screen.findByLabelText("Tagged toggle enabled"));

    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.method === "PUT" && c.url.endsWith("/api/pruning-rules/32"),
        ),
      ).toBe(true),
    );
    const put = calls.find(
      (c) => c.method === "PUT" && c.url.endsWith("/api/pruning-rules/32"),
    )!;
    expect(put.body).toEqual({
      name: "Tagged toggle",
      mode: "movies",
      ageDays: 0,
      sizeBytes: 0,
      qualityTierFloor: "",
      tags: ["BDSM", "Rope"],
      enabled: false,
    });
  });
});
