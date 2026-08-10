// Rename — source-file video preview (queue row pop-out + both SearchTakeover
// mounts). Split out from Rename.test.tsx deliberately, same precedent as
// Rename.repick.test.tsx / Rename.delete.test.tsx: one feature area, own file.
//
// Spec: .omc/plans/autopilot-impl-rename-preview.md §5, §6.3, §8.4, §8.5 #13.
//
// SELECTOR CONVENTION FOR THIS WHOLE FILE (§4/§8.4 #0): the row control is
// `getByLabelText("Open preview for <sourceName>")`; the mounted video is
// `getByLabelText("Preview of <sourceName>")`. Never reuse one string for
// both — SourcePreview.tsx's own header explains why: both are mounted
// simultaneously while the modal is open, so a shared label makes
// getByLabelText throw on multiple matches, and makes "closing unmounts the
// video" assertions silently match the still-present button instead.
//
// The two CRITICAL tests in this file are:
//   - "the Re-pick takeover renders the source-file preview affordance"
//     (AC #4's only end-to-end coverage that Rename.tsx actually PASSES the
//     preview prop, not just that SearchTakeover can accept one).
//   - "Rename's MOVE takeover previews the proposal's OWN mode, not the move
//     target" — the direct regression test for the copy-paste hazard §6.3
//     names: Rename.tsx's Move mount sits one line away from `searchMode={
//     st.kind === "repick" ? props.mode : st.target}`, and copying that
//     `st.target` expression into the preview's src would silently 400 the
//     preview on every Move takeover (prop.Mode != m) with no console error
//     and no visible failure anywhere else. The fixture below deliberately
//     has `st.target !== props.mode` (a Movies proposal, moved to Adult), or
//     this test could not fail.

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@solidjs/testing-library";
import type { Proposal } from "@dto";
import { Rename } from "./Rename";

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

const proposal = (over: Partial<Proposal>): Proposal => ({
  id: 1,
  status: "pending",
  sourceName: "Some.Movie.2021.1080p.mkv",
  rootFolderPath: "/movies",
  title: "Some Movie",
  year: 2021,
  reason: "",
  draftId: "",
  ...over,
});

type Call = { url: string; method: string };
type Handler = (url: string, init?: RequestInit) => Response | Promise<Response>;

const pageOf = (items: Proposal[]) => ({
  items,
  total: items.length,
  limit: 50,
  offset: 0,
});

// Verbatim shape of Rename.test.tsx's / Rename.delete.test.tsx's stubFetch —
// same boilerplate-route handling (organize/events, pending-ids, the
// array->ProposalPage auto-wrap for /rename/proposals responses).
const stubFetch = (handler: Handler) => {
  const calls: Call[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push({ url, method: (init?.method ?? "GET").toUpperCase() });
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

const videoCalls = (calls: Call[]) =>
  calls.filter((c) => c.url.includes("/video"));

afterEach(() => {
  vi.unstubAllGlobals();
  localStorage.clear();
});

describe("Rename — source-file preview (queue row control)", () => {
  it("renders a preview control on pending and unmatched rows", async () => {
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
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("Pending.mkv");

    expect(
      screen.getByLabelText("Open preview for Pending.mkv"),
    ).toBeInTheDocument();
    expect(
      screen.getByLabelText("Open preview for Unmatched.mkv"),
    ).toBeInTheDocument();
  });

  it("renders NO preview control on applied or dismissed rows", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals")) {
        if (url.includes("view=history"))
          return jsonResponse([
            proposal({ id: 3, status: "applied", sourceName: "Applied.mkv" }),
            proposal({
              id: 4,
              status: "dismissed",
              sourceName: "Dismissed.mkv",
            }),
          ]);
        return jsonResponse([
          proposal({ id: 1, status: "pending", sourceName: "Live.mkv" }),
        ]);
      }
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("Live.mkv");
    fireEvent.click(screen.getByText("Show history"));

    // GUARD THE GUARD FIRST (SearchTakeover.test.tsx:557-565's convention):
    // prove the history rows actually rendered before asserting anything is
    // absent from them — otherwise a broken history fetch would make this
    // test pass against an empty table.
    await screen.findByText("Applied.mkv");
    expect(screen.getByText("Dismissed.mkv")).toBeInTheDocument();
    expect(document.querySelectorAll("tbody tr")).toHaveLength(2);

    expect(screen.queryByLabelText("Open preview for Applied.mkv")).toBeNull();
    expect(
      screen.queryByLabelText("Open preview for Dismissed.mkv"),
    ).toBeNull();
  });

  it("the preview URL carries NO candidateIndex", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({ id: 7, sourceName: "Movie.A.mkv" }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("Movie.A.mkv");
    fireEvent.click(screen.getByLabelText("Open preview for Movie.A.mkv"));

    const video = await screen.findByLabelText("Preview of Movie.A.mkv");
    const src = video.getAttribute("src")!;
    expect(src).toBe("/api/modes/movies/proposals/7/video");
    expect(src).not.toContain("candidateIndex");
  });

  it("no bytes are fetched until the operator opens the preview", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({ id: 7, sourceName: "Movie.A.mkv" }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("Movie.A.mkv");
    expect(videoCalls(calls)).toHaveLength(0);

    fireEvent.click(screen.getByLabelText("Open preview for Movie.A.mkv"));
    const video = await screen.findByLabelText("Preview of Movie.A.mkv");

    // jsdom does not fetch media at all, so this preload assertion is the
    // load-bearing half of this test — do not "strengthen" it into something
    // jsdom cannot observe.
    expect(video.getAttribute("preload")).toBe("none");
    expect(videoCalls(calls)).toHaveLength(0);
  });

  it("the preview video is NOT muted", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({ id: 7, sourceName: "Movie.A.mkv" }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("Movie.A.mkv");
    fireEvent.click(screen.getByLabelText("Open preview for Movie.A.mkv"));

    const video = (await screen.findByLabelText(
      "Preview of Movie.A.mkv",
    )) as HTMLVideoElement;
    // The assertion that fails if someone copies Dedup's tile wholesale.
    expect(video.muted).toBe(false);
    expect(video.hasAttribute("muted")).toBe(false);
  });

  it("the action dropdown gains no Preview option", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({ id: 7, sourceName: "Movie.A.mkv" }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    const row = (await screen.findByText("Movie.A.mkv")).closest("tr")!;
    const select = within(row).getByRole("combobox") as HTMLSelectElement;
    const optionLabels = Array.from(select.options).map((o) => o.textContent);
    expect(optionLabels).toEqual([
      "select action",
      "Rename",
      "Search",
      "Dismiss",
      "Delete file",
      "Move to Series",
      "Move to Adult",
    ]);
  });

  // CRITICAL — AC #4's only end-to-end coverage that Rename.tsx actually
  // PASSES the preview prop into its Re-pick mount. SearchTakeover.test.tsx's
  // own preview-slot tests only prove the component's contract, not that this
  // screen wires it up. Drives the real gesture per Rename.tsx's own NOTE FOR
  // TESTS/DOCS comment: the row's clickable control is labelled literally
  // "Apply" and does not relabel itself — select it by its accessible name.
  it("the Re-pick takeover renders the source-file preview affordance", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({ id: 7, sourceName: "Movie.A.mkv" }),
        ]);
      if (url.includes("/tmdb-search")) return jsonResponse([]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    const row = (await screen.findByText("Movie.A.mkv")).closest("tr")!;
    fireEvent.change(within(row).getByRole("combobox"), {
      target: { value: "repick" },
    });
    fireEvent.click(
      within(row).getByRole("button", {
        name: "Apply selected action for Movie.A.mkv",
      }),
    );

    expect(
      await screen.findByText("Preview source file"),
    ).toBeInTheDocument();
    // Collapsed by default — no video mounted yet.
    expect(document.querySelector("video")).toBeNull();
  });

  it("closing the pop-out unmounts the video", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({ id: 7, sourceName: "Movie.A.mkv" }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("Movie.A.mkv");
    fireEvent.click(screen.getByLabelText("Open preview for Movie.A.mkv"));
    await screen.findByLabelText("Preview of Movie.A.mkv");

    fireEvent.click(screen.getByRole("button", { name: "Close" }));

    // Both halves are required: asserting only the video's absence against a
    // shared label would match the surviving button and prove nothing.
    await waitFor(() =>
      expect(screen.queryByLabelText("Preview of Movie.A.mkv")).toBeNull(),
    );
    expect(
      screen.queryByLabelText("Open preview for Movie.A.mkv"),
    ).not.toBeNull();
  });
});

describe("Rename — SearchTakeover preview wiring (§6.3, §8.5 #13)", () => {
  // CRITICAL — the direct regression test for the copy-paste hazard §6.3
  // names. The fixture is a Movies proposal moved to Adult specifically so
  // `st.target !== props.mode` — without that mismatch this test could not
  // fail even if Rename.tsx wired `st.target` into the preview src instead of
  // `props.mode`.
  it("Rename's MOVE takeover previews the proposal's OWN mode, not the move target", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({ id: 7, sourceName: "Movie.A.mkv" }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    const row = (await screen.findByText("Movie.A.mkv")).closest("tr")!;
    fireEvent.change(within(row).getByRole("combobox"), {
      target: { value: "move:adult" },
    });
    fireEvent.click(
      within(row).getByRole("button", {
        name: "Apply selected action for Movie.A.mkv",
      }),
    );

    expect(
      await screen.findByText(/Move .+ to another section/),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByText("Preview source file"));
    const video = await screen.findByLabelText("Preview of Movie.A.mkv");
    const src = video.getAttribute("src")!;
    expect(src).toBe("/api/modes/movies/proposals/7/video");
    expect(src).not.toContain("/adult/");
  });
});
