// AdultRowAdmin tests — create (with and without a genre filter), the
// empty-genre-list fallback, edit, delete, reorder (button-based), enabled
// toggle, and the global scan-interval control. Conventions mirror
// SliderAdmin.test.tsx (stubFetch/defaultGet/Call).

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

describe("AdultRowAdminSection — list", () => {
  it("shows the empty state with no rows", async () => {
    stubFetch();
    render(() => <AdultRowAdminSection />);
    expect(await screen.findByText("No custom rows yet.")).toBeInTheDocument();
  });

  it("lists an existing row with its summary", async () => {
    stubFetch((url) => {
      if (isRowsList(url))
        return jsonResponse([
          row({ id: 1, title: "Action Scenes", rowType: "scene", genreFilter: "Action" }),
        ]);
      return undefined;
    });
    render(() => <AdultRowAdminSection />);
    expect(await screen.findByText("Action Scenes")).toBeInTheDocument();
    expect(screen.getByText(/Scene · Action/)).toBeInTheDocument();
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
  it("Edit pre-fills the form and Save PUTs the updated row", async () => {
    const calls = stubFetch((url) => {
      if (isRowsList(url))
        return jsonResponse([
          row({ id: 5, title: "Old Title", rowType: "studio" }),
        ]);
      return undefined;
    });
    render(() => <AdultRowAdminSection />);
    fireEvent.click(await screen.findByText("Edit"));
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
  it("moving the second row up sends the full new id order", async () => {
    const calls = stubFetch((url) => {
      if (isRowsList(url))
        return jsonResponse([
          row({ id: 1, title: "First" }),
          row({ id: 2, title: "Second" }),
        ]);
      return undefined;
    });
    render(() => <AdultRowAdminSection />);
    await screen.findByText("First");
    const secondRow = screen.getByText("Second").closest("li")!;
    fireEvent.click(within(secondRow).getByLabelText("Move Second up"));
    await waitFor(() =>
      expect(
        calls.some((c) => c.method === "POST" && c.url.includes("/reorder")),
      ).toBe(true),
    );
    const reorder = calls.find(
      (c) => c.method === "POST" && c.url.includes("/reorder"),
    )!;
    expect(reorder.body).toEqual({ ids: [2, 1] });
  });

  it("the first row's Up button is disabled", async () => {
    stubFetch((url) => {
      if (isRowsList(url))
        return jsonResponse([row({ id: 1, title: "Only One" })]);
      return undefined;
    });
    render(() => <AdultRowAdminSection />);
    await screen.findByText("Only One");
    expect(screen.getByLabelText("Move Only One up")).toBeDisabled();
    expect(screen.getByLabelText("Move Only One down")).toBeDisabled();
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
