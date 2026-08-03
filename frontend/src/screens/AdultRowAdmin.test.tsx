// AdultRowAdmin tests — create (with and without a genre filter), the
// empty-genre-list fallback, edit, delete, reorder, enabled toggle, and the
// global scan-interval control. Conventions mirror SliderAdmin.test.tsx
// (stubFetch/defaultGet/Call).
//
// This screen no longer owns its list rendering: it renders Discover's
// <RowEditor> verbatim, so the row surface asserted here is RowEditor's —
// a ⠿ grip handle per row (drag-and-drop, NOT ▲▼ buttons), a title-only body,
// an Enabled checkbox, Delete, and this screen's one injected Edit RowAction.
//
// A real solid-dnd pointer drag IS simulable here — see dragRow below. The
// earlier posture ("not simulable in jsdom", inherited from RowEditor.test.tsx)
// was true only of a drag against jsdom's default all-zero
// getBoundingClientRect, which collapses every droppable onto one point and
// makes closestCenter's answer meaningless. Give the row <li>s real rects and
// the whole pointerdown → pointermove → pointerup path runs, so THIS file — not
// useAdultRowOrder.test.ts — is where the `newestrow:{id}` → id extraction and
// the resulting POST /reorder body are actually covered end to end.
// useAdultRowOrder.test.ts covers the hook's own contract (override ordering,
// revert-on-failure) below that boundary; reorderKeys' pure key-order logic has
// its own unit tests in RowEditor.test.tsx.

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@solidjs/testing-library";
import { AdultRowAdminSection } from "./AdultRowAdmin";

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
const noContent = (): Response => new Response(null, { status: 204 });

type Call = { url: string; method: string; body: unknown };
type Override = (
  url: string,
  init?: RequestInit,
) => Response | undefined | Promise<Response | undefined>;

const row = (over: Partial<Record<string, unknown>> = {}) => ({
  id: 1,
  title: "Newest Movies",
  rowType: "movie",
  genreFilter: undefined,
  sortOrder: 0,
  enabled: true,
  createdAt: "2026-07-14T00:00:00Z",
  updatedAt: "2026-07-14T00:00:00Z",
  ...over,
});

// isRowsList matches the CRUD list endpoint but not its /reorder or /genres
// siblings (both are prefixed by /newest-rows).
const isRowsList = (url: string) =>
  url.includes("/api/modes/adult/newest-rows") &&
  !url.includes("/reorder") &&
  !url.includes("/genres");

function defaultGet(url: string): Response | undefined {
  if (url.includes("/newest-rows/genres")) return jsonResponse([]);
  if (isRowsList(url)) return jsonResponse([]);
  if (url.includes("/api/settings/adult-newest-scan-interval"))
    return jsonResponse({ intervalSeconds: 0 });
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
  c.method === "POST" && c.url.includes("/newest-rows/reorder");

describe("AdultRowAdminSection — list", () => {
  it("shows RowEditor's empty state — and STILL offers '+ New row' (C4)", async () => {
    stubFetch();
    render(() => <AdultRowAdminSection />);
    // The copy is RowEditor's own ("No custom rows yet." was this screen's, and
    // went with its hand-rolled list). The second assertion is the one that
    // matters: footer must render as a SIBLING of the empty-state <Show>, or a
    // fresh install can never create its first row.
    expect(await screen.findByText("No rows yet.")).toBeInTheDocument();
    expect(screen.getByText("+ New row")).toBeInTheDocument();
  });

  it("lists an existing row title-only — no type/genre subtitle (AC4)", async () => {
    stubFetch((url) => {
      if (isRowsList(url))
        return jsonResponse([
          row({ id: 1, title: "Action Scenes", rowType: "scene", genreFilter: "Action" }),
        ]);
      return undefined;
    });
    render(() => <AdultRowAdminSection />);
    const title = await screen.findByText("Action Scenes");
    // Simple white title-only rows, matching Discover's: the "Scene · Action"
    // summary line the old hand-rolled entry carried is deliberately gone.
    expect(screen.queryByText(/Scene · Action/)).toBeNull();
    const li = title.closest("li") as HTMLElement;
    expect(within(li).getByLabelText("Drag Action Scenes")).toBeTruthy();
  });
});

describe("AdultRowAdminSection — create", () => {
  it("creates a row with no genre filter (genreFilter omitted from body)", async () => {
    const calls = stubFetch();
    render(() => <AdultRowAdminSection />);
    fireEvent.click(await screen.findByText("+ New row"));
    fireEvent.input(screen.getByLabelText("Row title"), {
      target: { value: "My Performers" },
    });
    fireEvent.change(screen.getByLabelText("Row type"), {
      target: { value: "performer" },
    });
    fireEvent.click(screen.getByText("Create row"));
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "POST" &&
            c.url.endsWith("/api/modes/adult/newest-rows"),
        ),
      ).toBe(true),
    );
    const post = calls.find(
      (c) =>
        c.method === "POST" && c.url.endsWith("/api/modes/adult/newest-rows"),
    )!;
    expect(post.body).toEqual({
      title: "My Performers",
      rowType: "performer",
      enabled: true,
    });
  });

  it("rejects a blank title (no POST fired)", async () => {
    const calls = stubFetch();
    render(() => <AdultRowAdminSection />);
    fireEvent.click(await screen.findByText("+ New row"));
    fireEvent.click(screen.getByText("Create row"));
    await screen.findByText(/title is required/i);
    expect(
      calls.some(
        (c) =>
          c.method === "POST" &&
          c.url.endsWith("/api/modes/adult/newest-rows"),
      ),
    ).toBe(false);
  });

  it("creates a row with a genre filter picked from the fetched list", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/newest-rows/genres"))
        return jsonResponse(["Action", "Comedy"]);
      return undefined;
    });
    render(() => <AdultRowAdminSection />);
    fireEvent.click(await screen.findByText("+ New row"));
    fireEvent.input(screen.getByLabelText("Row title"), {
      target: { value: "Comedy Movies" },
    });
    // The select renders a disabled fallback until the genre resource resolves,
    // then swaps to the real (enabled) select — wait for the option, then query
    // the current select fresh (the fallback node is detached by then).
    await screen.findByRole("option", { name: "Comedy" });
    const genreSelect = screen.getByLabelText(
      "Genre filter",
    ) as HTMLSelectElement;
    fireEvent.change(genreSelect, { target: { value: "Comedy" } });
    fireEvent.click(screen.getByText("Create row"));
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "POST" &&
            c.url.endsWith("/api/modes/adult/newest-rows"),
        ),
      ).toBe(true),
    );
    const post = calls.find(
      (c) =>
        c.method === "POST" && c.url.endsWith("/api/modes/adult/newest-rows"),
    )!;
    expect(post.body).toEqual({
      title: "Comedy Movies",
      rowType: "movie",
      genreFilter: "Comedy",
      enabled: true,
    });
  });

  it("disables the genre select with a hint when no genres exist yet", async () => {
    stubFetch(); // defaultGet returns [] for genres
    render(() => <AdultRowAdminSection />);
    fireEvent.click(await screen.findByText("+ New row"));
    const genreSelect = (await screen.findByLabelText(
      "Genre filter",
    )) as HTMLSelectElement;
    expect(genreSelect).toBeDisabled();
    expect(
      screen.getByText(/No genres available yet/i),
    ).toBeInTheDocument();
  });

  it("cancel closes the form without posting", async () => {
    const calls = stubFetch();
    render(() => <AdultRowAdminSection />);
    fireEvent.click(await screen.findByText("+ New row"));
    fireEvent.click(screen.getByText("Cancel"));
    expect(screen.queryByLabelText("Row title")).toBeNull();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });
});

describe("AdultRowAdminSection — edit", () => {
  // D1's regression test: RowEditor's built-in controls are Enabled + Delete
  // only, so adopting it verbatim would have deleted the app's ONLY affordance
  // for editing a row's title/rowType/genreFilter. It survives as an injected
  // RowAction, reached by its aria-label (the ✎ glyph itself is aria-hidden).
  it("the injected Edit action opens the form pre-filled, and Save PUTs the updated row", async () => {
    const calls = stubFetch((url) => {
      if (isRowsList(url))
        return jsonResponse([
          row({ id: 5, title: "Old Title", rowType: "studio" }),
        ]);
      return undefined;
    });
    render(() => <AdultRowAdminSection />);
    fireEvent.click(await screen.findByLabelText("Edit Old Title"));
    const titleInput = (await screen.findByLabelText(
      "Row title",
    )) as HTMLInputElement;
    expect(titleInput.value).toBe("Old Title");
    fireEvent.input(titleInput, { target: { value: "New Title" } });
    fireEvent.click(screen.getByText("Save changes"));
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "PUT" &&
            c.url.includes("/api/modes/adult/newest-rows/5"),
        ),
      ).toBe(true),
    );
    const put = calls.find(
      (c) =>
        c.method === "PUT" && c.url.includes("/api/modes/adult/newest-rows/5"),
    )!;
    expect((put.body as { title: string }).title).toBe("New Title");
    expect((put.body as { rowType: string }).rowType).toBe("studio");
  });
});

describe("AdultRowAdminSection — delete", () => {
  it("Delete confirms then DELETEs that row", async () => {
    const calls = stubFetch((url) => {
      if (isRowsList(url))
        return jsonResponse([row({ id: 7, title: "Doomed Row" })]);
      return undefined;
    });
    render(() => <AdultRowAdminSection />);
    fireEvent.click(await screen.findByText("Delete"));
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "DELETE" &&
            c.url.includes("/api/modes/adult/newest-rows/7"),
        ),
      ).toBe(true),
    );
  });
});

describe("AdultRowAdminSection — reorder", () => {
  it("every row carries a ⠿ drag handle and no ▲▼ buttons survive", async () => {
    stubFetch((url) => {
      if (isRowsList(url))
        return jsonResponse([
          row({ id: 1, title: "First" }),
          row({ id: 2, title: "Second" }),
        ]);
      return undefined;
    });
    render(() => <AdultRowAdminSection />);
    await screen.findByText("First");
    expect(screen.getByLabelText("Drag First")).toBeTruthy();
    expect(screen.getByLabelText("Drag Second")).toBeTruthy();
    // The ▲▼ pair this screen used to own is gone entirely — Discover and
    // Settings now share ONE reorder mechanism (and one persisted order).
    expect(screen.queryByLabelText(/Move .* up/)).toBeNull();
    expect(screen.queryByLabelText(/Move .* down/)).toBeNull();
  });

  // The one assertion that would silently pass against a broken key→id
  // extraction if it lived a layer down: useAdultRowOrder.test.ts can only prove
  // persistOrder passes through the ids it is HANDED, never that this screen
  // derived them correctly. `Number("newestrow".length)`-style off-by-one on the
  // prefix yields NaN, which JSON.stringify writes as null — so the body below
  // would be {ids:[null,null,null]} and nothing else in the suite would notice.
  it("a drag POSTs /reorder with the ACTUAL numeric ids the newestrow: keys map to", async () => {
    const calls = stubFetch((url) => {
      if (isRowsList(url))
        return jsonResponse([
          row({ id: 11, title: "First" }),
          row({ id: 22, title: "Second" }),
          row({ id: 33, title: "Third" }),
        ]);
      return undefined;
    });
    render(() => <AdultRowAdminSection />);
    await screen.findByLabelText("Drag First");
    dragRow(0, 2);
    await waitFor(() => expect(calls.some(isReorder)).toBe(true));
    // "First" moved into "Third"'s slot — the full id set, every id exactly
    // once, in the new display order (Store.Reorder rejects anything else).
    expect(calls.find(isReorder)!.body).toEqual({ ids: [22, 33, 11] });
  });

  // M3's guard for R2: moving this screen's error lines into RowEditor's
  // `description` slot must not make them invisible. Asserted through a failed
  // DELETE; the reorder half of that same surface has its own test below.
  it("a mutation failure's message renders in the editor's description slot", async () => {
    stubFetch((url, init) => {
      if ((init?.method ?? "GET").toUpperCase() === "DELETE")
        return new Response(JSON.stringify({ error: "row set changed" }), {
          status: 400,
          headers: { "Content-Type": "application/json" },
        });
      if (isRowsList(url))
        return jsonResponse([row({ id: 9, title: "Doomed" })]);
      return undefined;
    });
    render(() => <AdultRowAdminSection />);
    fireEvent.click(await screen.findByText("Delete"));
    expect(await screen.findByText(/row set changed/)).toBeInTheDocument();
  });

  // The masking regression itself: displayError used to be
  // `listError() || reorderError()` with neither handler clearing the other, so
  // once ANY delete/toggle had failed, every subsequent reorder failure was
  // invisible forever — the operator saw a stale, unrelated message and no sign
  // the drag hadn't persisted. Both mutations now clear the other signal on
  // entry, so only the latest failure is ever shown.
  it("a reorder failure is shown even after an earlier delete failure, and replaces it", async () => {
    stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (method === "DELETE")
        return new Response(JSON.stringify({ error: "delete blew up" }), {
          status: 400,
          headers: { "Content-Type": "application/json" },
        });
      if (method === "POST" && url.includes("/newest-rows/reorder"))
        return new Response(JSON.stringify({ error: "row set changed" }), {
          status: 400,
          headers: { "Content-Type": "application/json" },
        });
      if (isRowsList(url))
        return jsonResponse([
          row({ id: 11, title: "First" }),
          row({ id: 22, title: "Second" }),
        ]);
      return undefined;
    });
    render(() => <AdultRowAdminSection />);

    // 1. A delete fails — listError is set and rendered.
    fireEvent.click((await screen.findAllByText("Delete"))[0]!);
    expect(await screen.findByText(/delete blew up/)).toBeInTheDocument();

    // 2. A drag then fails too. Its message must reach the DOM...
    dragRow(0, 1);
    expect(await screen.findByText(/row set changed/)).toBeInTheDocument();
    // ...and the stale delete error must be gone, not stacked beside it.
    expect(screen.queryByText(/delete blew up/)).toBeNull();
  });
});

describe("AdultRowAdminSection — enabled toggle", () => {
  it("toggling the checkbox PUTs the row with enabled flipped", async () => {
    const calls = stubFetch((url) => {
      if (isRowsList(url))
        return jsonResponse([row({ id: 3, title: "Togglable", enabled: true })]);
      return undefined;
    });
    render(() => <AdultRowAdminSection />);
    const toggle = (await screen.findByLabelText(
      "Togglable enabled",
    )) as HTMLInputElement;
    expect(toggle.checked).toBe(true);
    fireEvent.click(toggle);
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "PUT" &&
            c.url.includes("/api/modes/adult/newest-rows/3"),
        ),
      ).toBe(true),
    );
    const put = calls.find(
      (c) =>
        c.method === "PUT" && c.url.includes("/api/modes/adult/newest-rows/3"),
    )!;
    expect((put.body as { enabled: boolean }).enabled).toBe(false);
  });
});

describe("AdultRowAdminSection — scan interval", () => {
  it("saving the scan interval (Days/Hours/Minutes picker) PUTs the new value in seconds", async () => {
    const calls = stubFetch();
    render(() => <AdultRowAdminSection />);
    // Value 0 defaults the picker to the "Hours" unit; typing "1" there means
    // 1 hour = 3600 seconds.
    const input = (await screen.findByLabelText(
      "Background scan interval",
    )) as HTMLInputElement;
    fireEvent.input(input, { target: { value: "1" } });
    fireEvent.click(screen.getByText("Save"));
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "PUT" &&
            c.url.includes("/api/settings/adult-newest-scan-interval"),
        ),
      ).toBe(true),
    );
    const put = calls.find(
      (c) =>
        c.method === "PUT" &&
        c.url.includes("/api/settings/adult-newest-scan-interval"),
    )!;
    expect(put.body).toEqual({ intervalSeconds: 3600 });
  });
});

describe("AdultRowAdminSection — no bulk actions", () => {
  it("has no save-all / apply-all / delete-all affordance", async () => {
    stubFetch();
    render(() => <AdultRowAdminSection />);
    await screen.findByText("+ New row");
    expect(screen.queryByText(/save all/i)).toBeNull();
    expect(screen.queryByText(/apply all/i)).toBeNull();
    expect(screen.queryByText(/delete all/i)).toBeNull();
  });
});
