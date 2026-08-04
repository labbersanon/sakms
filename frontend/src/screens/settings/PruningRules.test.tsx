// PruningRules tests — the Settings "Pruning" tab (F1, plan
// .omc/plans/autopilot-impl-pruning-rules.md §9.7 + §13.1). Conventions
// mirror RssFeedAdmin.test.tsx/SliderAdmin.test.tsx (stubFetch/jsonResponse/
// Call harness re-declared per test file, not shared). Covers the §9.7 list
// (empty state, create with an age condition, GB->bytes conversion, edit,
// delete, both AC1 client-side blocks, the immediate enabled-toggle update,
// the purge scan interval, and a row-mutation error that doesn't clear the
// list) plus the §13.1 soft preview banner, which §9.7 doesn't separately
// enumerate but the plan's Critic safety amendment requires.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { PruningRulesSection } from "./PruningRules";
import type { PruningRule } from "../../api/pruningRules";

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
    if (method === "GET" && url.includes("/api/settings/purge-scan-interval"))
      return jsonResponse({ intervalSeconds: 0 });
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

describe("PruningRulesSection — list", () => {
  it("shows the empty state with no rules", async () => {
    stubFetch();
    render(() => <PruningRulesSection />);
    expect(await screen.findByText("No pruning rules yet.")).toBeInTheDocument();
  });
});

describe("PruningRulesSection — create", () => {
  it("creates a rule with a single age condition", async () => {
    const calls = stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (method === "POST" && isRuleList(url))
        return jsonResponse(rule({ id: 5, name: "Stale movies", ageDays: 400 }));
      return undefined;
    });
    render(() => <PruningRulesSection />);
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
    render(() => <PruningRulesSection />);
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
      enabled: true,
    });
  });
});

describe("PruningRulesSection — edit", () => {
  it("edits an existing rule", async () => {
    const calls = stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (method === "GET" && isRuleList(url))
        return jsonResponse([rule({ id: 7, name: "Old", ageDays: 200 })]);
      if (method === "PUT" && url.endsWith("/api/pruning-rules/7"))
        return jsonResponse(rule({ id: 7, name: "Renamed", ageDays: 200 }));
      return undefined;
    });
    render(() => <PruningRulesSection />);
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
      enabled: true,
    });
  });
});

describe("PruningRulesSection — delete", () => {
  it("deletes a rule after confirmation", async () => {
    const calls = stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (method === "GET" && isRuleList(url))
        return jsonResponse([rule({ id: 8, name: "Doomed", ageDays: 30 })]);
      return undefined;
    });
    render(() => <PruningRulesSection />);
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

describe("PruningRulesSection — client-side validation (AC1)", () => {
  it("blocks submit when no condition is enabled", async () => {
    const calls = stubFetch();
    render(() => <PruningRulesSection />);
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
    render(() => <PruningRulesSection />);
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

describe("PruningRulesSection — enabled toggle", () => {
  it("toggling enabled fires an immediate update", async () => {
    const calls = stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (method === "GET" && isRuleList(url))
        return jsonResponse([
          rule({ id: 9, name: "Toggle me", ageDays: 100, enabled: true }),
        ]);
      return undefined;
    });
    render(() => <PruningRulesSection />);
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
      enabled: false,
    });
  });
});

describe("PruningRulesSection — row mutation error", () => {
  it("surfaces a row-mutation error without clearing the list", async () => {
    stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (method === "GET" && isRuleList(url))
        return jsonResponse([rule({ id: 10, name: "Fragile", ageDays: 50 })]);
      if (method === "PUT" && url.endsWith("/api/pruning-rules/10"))
        return errorResponse(500, "database is locked");
      return undefined;
    });
    render(() => <PruningRulesSection />);
    const toggle = await screen.findByLabelText("Fragile enabled");
    fireEvent.click(toggle);

    expect(await screen.findByText("database is locked")).toBeInTheDocument();
    // The row survives the failed mutation — the list is never cleared.
    expect(screen.getByText(/Fragile/)).toBeInTheDocument();
  });
});

describe("PruningRulesSection — purge scan interval (AC3's frontend gap)", () => {
  it("renders the purge scan interval and PUTs a new value", async () => {
    const calls = stubFetch();
    render(() => <PruningRulesSection />);
    // Value 0 defaults the picker to the "Hours" unit (same convention as
    // AdultRowAdmin's scan-interval control); typing "1" there means 1 hour
    // = 3600 seconds.
    const input = (await screen.findByLabelText(
      "Purge scan interval",
    )) as HTMLInputElement;
    fireEvent.input(input, { target: { value: "1" } });
    fireEvent.click(screen.getByText("Save"));

    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "PUT" &&
            c.url.includes("/api/settings/purge-scan-interval"),
        ),
      ).toBe(true),
    );
    const put = calls.find(
      (c) =>
        c.method === "PUT" && c.url.includes("/api/settings/purge-scan-interval"),
    )!;
    expect(put.body).toEqual({ intervalSeconds: 3600 });
  });
});

// --- §13.1 soft preview banner ----------------------------------------------

describe("PruningRulesSection — soft preview banner (§13.1)", () => {
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
    render(() => <PruningRulesSection />);
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
