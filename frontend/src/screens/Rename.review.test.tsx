// US — Adult Review action: web-identified Adult unmatched rows get a per-row
// Review modal (not batchable) that calls GET …/review for a preview and
// POST …/review-confirm to commit.
//
// Coverage (§7.12 of autopilot-impl-adult-rename-review-alts.md):
//   - Review option appears for Adult unmatched with title + no giveBackSceneId
//   - Review absent for: Movies row, Adult pending, Adult unmatched no-title,
//     Adult unmatched with giveBackSceneId
//   - Choosing Review + clicking Apply opens the modal
//   - Modal shows current basename and pre-filled editable proposed name
//   - Editing proposed name and confirming posts the edited fileName
//   - Catalog-match banner renders and disables input when preview has a catalog
//   - Cancel issues no mutating request
//   - planActionForRow returns null for a "review" selection (Apply-All skips)
//
// Harness conventions match Rename.delete.test.tsx: stubFetch + parsed-body
// Call tracking, pageOf auto-wrap, recently-applied and organize/events routed
// to empty defaults so each test only declares what it needs.
//
// Claude 2026-08-12: new file.
// Context: autopilot-impl-adult-rename-review-alts.md §6 F7, §7.12.

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@solidjs/testing-library";
import type { AdultReviewPreview, Proposal } from "@dto";
import { Rename, isAdultWebIdentified, planActionForRow } from "./Rename";
import { jsonResponse, noContent } from "../testing/http";

// ---- Helpers ----------------------------------------------------------------


const pageOf = (items: Proposal[]) => ({
  items,
  total: items.length,
  limit: 50,
  offset: 0,
});

// Minimal Adult unmatched row with a web-identified title. Override any field.
const adultProposal = (over: Partial<Proposal> = {}): Proposal => ({
  id: 1,
  status: "unmatched",
  sourceName: "Studio.Title.2024.mkv",
  sourcePath: "/adult/Studio.Title.2024.mkv",
  rootFolderPath: "/adult",
  title: "Title",
  studio: "Studio",
  date: "2024-01-01",
  phash: "abcd1234",
  reason: "web-identified only — no catalog scene id yet; use Review to name and track it",
  draftId: "",
  ...over,
});

const defaultPreview: AdultReviewPreview = {
  proposedName: "Studio - Title (2024-01-01) [phash-abcd1234].mkv",
  studio: "Studio",
  title: "Title",
  date: "2024-01-01",
  phash: "abcd1234",
  catalogBox: "",
  catalogSceneId: "",
  catalogTitle: "",
  catalogStudio: "",
  catalogDate: "",
  recheckError: "",
};

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
    if (url.includes("/naming-preset")) return jsonResponse({ preset: "jellyfin" });
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

const reviewCalls = (calls: Call[]) =>
  calls.filter((c) => c.url.includes("/review-confirm") && c.method === "POST");

afterEach(() => {
  vi.unstubAllGlobals();
  localStorage.clear();
});

// ---- Unit tests for exported predicates -------------------------------------

describe("isAdultWebIdentified", () => {
  it("returns true for adult unmatched with title and no giveBackSceneId", () => {
    expect(isAdultWebIdentified(adultProposal(), "adult")).toBe(true);
  });

  it("returns false for movies mode", () => {
    expect(isAdultWebIdentified(adultProposal(), "movies")).toBe(false);
  });

  it("returns false for series mode", () => {
    expect(isAdultWebIdentified(adultProposal(), "series")).toBe(false);
  });

  it("returns false for adult pending status", () => {
    expect(
      isAdultWebIdentified(adultProposal({ status: "pending" }), "adult"),
    ).toBe(false);
  });

  it("returns false for adult unmatched with no title and no web-identified reason", () => {
    expect(
      isAdultWebIdentified(
        adultProposal({ title: "", reason: "no confident identification" }),
        "adult",
      ),
    ).toBe(false);
  });

  it("returns true when reason contains web-identified even if title is empty", () => {
    expect(
      isAdultWebIdentified(
        adultProposal({
          title: "",
          reason:
            "web-identified only — no catalog scene id yet; use Review to name and track it",
        }),
        "adult",
      ),
    ).toBe(true);
  });

  it("returns false for adult unmatched with a giveBackSceneId (already has catalog id)", () => {
    expect(
      isAdultWebIdentified(
        adultProposal({ giveBackSceneId: "tpdb-scene-123" }),
        "adult",
      ),
    ).toBe(false);
  });
});

describe("planActionForRow — Review is excluded from Apply-All", () => {
  it("returns null for a review selection, never apply/dismiss/delete", () => {
    const p = adultProposal();
    expect(planActionForRow(p, "review", false)).toBeNull();
  });
});

// ---- Integration: Review option visibility ----------------------------------

describe("Rename — Review option eligibility", () => {
  it("Review option is enabled for an Adult unmatched row with title and no giveBackSceneId", async () => {
    stubFetch((url) => {
      if (url.includes("/rename/proposals")) return jsonResponse([adultProposal()]);
      return jsonResponse([]);
    });

    render(() => <Rename />);
    fireEvent.click(await screen.findByText("Adult"));
    await screen.findByText("Studio.Title.2024.mkv");

    const row = screen.getByText("Studio.Title.2024.mkv").closest("tr, [data-proposal-row]")! as HTMLElement;
    const select = within(row).getByRole("combobox");
    const reviewOption = within(select).getByRole("option", { name: "Review" });
    expect(reviewOption).not.toBeDisabled();
  });

  it("Review option is disabled for an Adult pending row", async () => {
    stubFetch((url) => {
      if (url.includes("/rename/proposals"))
        return jsonResponse([adultProposal({ status: "pending" })]);
      return jsonResponse([]);
    });

    render(() => <Rename />);
    fireEvent.click(await screen.findByText("Adult"));
    await screen.findByText("Studio.Title.2024.mkv");

    const row = screen.getByText("Studio.Title.2024.mkv").closest("tr, [data-proposal-row]")! as HTMLElement;
    const select = within(row).getByRole("combobox");
    const reviewOption = within(select).getByRole("option", { name: "Review" });
    expect(reviewOption).toBeDisabled();
  });

  it("Review option is enabled and pre-selected for a web-identified Adult unmatched row", async () => {
    stubFetch((url) => {
      if (url.includes("/rename/proposals")) return jsonResponse([adultProposal()]);
      return jsonResponse([]);
    });

    render(() => <Rename />);
    fireEvent.click(await screen.findByText("Adult"));
    await screen.findByText("Studio.Title.2024.mkv");

    const row = screen.getByText("Studio.Title.2024.mkv").closest("tr, [data-proposal-row]")! as HTMLElement;
    const select = within(row).getByRole("combobox") as HTMLSelectElement;
    expect(select.value).toBe("review");
    const reviewOption = within(select).getByRole("option", { name: "Review" });
    expect(reviewOption).not.toBeDisabled();
  });

  it("already-in-library pending rows default to Rename, not Review", async () => {
    stubFetch((url) => {
      if (url.includes("/rename/proposals"))
        return jsonResponse([
          adultProposal({
            status: "pending",
            giveBackSceneId: "scene-abc",
            reason:
              "alternate: already in library as \"Tracked Title\" — apply will fold as primary or alternate by quality",
          }),
        ]);
      return jsonResponse([]);
    });

    render(() => <Rename />);
    fireEvent.click(await screen.findByText("Adult"));
    await screen.findByText("Studio.Title.2024.mkv");

    const row = screen.getByText("Studio.Title.2024.mkv").closest("tr, [data-proposal-row]")! as HTMLElement;
    const select = within(row).getByRole("combobox") as HTMLSelectElement;
    expect(select.value).toBe("rename");
    const reviewOption = within(select).getByRole("option", { name: "Review" });
    expect(reviewOption).toBeDisabled();
  });

  it("Review option is disabled for an Adult unmatched row with no title and no web-identified reason", async () => {
    stubFetch((url) => {
      if (url.includes("/rename/proposals"))
        return jsonResponse([
          adultProposal({ title: "", reason: "no confident identification" }),
        ]);
      return jsonResponse([]);
    });

    render(() => <Rename />);
    fireEvent.click(await screen.findByText("Adult"));
    await screen.findByText("Studio.Title.2024.mkv");

    const row = screen.getByText("Studio.Title.2024.mkv").closest("tr, [data-proposal-row]")! as HTMLElement;
    const select = within(row).getByRole("combobox");
    const reviewOption = within(select).getByRole("option", { name: "Review" });
    expect(reviewOption).toBeDisabled();
  });

  it("Review option is disabled for an Adult unmatched row with a giveBackSceneId", async () => {
    stubFetch((url) => {
      if (url.includes("/rename/proposals"))
        return jsonResponse([adultProposal({ giveBackSceneId: "tpdb-scene-999" })]);
      return jsonResponse([]);
    });

    render(() => <Rename />);
    fireEvent.click(await screen.findByText("Adult"));
    await screen.findByText("Studio.Title.2024.mkv");

    const row = screen.getByText("Studio.Title.2024.mkv").closest("tr, [data-proposal-row]")! as HTMLElement;
    const select = within(row).getByRole("combobox");
    const reviewOption = within(select).getByRole("option", { name: "Review" });
    expect(reviewOption).toBeDisabled();
  });

  it("Review option is disabled for a Movies row", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          {
            id: 1,
            status: "unmatched",
            sourceName: "Movie.2024.mkv",
            rootFolderPath: "/movies",
            title: "Movie",
            year: 2024,
            reason: "no match",
            draftId: "",
          } satisfies Proposal,
        ]);
      return jsonResponse([]);
    });

    render(() => <Rename />);
    await screen.findByText("Movie.2024.mkv");

    const row = screen.getByText("Movie.2024.mkv").closest("tr, [data-proposal-row]")! as HTMLElement;
    const select = within(row).getByRole("combobox");
    // Review is Adult-only — the option is omitted on Movies/Series rows.
    expect(
      within(select).queryByRole("option", { name: "Review" }),
    ).toBeNull();
  });
});

// ---- Integration: opening ReviewDialog --------------------------------------

describe("Rename — Review modal", () => {
  it("opens the dialog when Review is selected and Apply is clicked", async () => {
    const calls: Call[] = [];
    stubFetch((url) => {
      // More specific checks first: review-confirm and /review must precede the
      // broader /rename/proposals match, because
      // /api/modes/adult/rename/proposals/1/review also contains /rename/proposals.
      if (url.includes("/review-confirm")) return jsonResponse({});
      if (url.includes("/review")) return jsonResponse(defaultPreview);
      if (url.includes("/rename/proposals")) return jsonResponse([adultProposal()]);
      return jsonResponse([]);
    });

    render(() => <Rename />);
    fireEvent.click(await screen.findByText("Adult"));
    await screen.findByText("Studio.Title.2024.mkv");

    const row = screen.getByText("Studio.Title.2024.mkv").closest("tr, [data-proposal-row]")! as HTMLElement;
    fireEvent.change(within(row as HTMLElement).getByRole("combobox"), {
      target: { value: "review" },
    });
    fireEvent.click(
      within(row as HTMLElement).getByRole("button", {
        name: /Apply selected action/,
      }),
    );

    await screen.findByRole("dialog", { name: /Review/ });
    expect(calls.length).toBeGreaterThanOrEqual(0); // suppress unused var warning
  });

  it("shows the current basename and a pre-filled proposed name", async () => {
    stubFetch((url) => {
      if (url.includes("/review-confirm")) return jsonResponse({});
      if (url.includes("/review")) return jsonResponse(defaultPreview);
      if (url.includes("/rename/proposals")) return jsonResponse([adultProposal()]);
      return jsonResponse([]);
    });

    render(() => <Rename />);
    fireEvent.click(await screen.findByText("Adult"));
    await screen.findByText("Studio.Title.2024.mkv");

    const row = screen.getByText("Studio.Title.2024.mkv").closest("tr, [data-proposal-row]")! as HTMLElement;
    fireEvent.change(within(row as HTMLElement).getByRole("combobox"), {
      target: { value: "review" },
    });
    fireEvent.click(
      within(row as HTMLElement).getByRole("button", {
        name: /Apply selected action/,
      }),
    );

    const dialog = await screen.findByRole("dialog", { name: /Review/ });

    // Wait for loading state to resolve — "Current name" heading only renders
    // once the preview resource has resolved (inside Show when={preview()}).
    // Scope to `within(dialog)` to avoid ambiguity with the underlying table.
    await within(dialog).findByText("Current name");

    // The proposed-name input must be present and enabled (local branch).
    const input = within(dialog).getByRole("textbox", { name: /proposed name/i });
    expect(input).toBeInTheDocument();
    expect(input).not.toBeDisabled();
  });

  it("editing the proposed name and confirming posts the edited fileName (local branch)", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/review-confirm")) return noContent();
      if (url.includes("/review")) return jsonResponse(defaultPreview);
      if (url.includes("/rename/proposals")) return jsonResponse([adultProposal()]);
      return jsonResponse([]);
    });

    render(() => <Rename />);
    fireEvent.click(await screen.findByText("Adult"));
    await screen.findByText("Studio.Title.2024.mkv");

    const row = screen.getByText("Studio.Title.2024.mkv").closest("tr, [data-proposal-row]")! as HTMLElement;
    fireEvent.change(within(row as HTMLElement).getByRole("combobox"), {
      target: { value: "review" },
    });
    fireEvent.click(
      within(row as HTMLElement).getByRole("button", {
        name: /Apply selected action/,
      }),
    );

    const dialog = await screen.findByRole("dialog", { name: /Review/ });
    // Wait for the preview to load (input is inside Show when={preview()})
    await within(dialog).findByText("Current name");
    const input = within(dialog).getByRole("textbox", { name: /proposed name/i });
    // Set a custom name (simulating an operator edit) and wait for Solid's
    // reactive system to process the update before clicking Confirm.
    fireEvent.input(input, { target: { value: "My Custom Name.mkv" } });
    // Wait for the Confirm button to be enabled (canConfirm uses fileName signal)
    const confirmBtn = within(dialog).getByRole("button", { name: /confirm/i });
    await waitFor(() => expect(confirmBtn).not.toBeDisabled());
    fireEvent.click(confirmBtn);

    await waitFor(() => {
      const rc = reviewCalls(calls);
      expect(rc).toHaveLength(1);
      expect(rc[0]!.body).toEqual({ fileName: "My Custom Name.mkv" });
    });
  });

  it("clearing the proposed name does not snap back to the preview default", async () => {
    stubFetch((url) => {
      if (url.includes("/review")) return jsonResponse(defaultPreview);
      if (url.includes("/rename/proposals")) return jsonResponse([adultProposal()]);
      return jsonResponse([]);
    });

    render(() => <Rename />);
    fireEvent.click(await screen.findByText("Adult"));
    await screen.findByText("Studio.Title.2024.mkv");

    const row = screen.getByText("Studio.Title.2024.mkv").closest("tr, [data-proposal-row]")! as HTMLElement;
    fireEvent.change(within(row as HTMLElement).getByRole("combobox"), {
      target: { value: "review" },
    });
    fireEvent.click(
      within(row as HTMLElement).getByRole("button", {
        name: /Apply selected action/,
      }),
    );

    const dialog = await screen.findByRole("dialog", { name: /Review/ });
    await within(dialog).findByText("Current name");
    const input = within(dialog).getByRole("textbox", {
      name: /proposed name/i,
    }) as HTMLInputElement;
    await waitFor(() =>
      expect(input.value).toBe(defaultPreview.proposedName),
    );

    fireEvent.input(input, { target: { value: "" } });
    await waitFor(() => expect(input.value).toBe(""));
    expect(
      within(dialog).getByRole("button", { name: /confirm/i }),
    ).toBeDisabled();
  });

  it("Cancel issues no mutating request", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/review-confirm")) return jsonResponse({});
      if (url.includes("/review")) return jsonResponse(defaultPreview);
      if (url.includes("/rename/proposals")) return jsonResponse([adultProposal()]);
      return jsonResponse([]);
    });

    render(() => <Rename />);
    fireEvent.click(await screen.findByText("Adult"));
    await screen.findByText("Studio.Title.2024.mkv");

    const row = screen.getByText("Studio.Title.2024.mkv").closest("tr, [data-proposal-row]")! as HTMLElement;
    fireEvent.change(within(row as HTMLElement).getByRole("combobox"), {
      target: { value: "review" },
    });
    fireEvent.click(
      within(row as HTMLElement).getByRole("button", {
        name: /Apply selected action/,
      }),
    );

    const dialog = await screen.findByRole("dialog", { name: /Review/ });
    fireEvent.click(within(dialog).getByRole("button", { name: /cancel/i }));

    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: /Review/ })).toBeNull(),
    );
    expect(reviewCalls(calls)).toHaveLength(0);
  });

  it("catalog branch: renders banner, disables input, posts box/sceneId on confirm", async () => {
    const catalogPreview: AdultReviewPreview = {
      ...defaultPreview,
      catalogBox: "tpdb",
      catalogSceneId: "scene-777",
      catalogTitle: "Catalog Title",
      catalogStudio: "Catalog Studio",
      catalogDate: "2024-06-01",
    };

    const calls = stubFetch((url) => {
      if (url.includes("/review-confirm")) return noContent();
      if (url.includes("/review")) return jsonResponse(catalogPreview);
      if (url.includes("/rename/proposals")) return jsonResponse([adultProposal()]);
      return jsonResponse([]);
    });

    render(() => <Rename />);
    fireEvent.click(await screen.findByText("Adult"));
    await screen.findByText("Studio.Title.2024.mkv");

    const row = screen.getByText("Studio.Title.2024.mkv").closest("tr, [data-proposal-row]")! as HTMLElement;
    fireEvent.change(within(row as HTMLElement).getByRole("combobox"), {
      target: { value: "review" },
    });
    fireEvent.click(
      within(row as HTMLElement).getByRole("button", {
        name: /Apply selected action/,
      }),
    );

    const dialog = await screen.findByRole("dialog", { name: /Review/ });

    // Catalog banner must appear — wait for preview to load
    await within(dialog).findByText(/Catalog match found/);

    // Input is disabled on catalog branch (preview loaded, catalog match present)
    const input = within(dialog).getByRole("textbox", { name: /proposed name/i });
    expect(input).toBeDisabled();

    // Confirm button should be enabled on catalog branch (phash is non-empty,
    // isCatalogMatch is true). Wait for Solid's reactive update.
    const confirmBtn = within(dialog).getByRole("button", { name: /confirm/i });
    await waitFor(() => expect(confirmBtn).not.toBeDisabled());
    fireEvent.click(confirmBtn);

    await waitFor(() => {
      const rc = reviewCalls(calls);
      expect(rc).toHaveLength(1);
      expect(rc[0]!.body).toMatchObject({
        box: "tpdb",
        sceneId: "scene-777",
        title: "Catalog Title",
        studio: "Catalog Studio",
        date: "2024-06-01",
      });
    });
  });
});

// ---- Apply-All exclusion ----------------------------------------------------

describe("Rename — Review not in Apply-All", () => {
  it("planActionForRow returns null for review, so Apply-All skips those rows", () => {
    // Direct unit test — no render needed.
    const p = adultProposal();
    // "review" selected → null
    expect(planActionForRow(p, "review", false, "adult")).toBeNull();
    // Default action is Review for web-identified rows — still excluded from Apply-All.
    expect(planActionForRow(p, "", false, "adult")).toBeNull();
  });

  it("Apply all does not include web-identified Adult rows in the plan", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/rename/proposals")) return jsonResponse([adultProposal()]);
      return jsonResponse([]);
    });

    render(() => <Rename />);
    fireEvent.click(await screen.findByText("Adult"));
    await screen.findByText("Studio.Title.2024.mkv");

    // Select Review on the only row
    const row = screen.getByText("Studio.Title.2024.mkv").closest("tr, [data-proposal-row]")! as HTMLElement;
    fireEvent.change(within(row as HTMLElement).getByRole("combobox"), {
      target: { value: "review" },
    });

    // "Apply all" should report "Nothing to apply" since review is excluded
    fireEvent.click(screen.getByRole("button", { name: "Apply all" }));

    await screen.findByText(/Nothing to apply/);
    // No confirm dialog — nothing was planned
    expect(screen.queryByRole("dialog", { name: "Confirm apply all" })).toBeNull();
    // No review-confirm call either
    expect(reviewCalls(calls)).toHaveLength(0);
  });
});
