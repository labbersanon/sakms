// US — Adult Review action: web-identified Adult unmatched rows get a per-row
// Review modal (not batchable) that calls GET …/review for a preview and
// POST …/review-confirm to commit.
//
// Coverage (§7.12 of autopilot-impl-adult-rename-review-alts.md):
//   - Review option appears for Adult unmatched with title + no giveBackSceneId
//   - Review appears for unmatched Movies/Series; disabled for pending Movies
//   - Review absent for: Adult pending, Adult unmatched no-title,
//     Adult unmatched with giveBackSceneId
//   - Choosing Review + clicking Apply opens the modal
//   - Modal shows current basename and a Studio/Title/Date form that composes
//     Studio - Title (Date) [phash-HASH].ext (phash and extension are locked)
//   - Editing the form and confirming posts the composed fileName
//   - Catalog-match banner renders and disables the form when preview has a catalog
//   - Cancel issues no mutating request
//   - planActionForRow returns null for a "review" selection (Apply-All skips)
//
// Harness conventions match Rename.delete.test.tsx: stubFetch + parsed-body
// Call tracking, asPage auto-wrap, recently-applied and organize/events routed
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
import { Rename, canReviewName, isAdultWebIdentified, planActionForRow } from "./Rename";
import { jsonResponse, noContent, asPage } from "../testing/http";

// ---- Helpers ----------------------------------------------------------------

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

const movieUnmatched = (): Proposal => ({
  id: 1,
  status: "unmatched",
  sourceName: "Movie.2024.mkv",
  sourcePath: "/movies/Movie.2024.mkv",
  rootFolderPath: "/movies",
  title: "Movie",
  year: 2024,
  reason: "no match",
  draftId: "",
});

const seriesUnmatched = (): Proposal => ({
  id: 1,
  status: "unmatched",
  sourceName: "Show.S01E02.mkv",
  sourcePath: "/series/Show.S01E02.mkv",
  rootFolderPath: "/series",
  title: "Show",
  year: 2020,
  seasonNumber: 1,
  episodeNumber: 2,
  episodeTitle: "Pilot",
  reason: "no match",
  draftId: "",
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
        return jsonResponse(asPage(body as Proposal[]));
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

  it("Review option is enabled for an unmatched Movies row", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([movieUnmatched()]);
      return jsonResponse([]);
    });

    render(() => <Rename />);
    await screen.findByText("Movie.2024.mkv");

    const row = screen.getByText("Movie.2024.mkv").closest("tr, [data-proposal-row]")! as HTMLElement;
    const select = within(row).getByRole("combobox");
    const reviewOption = within(select).getByRole("option", { name: "Review" });
    expect(reviewOption).not.toBeDisabled();
  });
});

describe("canReviewName", () => {
  it("is true for unmatched movies and series", () => {
    expect(
      canReviewName(
        { id: 1, status: "unmatched", sourceName: "a.mkv", rootFolderPath: "/m" },
        "movies",
      ),
    ).toBe(true);
    expect(
      canReviewName(
        { id: 1, status: "unmatched", sourceName: "a.mkv", rootFolderPath: "/s" },
        "series",
      ),
    ).toBe(true);
  });

  it("is false for pending movies", () => {
    expect(
      canReviewName(
        { id: 1, status: "pending", sourceName: "a.mkv", rootFolderPath: "/m", title: "A" },
        "movies",
      ),
    ).toBe(false);
  });
});

// ---- Integration: opening ReviewDialog --------------------------------------

describe("Rename — Review modal", () => {
  it("opens the dialog when Review is selected and Apply is clicked", async () => {
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
  });

  it("shows the current basename and a pre-filled Studio/Title/Date form", async () => {
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
    await within(dialog).findByText("Current name");

    const titleInput = within(dialog).getByRole("textbox", { name: /^title$/i });
    const studioInput = within(dialog).getByRole("textbox", { name: /^studio$/i });
    const dateInput = within(dialog).getByRole("textbox", { name: /^date$/i });
    expect(titleInput).not.toBeDisabled();
    expect(studioInput).not.toBeDisabled();
    expect(dateInput).not.toBeDisabled();
    await waitFor(() => {
      expect(titleInput).toHaveValue(defaultPreview.title);
      expect(studioInput).toHaveValue(defaultPreview.studio);
      expect(dateInput).toHaveValue(defaultPreview.date);
    });
    expect(within(dialog).getByRole("status", { name: /proposed name/i })).toHaveTextContent(
      defaultPreview.proposedName,
    );
  });

  it("editing Studio/Title/Date and confirming posts the composed fileName (local branch)", async () => {
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
    await within(dialog).findByText("Current name");
    const titleInput = within(dialog).getByRole("textbox", { name: /^title$/i });
    const studioInput = within(dialog).getByRole("textbox", { name: /^studio$/i });
    const dateInput = within(dialog).getByRole("textbox", { name: /^date$/i });
    await waitFor(() => expect(titleInput).toHaveValue(defaultPreview.title));
    fireEvent.input(studioInput, { target: { value: "Other Studio" } });
    fireEvent.input(titleInput, { target: { value: "Other Title" } });
    fireEvent.input(dateInput, { target: { value: "2025-06-15" } });
    await waitFor(() =>
      expect(
        within(dialog).getByRole("status", { name: /proposed name/i }),
      ).toHaveTextContent(
        "Other Studio - Other Title (2025-06-15) [phash-abcd1234].mkv",
      ),
    );
    const confirmBtn = within(dialog).getByRole("button", { name: /confirm/i });
    await waitFor(() => expect(confirmBtn).not.toBeDisabled());
    fireEvent.click(confirmBtn);

    await waitFor(() => {
      const rc = reviewCalls(calls);
      expect(rc).toHaveLength(1);
      expect(rc[0]!.body).toEqual({
        fileName: "Other Studio - Other Title (2025-06-15) [phash-abcd1234].mkv",
      });
    });
  });

  it("clearing Title does not snap back to the preview default", async () => {
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
    const titleInput = within(dialog).getByRole("textbox", {
      name: /^title$/i,
    }) as HTMLInputElement;
    await waitFor(() => expect(titleInput.value).toBe(defaultPreview.title));

    fireEvent.input(titleInput, { target: { value: "" } });
    await waitFor(() => expect(titleInput.value).toBe(""));
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
    await within(dialog).findByText(/Catalog match found/);

    expect(within(dialog).getByRole("textbox", { name: /^title$/i })).toBeDisabled();
    expect(within(dialog).getByRole("textbox", { name: /^studio$/i })).toBeDisabled();
    expect(within(dialog).getByRole("textbox", { name: /^date$/i })).toBeDisabled();

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
    const p = adultProposal();
    expect(planActionForRow(p, "review", false, "adult")).toBeNull();
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

    const row = screen.getByText("Studio.Title.2024.mkv").closest("tr, [data-proposal-row]")! as HTMLElement;
    fireEvent.change(within(row as HTMLElement).getByRole("combobox"), {
      target: { value: "review" },
    });

    fireEvent.click(screen.getByRole("button", { name: "Apply all" }));

    await screen.findByText(/Nothing to apply/);
    expect(screen.queryByRole("dialog", { name: "Confirm apply all" })).toBeNull();
    expect(reviewCalls(calls)).toHaveLength(0);
  });
});

describe("Rename — Movies Review form", () => {
  it("composes Title (Year) [tmdbid] and posts repick then apply", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/repick")) return jsonResponse({ ...movieUnmatched(), status: "pending" });
      if (url.includes("/apply")) return jsonResponse({ ...movieUnmatched(), status: "applied" });
      if (url.includes("/rename/proposals")) return jsonResponse([movieUnmatched()]);
      return jsonResponse([]);
    });

    render(() => <Rename />);
    await screen.findByText("Movie.2024.mkv");
    const row = screen.getByText("Movie.2024.mkv").closest("tr, [data-proposal-row]")! as HTMLElement;
    fireEvent.change(within(row).getByRole("combobox"), { target: { value: "review" } });
    fireEvent.click(within(row).getByRole("button", { name: /Apply selected action/ }));

    const dialog = await screen.findByRole("dialog", { name: /Review/ });
    await within(dialog).findByText("Current name");
    fireEvent.input(within(dialog).getByRole("textbox", { name: /^title$/i }), {
      target: { value: "Other Movie" },
    });
    fireEvent.input(within(dialog).getByRole("textbox", { name: /^year$/i }), {
      target: { value: "2025" },
    });
    fireEvent.input(within(dialog).getByRole("textbox", { name: /^tmdb id$/i }), {
      target: { value: "99" },
    });
    await waitFor(() =>
      expect(within(dialog).getByRole("status", { name: /proposed name/i })).toHaveTextContent(
        "Other Movie (2025) [tmdbid-99].mkv",
      ),
    );
    const confirmBtn = within(dialog).getByRole("button", { name: /confirm/i });
    await waitFor(() => expect(confirmBtn).not.toBeDisabled());
    fireEvent.click(confirmBtn);

    await waitFor(() => {
      const repick = calls.filter((c) => c.url.includes("/repick") && c.method === "POST");
      const apply = calls.filter((c) => c.url.endsWith("/apply") && c.method === "POST");
      expect(repick).toHaveLength(1);
      expect(repick[0]!.body).toEqual({
        title: "Other Movie",
        tmdbId: 99,
        year: 2025,
      });
      expect(apply).toHaveLength(1);
    });
  });
});

describe("Rename — Series Review form", () => {
  it("composes Series SxxExx Episode Title and posts repick then apply", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/repick")) return jsonResponse({ ...seriesUnmatched(), status: "pending" });
      if (url.includes("/apply")) return jsonResponse({ ...seriesUnmatched(), status: "applied" });
      if (url.includes("/api/modes/series/rename/proposals"))
        return jsonResponse([seriesUnmatched()]);
      return jsonResponse([]);
    });

    render(() => <Rename />);
    fireEvent.click(await screen.findByText("Series"));
    await screen.findByText("Show.S01E02.mkv");
    const row = screen.getByText("Show.S01E02.mkv").closest("tr, [data-proposal-row]")! as HTMLElement;
    fireEvent.change(within(row).getByRole("combobox"), { target: { value: "review" } });
    fireEvent.click(within(row).getByRole("button", { name: /Apply selected action/ }));

    const dialog = await screen.findByRole("dialog", { name: /Review/ });
    await within(dialog).findByText("Current name");
    fireEvent.input(within(dialog).getByRole("textbox", { name: /^series$/i }), {
      target: { value: "Looney Tunes" },
    });
    fireEvent.input(within(dialog).getByRole("textbox", { name: /^year$/i }), {
      target: { value: "1940" },
    });
    fireEvent.input(within(dialog).getByRole("textbox", { name: /^tmdb id$/i }), {
      target: { value: "55" },
    });
    fireEvent.input(within(dialog).getByRole("textbox", { name: /^season$/i }), {
      target: { value: "1947" },
    });
    fireEvent.input(within(dialog).getByRole("textbox", { name: /^episode$/i }), {
      target: { value: "5" },
    });
    fireEvent.input(within(dialog).getByRole("textbox", { name: /^episode title$/i }), {
      target: { value: "A Hare Grows in Manhattan" },
    });
    await waitFor(() =>
      expect(within(dialog).getByRole("status", { name: /proposed name/i })).toHaveTextContent(
        "Looney Tunes S1947E05 A Hare Grows in Manhattan.mkv",
      ),
    );
    const confirmBtn = within(dialog).getByRole("button", { name: /confirm/i });
    await waitFor(() => expect(confirmBtn).not.toBeDisabled());
    fireEvent.click(confirmBtn);

    await waitFor(() => {
      const repick = calls.filter((c) => c.url.includes("/repick") && c.method === "POST");
      const apply = calls.filter((c) => c.url.endsWith("/apply") && c.method === "POST");
      expect(repick).toHaveLength(1);
      expect(repick[0]!.body).toEqual({
        title: "Looney Tunes",
        tmdbId: 55,
        year: 1940,
        seasonNumber: 1947,
        episodeNumber: 5,
        episodeTitle: "A Hare Grows in Manhattan",
      });
      expect(apply).toHaveLength(1);
    });
  });
});
