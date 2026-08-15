// PurgeRulesCard tests — the Clean-up screen's collapsible Rules card.
//
// Claude 2026-08-11: MOVED here from settings/PruningRules.test.tsx when the
// rules builder relocated off the (now deleted) Settings "Pruning" tab.
// Claude 2026-08-14: checkbox conditions replaced by AND-criteria rows
// (field / operator / value / unit). POST/PUT bodies send `criteria` and
// zero the five scalar fields. Filling the empty row appends another;
// an in-progress row does not. Tag rows send values[] + matchMode (chips
// + Any/All); empty chips block save.
// Review if: the rules builder ever moves back into Settings.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { PurgeRulesCard } from "./PurgeRulesCard";
import type { PruningCriterion, PruningRule } from "../api/pruningRules";
import { jsonResponse, noContent } from "../testing/http";

const errorResponse = (status: number, msg: string): Response =>
  new Response(msg, { status });

type Call = { url: string; method: string; body: unknown };
type Override = (
  url: string,
  init?: RequestInit,
) => Response | undefined | Promise<Response | undefined>;

const ageRow = (value: string): PruningCriterion => ({
  field: "age",
  op: "gt",
  value,
  unit: "days",
});

const tagRow = (
  values: string[],
  over: Partial<PruningCriterion> = {},
): PruningCriterion => ({
  field: "tag",
  op: "contains",
  value: "",
  values,
  matchMode: "any",
  ...over,
});

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

const upsertBody = (over: Record<string, unknown>) => ({
  name: "",
  mode: "movies",
  ageDays: 0,
  sizeBytes: 0,
  qualityTierFloor: "",
  tags: [],
  minRating: 0,
  criteria: [],
  enabled: true,
  ...over,
});

const isRuleList = (url: string) => url.endsWith("/api/pruning-rules");

const fillCriterion = (
  n: number,
  field: string,
  value: string,
  op?: string,
  unit?: string,
) => {
  fireEvent.change(screen.getByLabelText(`Criterion ${n} field`), {
    target: { value: field },
  });
  if (op) {
    fireEvent.change(screen.getByLabelText(`Criterion ${n} operator`), {
      target: { value: op },
    });
  }
  fireEvent.input(screen.getByLabelText(`Criterion ${n} value`), {
    target: { value },
  });
  if (unit) {
    fireEvent.change(screen.getByLabelText(`Criterion ${n} unit`), {
      target: { value: unit },
    });
  }
};

const addTagChip = (n: number, tag: string, op?: string, matchMode?: string) => {
  fireEvent.change(screen.getByLabelText(`Criterion ${n} field`), {
    target: { value: "tag" },
  });
  if (op) {
    fireEvent.change(screen.getByLabelText(`Criterion ${n} operator`), {
      target: { value: op },
    });
  }
  if (matchMode) {
    fireEvent.change(screen.getByLabelText(`Criterion ${n} match mode`), {
      target: { value: matchMode },
    });
  }
  fireEvent.input(screen.getByLabelText(`Criterion ${n} new tag`), {
    target: { value: tag },
  });
  fireEvent.click(
    screen.getByRole("button", { name: `Add tag to criterion ${n}` }),
  );
};

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
  it("creates a rule with a single age criterion", async () => {
    const calls = stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (method === "POST" && isRuleList(url))
        return jsonResponse(
          rule({ id: 5, name: "Stale movies", criteria: [ageRow("400")] }),
        );
      return undefined;
    });
    render(() => <PurgeRulesCard mode="movies" />);
    fireEvent.click(await screen.findByText("+ New rule"));

    fireEvent.input(screen.getByLabelText("Rule name"), {
      target: { value: "Stale movies" },
    });
    fillCriterion(1, "age", "400");
    fireEvent.click(screen.getByText("Create rule"));

    await waitFor(() =>
      expect(calls.some((c) => c.method === "POST" && isRuleList(c.url))).toBe(
        true,
      ),
    );
    const post = calls.find((c) => c.method === "POST" && isRuleList(c.url))!;
    expect(post.body).toEqual(
      upsertBody({
        name: "Stale movies",
        criteria: [ageRow("400")],
      }),
    );
  });

  it("creates a rule with a rating-less-than criterion", async () => {
    const calls = stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (method === "POST" && isRuleList(url))
        return jsonResponse(rule({ id: 11, name: "One-star junk" }));
      return undefined;
    });
    render(() => <PurgeRulesCard mode="movies" />);
    fireEvent.click(await screen.findByText("+ New rule"));

    fireEvent.input(screen.getByLabelText("Rule name"), {
      target: { value: "One-star junk" },
    });
    fillCriterion(1, "rating", "3", "lt");
    fireEvent.click(screen.getByText("Create rule"));

    await waitFor(() =>
      expect(calls.some((c) => c.method === "POST" && isRuleList(c.url))).toBe(
        true,
      ),
    );
    const post = calls.find((c) => c.method === "POST" && isRuleList(c.url))!;
    expect(post.body).toEqual(
      upsertBody({
        name: "One-star junk",
        criteria: [{ field: "rating", op: "lt", value: "3", unit: "stars" }],
      }),
    );
  });

  it("sends size as a free-fill value plus GB unit, not precomputed bytes", async () => {
    const calls = stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (method === "POST" && isRuleList(url))
        return jsonResponse(rule({ id: 6, name: "Big movies" }));
      return undefined;
    });
    render(() => <PurgeRulesCard mode="movies" />);
    fireEvent.click(await screen.findByText("+ New rule"));

    fireEvent.input(screen.getByLabelText("Rule name"), {
      target: { value: "Big movies" },
    });
    fillCriterion(1, "size", "2");
    fireEvent.click(screen.getByText("Create rule"));

    await waitFor(() =>
      expect(calls.some((c) => c.method === "POST" && isRuleList(c.url))).toBe(
        true,
      ),
    );
    const post = calls.find((c) => c.method === "POST" && isRuleList(c.url))!;
    expect(post.body).toEqual(
      upsertBody({
        name: "Big movies",
        criteria: [{ field: "size", op: "gt", value: "2", unit: "gb" }],
      }),
    );
  });

  it("ANDs two complete rows on the same rule", async () => {
    const calls = stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (method === "POST" && isRuleList(url))
        return jsonResponse(rule({ id: 12, name: "Old and tagged" }));
      return undefined;
    });
    render(() => <PurgeRulesCard mode="movies" />);
    fireEvent.click(await screen.findByText("+ New rule"));

    fireEvent.input(screen.getByLabelText("Rule name"), {
      target: { value: "Old and tagged" },
    });
    fillCriterion(1, "age", "30");
    addTagChip(2, "BDSM");
    fireEvent.click(screen.getByText("Create rule"));

    await waitFor(() =>
      expect(calls.some((c) => c.method === "POST" && isRuleList(c.url))).toBe(
        true,
      ),
    );
    const post = calls.find((c) => c.method === "POST" && isRuleList(c.url))!;
    expect(post.body).toEqual(
      upsertBody({
        name: "Old and tagged",
        criteria: [ageRow("30"), tagRow(["BDSM"])],
      }),
    );
  });
});

describe("PurgeRulesCard — dynamic rows", () => {
  it("starts with one empty row and appends another only after it is complete", async () => {
    stubFetch();
    render(() => <PurgeRulesCard mode="movies" />);
    fireEvent.click(await screen.findByText("+ New rule"));

    expect(screen.getByLabelText("Criterion 1 field")).toBeInTheDocument();
    expect(screen.queryByLabelText("Criterion 2 field")).toBeNull();

    fillCriterion(1, "age", "10");
    expect(screen.getByLabelText("Criterion 2 field")).toBeInTheDocument();
    expect(screen.queryByLabelText("Criterion 3 field")).toBeNull();

    fireEvent.change(screen.getByLabelText("Criterion 2 field"), {
      target: { value: "tag" },
    });
    expect(screen.queryByLabelText("Criterion 3 field")).toBeNull();
  });
});

describe("PurgeRulesCard — edit", () => {
  it("edits an existing rule and keeps its criteria", async () => {
    const calls = stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (method === "GET" && isRuleList(url))
        return jsonResponse([
          rule({ id: 7, name: "Old", criteria: [ageRow("200")] }),
        ]);
      if (method === "PUT" && url.endsWith("/api/pruning-rules/7"))
        return jsonResponse(
          rule({ id: 7, name: "Renamed", criteria: [ageRow("200")] }),
        );
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
    expect(put.body).toEqual(
      upsertBody({
        name: "Renamed",
        criteria: [ageRow("200")],
      }),
    );
  });
});

describe("PurgeRulesCard — delete", () => {
  it("deletes a rule after confirmation", async () => {
    const calls = stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (method === "GET" && isRuleList(url))
        return jsonResponse([
          rule({ id: 8, name: "Doomed", criteria: [ageRow("30")] }),
        ]);
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

describe("PurgeRulesCard — client-side validation (AC1)", () => {
  it("blocks submit when no criterion is complete", async () => {
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
    fillCriterion(1, "age", "10");
    fireEvent.click(screen.getByText("Create rule"));

    await screen.findByText("name is required");
    expect(calls.some((c) => c.method === "POST" && isRuleList(c.url))).toBe(
      false,
    );
  });
});

describe("PurgeRulesCard — enabled toggle", () => {
  it("toggling enabled fires an immediate update that carries criteria", async () => {
    const calls = stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (method === "GET" && isRuleList(url))
        return jsonResponse([
          rule({
            id: 9,
            name: "Toggle me",
            criteria: [ageRow("100")],
            enabled: true,
          }),
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
    expect(put.body).toEqual(
      upsertBody({
        name: "Toggle me",
        criteria: [ageRow("100")],
        enabled: false,
      }),
    );
  });
});

describe("PurgeRulesCard — row mutation error", () => {
  it("surfaces a row-mutation error without clearing the list", async () => {
    stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (method === "GET" && isRuleList(url))
        return jsonResponse([
          rule({ id: 10, name: "Fragile", criteria: [ageRow("50")] }),
        ]);
      if (method === "PUT" && url.endsWith("/api/pruning-rules/10"))
        return errorResponse(500, "database is locked");
      return undefined;
    });
    render(() => <PurgeRulesCard mode="movies" />);
    const toggle = await screen.findByLabelText("Fragile enabled");
    fireEvent.click(toggle);

    expect(await screen.findByText("database is locked")).toBeInTheDocument();
    expect(screen.getByText(/Fragile/)).toBeInTheDocument();
  });
});

describe("PurgeRulesCard — soft preview banner (§13.1)", () => {
  it("shows a debounced match-count banner once a criterion is complete, and never blocks a large-count save", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const calls = stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (method === "POST" && url.endsWith("/api/pruning-rules/preview"))
        return jsonResponse({ matchCount: 142 });
      if (method === "POST" && isRuleList(url))
        return jsonResponse(
          rule({ id: 11, name: "Stale", criteria: [ageRow("400")] }),
        );
      return undefined;
    });
    render(() => <PurgeRulesCard mode="movies" />);
    fireEvent.click(await screen.findByText("+ New rule"));
    fillCriterion(1, "age", "400");

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

describe("PurgeRulesCard — the card shell", () => {
  it("is a collapsed <details> with a Rules summary", async () => {
    stubFetch();
    const { container } = render(() => <PurgeRulesCard mode="movies" />);
    await screen.findByText("No rules for this mode yet.");

    const details = container.querySelector("details");
    expect(details).not.toBeNull();
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
          rule({
            id: 20,
            name: "Movies rule",
            mode: "movies",
            criteria: [ageRow("10")],
          }),
          rule({
            id: 21,
            name: "Series rule",
            mode: "series",
            criteria: [ageRow("10")],
          }),
          rule({
            id: 22,
            name: "Adult rule",
            mode: "adult",
            criteria: [ageRow("10")],
          }),
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

    expect(screen.queryByLabelText("Rule mode")).toBeNull();

    fireEvent.input(screen.getByLabelText("Rule name"), {
      target: { value: "S" },
    });
    fillCriterion(1, "age", "5");
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
  it("sends a tag-contains-any criterion with chips, firing NO request until Save", async () => {
    const calls = stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (method === "POST" && isRuleList(url))
        return jsonResponse(rule({ id: 30, name: "Tagged" }));
      return undefined;
    });
    render(() => <PurgeRulesCard mode="movies" />);
    fireEvent.click(await screen.findByText("+ New rule"));
    addTagChip(1, "BDSM");

    expect(
      calls.some((c) => c.method === "POST" && isRuleList(c.url)),
    ).toBe(false);

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
    expect(post.body).toEqual(
      upsertBody({
        name: "Tagged",
        criteria: [tagRow(["BDSM"])],
      }),
    );
  });

  it("defaults a new tag row to contains + any", async () => {
    stubFetch();
    render(() => <PurgeRulesCard mode="movies" />);
    fireEvent.click(await screen.findByText("+ New rule"));
    fireEvent.change(screen.getByLabelText("Criterion 1 field"), {
      target: { value: "tag" },
    });
    expect(
      (screen.getByLabelText("Criterion 1 operator") as HTMLSelectElement)
        .value,
    ).toBe("contains");
    expect(
      (screen.getByLabelText("Criterion 1 match mode") as HTMLSelectElement)
        .value,
    ).toBe("any");
  });

  it("ANDs two tag rows (contains any and does-not-contain any)", async () => {
    const calls = stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (method === "POST" && isRuleList(url))
        return jsonResponse(rule({ id: 31, name: "BDSM not Pee" }));
      return undefined;
    });
    render(() => <PurgeRulesCard mode="movies" />);
    fireEvent.click(await screen.findByText("+ New rule"));
    fireEvent.input(screen.getByLabelText("Rule name"), {
      target: { value: "BDSM not Pee" },
    });
    addTagChip(1, "BDSM");
    addTagChip(2, "Pee", "notContains");
    fireEvent.click(screen.getByText("Create rule"));

    await waitFor(() =>
      expect(calls.some((c) => c.method === "POST" && isRuleList(c.url))).toBe(
        true,
      ),
    );
    const post = calls.find((c) => c.method === "POST" && isRuleList(c.url))!;
    expect(post.body).toEqual(
      upsertBody({
        name: "BDSM not Pee",
        criteria: [
          tagRow(["BDSM"]),
          tagRow(["Pee"], { op: "notContains" }),
        ],
      }),
    );
  });

  it("sends matchMode all and multiple chips on one row", async () => {
    const calls = stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (method === "POST" && isRuleList(url))
        return jsonResponse(rule({ id: 33, name: "All bondage" }));
      return undefined;
    });
    render(() => <PurgeRulesCard mode="adult" />);
    fireEvent.click(await screen.findByText("+ New rule"));
    fireEvent.input(screen.getByLabelText("Rule name"), {
      target: { value: "All bondage" },
    });
    addTagChip(1, "Bondage", undefined, "all");
    fireEvent.input(screen.getByLabelText("Criterion 1 new tag"), {
      target: { value: "Bound" },
    });
    fireEvent.click(
      screen.getByRole("button", { name: "Add tag to criterion 1" }),
    );
    fireEvent.click(screen.getByText("Create rule"));

    await waitFor(() =>
      expect(calls.some((c) => c.method === "POST" && isRuleList(c.url))).toBe(
        true,
      ),
    );
    const post = calls.find((c) => c.method === "POST" && isRuleList(c.url))!;
    expect(post.body).toEqual(
      upsertBody({
        name: "All bondage",
        mode: "adult",
        criteria: [tagRow(["Bondage", "Bound"], { matchMode: "all" })],
      }),
    );
  });

  it("offers no bulk affordance — one × per chip, one Add, no clear-all", async () => {
    stubFetch();
    render(() => <PurgeRulesCard mode="movies" />);
    fireEvent.click(await screen.findByText("+ New rule"));
    addTagChip(1, "Bondage");
    fireEvent.input(screen.getByLabelText("Criterion 1 new tag"), {
      target: { value: "Bound" },
    });
    fireEvent.click(
      screen.getByRole("button", { name: "Add tag to criterion 1" }),
    );

    expect(screen.getByLabelText("Remove Bondage")).toBeInTheDocument();
    expect(screen.getByLabelText("Remove Bound")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Add tag to criterion 1" }),
    ).toBeInTheDocument();
    expect(screen.queryByText(/clear all/i)).toBeNull();
    expect(screen.queryByText(/remove all/i)).toBeNull();
  });

  it("stacks tag chips below the dropdowns and omits the unit select", async () => {
    stubFetch();
    render(() => <PurgeRulesCard mode="adult" />);
    fireEvent.click(await screen.findByText("+ New rule"));
    addTagChip(1, "Bondage");
    fireEvent.input(screen.getByLabelText("Criterion 1 new tag"), {
      target: { value: "Bound" },
    });
    fireEvent.click(
      screen.getByRole("button", { name: "Add tag to criterion 1" }),
    );

    expect(screen.queryByLabelText("Criterion 1 unit")).toBeNull();
    const chips = screen.getByTestId("criterion-1-chips");
    expect(chips).toContainElement(screen.getByLabelText("Remove Bondage"));
    expect(chips).toContainElement(screen.getByLabelText("Remove Bound"));
    expect(chips).not.toContainElement(
      screen.getByLabelText("Criterion 1 field"),
    );
    expect(chips).not.toContainElement(
      screen.getByRole("button", { name: "Add tag to criterion 1" }),
    );
    expect(screen.getByText("Bondage").closest("span.inline-flex")).toHaveClass(
      "bg-surface/60",
    );
  });

  it("de-dupes chips case-insensitively with no error", async () => {
    stubFetch();
    render(() => <PurgeRulesCard mode="movies" />);
    fireEvent.click(await screen.findByText("+ New rule"));
    addTagChip(1, "Bondage");
    fireEvent.input(screen.getByLabelText("Criterion 1 new tag"), {
      target: { value: "bondage" },
    });
    fireEvent.click(
      screen.getByRole("button", { name: "Add tag to criterion 1" }),
    );
    expect(screen.getAllByLabelText("Remove Bondage")).toHaveLength(1);
  });
});

describe("PurgeRulesCard — client-side validation, tags-only", () => {
  it("accepts a rule whose ONLY criterion is a tag", async () => {
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
    addTagChip(1, "Trailer");
    fireEvent.click(screen.getByText("Create rule"));

    await waitFor(() =>
      expect(calls.some((c) => c.method === "POST" && isRuleList(c.url))).toBe(
        true,
      ),
    );
    expect(screen.queryByText("select at least one condition")).toBeNull();
  });

  it("blocks submit when a tag row has no chips", async () => {
    const calls = stubFetch();
    render(() => <PurgeRulesCard mode="movies" />);
    fireEvent.click(await screen.findByText("+ New rule"));
    fireEvent.input(screen.getByLabelText("Rule name"), {
      target: { value: "Empty chips" },
    });
    fireEvent.change(screen.getByLabelText("Criterion 1 field"), {
      target: { value: "tag" },
    });
    fireEvent.click(screen.getByText("Create rule"));

    await screen.findByText("select at least one condition");
    expect(calls.some((c) => c.method === "POST" && isRuleList(c.url))).toBe(
      false,
    );
  });
});

describe("PurgeRulesCard — the enabled toggle preserves criteria", () => {
  it("carries criteria through an enable/disable toggle", async () => {
    const calls = stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (method === "GET" && isRuleList(url))
        return jsonResponse([
          rule({
            id: 32,
            name: "Tagged toggle",
            criteria: [
              tagRow(["BDSM"]),
              tagRow(["Rope"]),
            ],
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
    expect(put.body).toEqual(
      upsertBody({
        name: "Tagged toggle",
        criteria: [
          tagRow(["BDSM"]),
          tagRow(["Rope"]),
        ],
        enabled: false,
      }),
    );
  });
});
