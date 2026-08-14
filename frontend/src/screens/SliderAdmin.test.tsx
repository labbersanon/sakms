// SliderAdmin tests — create (fixed feed + reference-list-backed filter
// types), edit, delete, reorder (drag-and-drop), enabled toggle, and the
// target auto-correction for studio (movie-only) / network (tv-only) filter
// types. Conventions mirror Settings.test.tsx (stubFetch/defaultGet/Call).
//
// This screen no longer owns its list rendering: it renders Discover's
// <RowEditor> verbatim, so the row surface asserted here is RowEditor's — a ⠿
// grip handle per row (drag-and-drop, NOT the ▲▼ buttons this file used to
// test), a title-only body with no filter/target subtitle, an Enabled
// checkbox, Delete, and this screen's one injected Edit RowAction.
//
// A real solid-dnd pointer drag IS simulable here — see dragRow below, copied
// from AdultRowAdmin.test.tsx rather than hoisted into a shared helper (a
// handful of callers duplicating is this repo's stance; RssFeedAdmin.test.tsx
// carries a third copy). The "not simulable in jsdom" posture the earlier tests
// recorded was true only of a drag against jsdom's default all-zero
// getBoundingClientRect, which collapses every droppable onto one point; the
// stale claim RowEditor.test.tsx used to carry has since been corrected there.

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@solidjs/testing-library";
import { reorderKeys } from "./discover/RowEditor";
import { SliderAdminSection, sliderIdsFromRowKeys } from "./SliderAdmin";
import { jsonResponse, noContent } from "../testing/http";


type Call = { url: string; method: string; body: unknown };
type Override = (
  url: string,
  init?: RequestInit,
) => Response | undefined | Promise<Response | undefined>;

const slider = (over: Partial<Record<string, unknown>> = {}) => ({
  id: 1,
  title: "Trending Movies",
  filterType: "trending",
  filterValue: "",
  target: "mixed",
  sortOrder: 0,
  enabled: true,
  createdAt: "2026-07-14T00:00:00Z",
  updatedAt: "2026-07-14T00:00:00Z",
  ...over,
});

function defaultGet(url: string): Response | undefined {
  if (url.includes("/api/discover/sliders")) return jsonResponse([]);
  if (url.includes("/discover/genres")) return jsonResponse([]);
  if (url.includes("/api/discover/studios")) return jsonResponse([]);
  if (url.includes("/api/discover/networks")) return jsonResponse([]);
  if (url.includes("/api/discover/keywords")) return jsonResponse([]);
  return undefined;
}

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
    if (method === "GET") {
      const d = defaultGet(url);
      if (d) return d;
    }
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

// dragRow drives a REAL solid-dnd pointer drag of the row at fromIndex onto the
// slot of the row at toIndex. jsdom computes no layout, so every
// getBoundingClientRect is all-zero and solid-dnd's closestCenter sees every
// droppable at the same point; stubbing a 100px-tall rect per row is what makes
// the collision answer meaningful. The stubs are ELEMENT-LOCAL on purpose —
// patching Element.prototype would leak into every other test in this file.
// The [aria-label^="Drag "] filter is load-bearing too: FilterValuePicker's
// keyword-result <li>s would otherwise be rect-stubbed as if they were rows.
// Then: pointerdown on the ⠿ grip (the only activator), a pointermove past the
// sensor's 10px activation distance, and pointerup to commit. Landing the move
// exactly on the target row's centre is what makes the resulting order exact.
const dragRow = (fromIndex: number, toIndex: number) => {
  const lis = Array.from(document.querySelectorAll("li")).filter((li) =>
    li.querySelector('[aria-label^="Drag "]'),
  );
  lis.forEach((li, i) => {
    li.getBoundingClientRect = () =>
      ({
        x: 0,
        y: i * 100,
        left: 0,
        top: i * 100,
        right: 500,
        bottom: i * 100 + 100,
        width: 500,
        height: 100,
        toJSON: () => ({}),
      }) as DOMRect;
  });
  const handle = lis[fromIndex]!.querySelector(
    '[aria-label^="Drag "]',
  ) as HTMLElement;
  fireEvent.pointerDown(handle, {
    clientX: 250,
    clientY: fromIndex * 100 + 50,
    button: 0,
  });
  const to = { clientX: 250, clientY: toIndex * 100 + 50 };
  fireEvent.pointerMove(document, to);
  fireEvent.pointerUp(document, to);
};

const isReorder = (c: Call) =>
  c.method === "POST" && c.url.includes("/reorder");

describe("SliderAdminSection — list", () => {
  it("shows RowEditor's empty state — and STILL offers '+ New slider' (C4)", async () => {
    stubFetch();
    render(() => <SliderAdminSection />);
    // The copy is RowEditor's own ("No custom sliders yet." was this screen's,
    // and died with its list block).
    expect(await screen.findByText("No rows yet.")).toBeInTheDocument();
    // The create affordance lives in RowEditor's `footer`, which is a SIBLING
    // of the empty-state <Show>, never nested inside it. Nested, a fresh
    // install would render "No rows yet." with no way to create its first
    // slider — the single most likely way to break this conversion.
    expect(screen.getByText("+ New slider")).toBeInTheDocument();
  });

  // The row is title-only now: the "Trending · mixed" subtitle this test used
  // to pin is deliberately gone. Asserting only its absence would record the
  // loss without recording where it went, so this pins the MOVE — the filter
  // type is still reachable, one Edit click away, in SliderForm.
  it("renders a title-only row; the filter summary moved into the form", async () => {
    stubFetch((url) => {
      if (url.includes("/api/discover/sliders") && !url.includes("/reorder"))
        return jsonResponse([slider({ id: 1, title: "Trending Movies" })]);
      return undefined;
    });
    render(() => <SliderAdminSection />);
    expect(await screen.findByText("Trending Movies")).toBeInTheDocument();
    expect(screen.queryByText(/Trending · mixed/)).toBeNull();

    fireEvent.click(screen.getByLabelText("Edit Trending Movies"));
    const filterType = (await screen.findByLabelText(
      "Filter type",
    )) as HTMLSelectElement;
    expect(filterType.value).toBe("trending");
  });

  // AC3: the Edit action renders a real icon-library SVG, not a unicode glyph.
  it("the Edit action renders an <svg> icon and no unicode glyph", async () => {
    stubFetch((url) => {
      if (url.includes("/api/discover/sliders") && !url.includes("/reorder"))
        return jsonResponse([slider({ id: 1, title: "Trending Movies" })]);
      return undefined;
    });
    render(() => <SliderAdminSection />);
    const edit = await screen.findByLabelText("Edit Trending Movies");
    expect(edit.querySelector("svg")).toBeTruthy();
    expect(edit.textContent).toBe("");
  });
});

describe("SliderAdminSection — create", () => {
  it("creates a fixed-feed slider with no filter value required", async () => {
    const calls = stubFetch();
    render(() => <SliderAdminSection />);
    fireEvent.click(await screen.findByText("+ New slider"));
    fireEvent.input(screen.getByLabelText("Slider title"), {
      target: { value: "My Upcoming Row" },
    });
    // Default filterType is "upcoming" (a fixed feed) — no value field shown.
    expect(screen.queryByLabelText("Genre")).toBeNull();
    fireEvent.click(screen.getByText("Create slider"));
    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.method === "POST" && c.url.endsWith("/api/discover/sliders"),
        ),
      ).toBe(true),
    );
    const post = calls.find(
      (c) => c.method === "POST" && c.url.endsWith("/api/discover/sliders"),
    )!;
    expect(post.body).toEqual({
      title: "My Upcoming Row",
      filterType: "upcoming",
      filterValue: "",
      target: "mixed",
      enabled: true,
    });
  });

  it("rejects a genre slider with no genre selected (no POST fired)", async () => {
    const calls = stubFetch();
    render(() => <SliderAdminSection />);
    fireEvent.click(await screen.findByText("+ New slider"));
    fireEvent.input(screen.getByLabelText("Slider title"), {
      target: { value: "By Genre" },
    });
    fireEvent.change(screen.getByLabelText("Filter type"), {
      target: { value: "genre" },
    });
    fireEvent.click(screen.getByText("Create slider"));
    await screen.findByText(/select a genre value first/i);
    expect(
      calls.some(
        (c) => c.method === "POST" && c.url.endsWith("/api/discover/sliders"),
      ),
    ).toBe(false);
  });

  it("creates a genre slider once a genre is picked from the fetched list", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover/genres"))
        return jsonResponse([{ id: 28, name: "Action" }]);
      return undefined;
    });
    render(() => <SliderAdminSection />);
    fireEvent.click(await screen.findByText("+ New slider"));
    fireEvent.input(screen.getByLabelText("Slider title"), {
      target: { value: "Action Movies" },
    });
    fireEvent.change(screen.getByLabelText("Filter type"), {
      target: { value: "genre" },
    });
    const genreSelect = (await screen.findByLabelText(
      "Genre",
    )) as HTMLSelectElement;
    await waitFor(() =>
      expect(within(genreSelect).getByText("Action")).toBeInTheDocument(),
    );
    fireEvent.change(genreSelect, { target: { value: "28" } });
    fireEvent.click(screen.getByText("Create slider"));
    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.method === "POST" && c.url.endsWith("/api/discover/sliders"),
        ),
      ).toBe(true),
    );
    const post = calls.find(
      (c) => c.method === "POST" && c.url.endsWith("/api/discover/sliders"),
    )!;
    expect(post.body).toEqual({
      title: "Action Movies",
      filterType: "genre",
      filterValue: "28",
      target: "mixed",
      enabled: true,
    });
  });

  it("keyword search only fetches on Search click, not per keystroke", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/discover/keywords"))
        return jsonResponse([{ id: 99, name: "heist" }]);
      return undefined;
    });
    render(() => <SliderAdminSection />);
    fireEvent.click(await screen.findByText("+ New slider"));
    fireEvent.change(screen.getByLabelText("Filter type"), {
      target: { value: "keyword" },
    });
    const input = await screen.findByLabelText("Keyword search");
    fireEvent.input(input, { target: { value: "h" } });
    fireEvent.input(input, { target: { value: "he" } });
    fireEvent.input(input, { target: { value: "heist" } });
    // Typing alone must not fire any /api/discover/keywords request.
    expect(calls.some((c) => c.url.includes("/api/discover/keywords"))).toBe(
      false,
    );
    fireEvent.click(screen.getByText("Search"));
    await waitFor(() =>
      expect(
        calls.some((c) => c.url.includes("/api/discover/keywords?q=heist")),
      ).toBe(true),
    );
    expect(
      calls.filter((c) => c.url.includes("/api/discover/keywords")),
    ).toHaveLength(1);
  });

  it("clears a stale filter value when switching between reference-list filter types", async () => {
    stubFetch((url) => {
      if (url.includes("/api/discover/studios"))
        return jsonResponse([{ id: 1, name: "A24" }]);
      return undefined;
    });
    render(() => <SliderAdminSection />);
    fireEvent.click(await screen.findByText("+ New slider"));
    fireEvent.change(screen.getByLabelText("Filter type"), {
      target: { value: "studio" },
    });
    const studioSelect = (await screen.findByLabelText(
      "Studio",
    )) as HTMLSelectElement;
    await waitFor(() =>
      expect(within(studioSelect).getByText("A24")).toBeInTheDocument(),
    );
    fireEvent.change(studioSelect, { target: { value: "1" } });
    expect(studioSelect.value).toBe("1");
    // Switching to network must not carry the studio id (1) over as a
    // network id — the value resets so the operator must pick explicitly.
    fireEvent.change(screen.getByLabelText("Filter type"), {
      target: { value: "network" },
    });
    const networkSelect = (await screen.findByLabelText(
      "Network",
    )) as HTMLSelectElement;
    expect(networkSelect.value).toBe("");
  });

  it("auto-corrects target when switching to studio (movie-only)", async () => {
    stubFetch();
    render(() => <SliderAdminSection />);
    fireEvent.click(await screen.findByText("+ New slider"));
    const targetSelect = (await screen.findByLabelText(
      "Target",
    )) as HTMLSelectElement;
    fireEvent.change(targetSelect, { target: { value: "tv" } });
    expect(targetSelect.value).toBe("tv");
    fireEvent.change(screen.getByLabelText("Filter type"), {
      target: { value: "studio" },
    });
    // "tv" is not a valid target for studio (movie-only) — corrected to "movie".
    await waitFor(() => expect(targetSelect.value).toBe("movie"));
    // The now-invalid "tv" option must not even be selectable.
    expect(
      within(targetSelect).queryByRole("option", { name: "tv" }),
    ).toBeNull();
  });

  it("cancel closes the form without posting", async () => {
    const calls = stubFetch();
    render(() => <SliderAdminSection />);
    fireEvent.click(await screen.findByText("+ New slider"));
    fireEvent.click(screen.getByText("Cancel"));
    expect(screen.queryByLabelText("Slider title")).toBeNull();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });
});

describe("SliderAdminSection — edit", () => {
  it("Edit pre-fills the form and Save PUTs the updated slider", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/discover/sliders") && !url.includes("/reorder"))
        return jsonResponse([
          slider({ id: 5, title: "Old Title", filterType: "popular" }),
        ]);
      return undefined;
    });
    render(() => <SliderAdminSection />);
    // The Edit affordance is an icon RowAction now — its aria-label is the
    // only text it carries. (The old "Editing…" button-text swap is gone with
    // it; nothing asserted that string, and the form rendering in RowEditor's
    // footer below the list is the indicator.)
    fireEvent.click(await screen.findByLabelText("Edit Old Title"));
    const titleInput = (await screen.findByLabelText(
      "Slider title",
    )) as HTMLInputElement;
    expect(titleInput.value).toBe("Old Title");
    fireEvent.input(titleInput, { target: { value: "New Title" } });
    fireEvent.click(screen.getByText("Save changes"));
    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.method === "PUT" && c.url.includes("/api/discover/sliders/5"),
        ),
      ).toBe(true),
    );
    const put = calls.find(
      (c) => c.method === "PUT" && c.url.includes("/api/discover/sliders/5"),
    )!;
    expect(put.body).toEqual({
      title: "New Title",
      filterType: "popular",
      filterValue: "",
      target: "mixed",
      enabled: true,
    });
  });
});

describe("SliderAdminSection — delete", () => {
  it("Delete confirms then DELETEs that slider", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/discover/sliders") && !url.includes("/reorder"))
        return jsonResponse([slider({ id: 7, title: "Doomed Row" })]);
      return undefined;
    });
    render(() => <SliderAdminSection />);
    fireEvent.click(await screen.findByText("Delete"));
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "DELETE" && c.url.includes("/api/discover/sliders/7"),
        ),
      ).toBe(true),
    );
  });
});

describe("SliderAdminSection — reorder", () => {
  it("every row carries a ⠿ drag handle and no ▲▼ buttons survive", async () => {
    stubFetch((url) => {
      if (url.includes("/api/discover/sliders") && !url.includes("/reorder"))
        return jsonResponse([
          slider({ id: 1, title: "First" }),
          slider({ id: 2, title: "Second" }),
        ]);
      return undefined;
    });
    render(() => <SliderAdminSection />);
    await screen.findByText("First");
    expect(screen.getByLabelText("Drag First")).toBeTruthy();
    expect(screen.getByLabelText("Drag Second")).toBeTruthy();
    // The ▲▼ pair this screen used to own is gone entirely — Settings and
    // Discover now share ONE reorder mechanism.
    expect(screen.queryByLabelText(/Move .* up/)).toBeNull();
    expect(screen.queryByLabelText(/Move .* down/)).toBeNull();
  });

  it("a drag POSTs /reorder with the full new id order", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/discover/sliders") && !url.includes("/reorder"))
        return jsonResponse([
          slider({ id: 1, title: "First" }),
          slider({ id: 2, title: "Second" }),
          slider({ id: 3, title: "Third" }),
        ]);
      return undefined;
    });
    render(() => <SliderAdminSection />);
    await screen.findByLabelText("Drag First");
    dragRow(0, 2);
    await waitFor(() => expect(calls.some(isReorder)).toBe(true));
    // "First" moved into "Third"'s slot — the full id set, every id exactly
    // once, in the new display order (Store.Reorder rejects anything else).
    expect(calls.find(isReorder)!.body).toEqual({ ids: [2, 3, 1] });
  });

  // The pure mapping the drag test exercises end to end, pinned on its own so
  // the exact POST body is covered without a simulated pointer — this is what
  // justifies sliderIdsFromRowKeys being exported rather than inlined.
  it("sliderIdsFromRowKeys maps reorderKeys' output back to numeric ids", () => {
    expect(sliderIdsFromRowKeys(reorderKeys(["1", "2"], "2", "1"))).toEqual([
      2, 1,
    ]);
    expect(
      sliderIdsFromRowKeys(reorderKeys(["11", "22", "33"], "11", "33")),
    ).toEqual([22, 33, 11]);
  });

  // R3: the reorder-failure message lives in RowEditor's `description` slot
  // now (above the list, where it always was). Moving it there must not have
  // made it invisible — a 400 from Store.Reorder is the operator's only sign
  // the drag didn't persist.
  it("a failed reorder still renders listError", async () => {
    stubFetch((url, init) => {
      if ((init?.method ?? "GET").toUpperCase() === "POST" &&
          url.includes("/reorder"))
        return new Response(JSON.stringify({ error: "slider set changed" }), {
          status: 400,
          headers: { "Content-Type": "application/json" },
        });
      if (url.includes("/api/discover/sliders") && !url.includes("/reorder"))
        return jsonResponse([
          slider({ id: 1, title: "First" }),
          slider({ id: 2, title: "Second" }),
        ]);
      return undefined;
    });
    render(() => <SliderAdminSection />);
    await screen.findByLabelText("Drag First");
    dragRow(0, 1);
    expect(await screen.findByText(/slider set changed/)).toBeInTheDocument();
  });
});

describe("SliderAdminSection — enabled toggle", () => {
  it("toggling the checkbox PUTs the slider with enabled flipped", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/discover/sliders") && !url.includes("/reorder"))
        return jsonResponse([
          slider({ id: 3, title: "Togglable", enabled: true }),
        ]);
      return undefined;
    });
    render(() => <SliderAdminSection />);
    const toggle = (await screen.findByLabelText(
      "Togglable enabled",
    )) as HTMLInputElement;
    expect(toggle.checked).toBe(true);
    fireEvent.click(toggle);
    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.method === "PUT" && c.url.includes("/api/discover/sliders/3"),
        ),
      ).toBe(true),
    );
    const put = calls.find(
      (c) => c.method === "PUT" && c.url.includes("/api/discover/sliders/3"),
    )!;
    expect((put.body as { enabled: boolean }).enabled).toBe(false);
  });
});

describe("SliderAdminSection — no bulk actions", () => {
  it("has no save-all / apply-all / delete-all affordance", async () => {
    stubFetch();
    render(() => <SliderAdminSection />);
    await screen.findByText("+ New slider");
    expect(screen.queryByText(/save all/i)).toBeNull();
    expect(screen.queryByText(/apply all/i)).toBeNull();
    expect(screen.queryByText(/delete all/i)).toBeNull();
  });
});
