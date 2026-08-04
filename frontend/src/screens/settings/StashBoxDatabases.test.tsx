// StashBoxDatabases tests — the Settings -> Library -> Adult stash-box
// registry panel (Stage 5 Wave 5.5, plan
// .omc/plans/autopilot-impl-stage5-stashboxdb-ui.md).
//
// Covers the acceptance criteria that are only observable from the UI:
//   AC11  every row renders identically — the two seeded databases carry no
//         badge, no protection, and the same action set as an operator-added
//         one
//   AC9   a drag persists the new cascade order immediately
//   AC7   the Add affordance is replaced by a "5 of 5" note at the cap
//   AC8   Delete is a plain confirm
//   AC6/AC12 a failed stored test tints the row (auto on list-resolve, and
//         via the per-row manual Test)
//   AC2   TPDB never appears in this panel
//   the three-state secret rule holds — an edit that never touches the key
//   must OMIT apiKey, not send ""
//
// Conventions (stubFetch/Call/dragRow) are copied from RssFeedAdmin.test.tsx
// rather than hoisted into a shared helper, matching this repo's stance that
// a handful of duplicating callers is preferable to a test-helper package.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { StashBoxDatabases } from "./StashBoxDatabases";
import type { StashBoxDatabase } from "../../api/stashboxdb";

const jsonResponse = (obj: unknown, status = 200): Response =>
  new Response(JSON.stringify(obj), {
    status,
    headers: { "Content-Type": "application/json" },
  });
const noContent = (): Response => new Response(null, { status: 204 });

type Call = { url: string; method: string; body: unknown };
type Override = (
  url: string,
  init?: RequestInit,
) => Response | undefined | Promise<Response | undefined>;

const database = (over: Partial<StashBoxDatabase> = {}): StashBoxDatabase => ({
  id: 1,
  name: "stashdb",
  endpoint: "https://stashdb.org/graphql",
  priority: 0,
  enabled: true,
  fansiteOnly: false,
  hasApiKey: true,
  keySuffix: "1234",
  updatedAt: "2026-08-04T00:00:00Z",
  ...over,
});

const seeded = (): StashBoxDatabase[] => [
  database(),
  database({
    id: 2,
    name: "fansdb",
    endpoint: "https://fansdb.cc/graphql",
    priority: 1,
    fansiteOnly: true,
    keySuffix: "5678",
  }),
];

const isList = (url: string, method: string) =>
  method === "GET" && url.includes("/api/stashbox-databases");

const stubFetch = (list: StashBoxDatabase[], override?: Override) => {
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
    if (isList(url, method)) return jsonResponse(list);
    return noContent();
  });
  vi.stubGlobal("fetch", fn);
  vi.stubGlobal(
    "confirm",
    vi.fn(() => true),
  );
  return calls;
};

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

// rowFor locates a database's <li> by its drag handle, so the red tint
// (RowDescriptor.danger -> "border-danger bg-danger/10" on the row itself)
// can be asserted on the element that actually carries it. Asserting the
// CLASS rather than a rendered string is deliberate: the tint IS the whole
// failure signal here — the stored-test endpoint deliberately returns no
// diagnostic detail (it would echo the row's operator-supplied endpoint).
const rowFor = (name: string): HTMLElement =>
  Array.from(document.querySelectorAll("li")).find((li) =>
    li.querySelector(`[aria-label="Drag ${name}"]`),
  ) as HTMLElement;

const isTinted = (name: string): boolean =>
  rowFor(name).className.includes("border-danger");

// dragRow drives a REAL solid-dnd pointer drag, copied from
// RssFeedAdmin.test.tsx. jsdom computes no layout, so every
// getBoundingClientRect is all-zero and closestCenter would see every
// droppable at one point; the element-local 100px rects are what make the
// collision answer meaningful.
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

describe("StashBoxDatabases", () => {
  it("renders every database identically — the seeded pair has no special treatment (AC11)", async () => {
    stubFetch(seeded());
    render(() => <StashBoxDatabases />);

    // Both seeded rows appear, and each offers the SAME action set an
    // operator-added row would: Drag, Edit, Test, enabled, Delete.
    for (const name of ["stashdb", "fansdb"]) {
      expect(await screen.findByLabelText(`Drag ${name}`)).toBeTruthy();
      expect(screen.getByLabelText(`Edit ${name}`)).toBeTruthy();
      expect(screen.getByLabelText(`Test ${name}`)).toBeTruthy();
      expect(screen.getByLabelText(`${name} enabled`)).toBeTruthy();
    }
    expect(screen.getAllByRole("button", { name: "Delete" })).toHaveLength(2);

    // No "built-in"/"default"/"protected" affordance anywhere, and TPDB (AC2)
    // never appears in this panel at all — the absence of both is the
    // criterion, so it is asserted rather than assumed.
    expect(document.body.textContent).not.toMatch(/built-?in|protected/i);
    expect(screen.queryByText("tpdb")).toBeNull();
  });

  it("shows a row's endpoint, masked key and fansite gate in its Edit form", async () => {
    stubFetch(seeded());
    render(() => <StashBoxDatabases />);

    // Rows are title-only (the house RowEditor pattern), so the detail lives
    // in the form — including the masked-key placeholder, which is what
    // tells the operator a key is stored WITHOUT the client ever holding it.
    fireEvent.click(await screen.findByLabelText("Edit fansdb"));

    const endpoint = (await screen.findByLabelText(
      "Endpoint",
    )) as HTMLInputElement;
    expect(endpoint.value).toBe("https://fansdb.cc/graphql");
    const key = screen.getByLabelText("API key") as HTMLInputElement;
    expect(key.value).toBe("");
    expect(key.placeholder).toContain("5678");
    expect(
      (screen.getByLabelText(/fansite-hinted filenames/i) as HTMLInputElement)
        .checked,
    ).toBe(true);
  });

  it("has no fansite-gate field on the Add form (create has no such parameter)", async () => {
    stubFetch(seeded());
    render(() => <StashBoxDatabases />);
    fireEvent.click(
      await screen.findByRole("button", { name: "+ Add database" }),
    );
    expect(screen.queryByLabelText(/fansite-hinted filenames/i)).toBeNull();
  });

  it("persists the new cascade order immediately on drop (AC9)", async () => {
    const calls = stubFetch(seeded());
    render(() => <StashBoxDatabases />);
    await screen.findByLabelText("Drag stashdb");

    dragRow(0, 1);

    await waitFor(() => {
      const reorder = calls.find((c) => c.url.includes("/reorder"));
      expect(reorder).toBeTruthy();
      expect(reorder!.method).toBe("PUT");
      // The FULL id set, in the new order — a partial list is rejected
      // server-side rather than silently leaving a stale priority.
      expect(reorder!.body).toEqual({ ids: [2, 1] });
    });
  });

  it("replaces the Add button with a cap note at 5 databases (AC7)", async () => {
    const full = [1, 2, 3, 4, 5].map((id) =>
      database({ id, name: `db${id}`, priority: id - 1 }),
    );
    stubFetch(full);
    render(() => <StashBoxDatabases />);

    await screen.findByLabelText("Drag db1");
    expect(screen.queryByRole("button", { name: "+ Add database" })).toBeNull();
    expect(screen.getByText(/5 of 5 databases configured/)).toBeTruthy();
  });

  it("offers Add below the cap", async () => {
    stubFetch(seeded());
    render(() => <StashBoxDatabases />);

    expect(
      await screen.findByRole("button", { name: "+ Add database" }),
    ).toBeTruthy();
    expect(screen.queryByText(/5 of 5 databases configured/)).toBeNull();
  });

  it("confirms before deleting, then deletes (AC8)", async () => {
    const calls = stubFetch(seeded());
    render(() => <StashBoxDatabases />);
    await screen.findByLabelText("Drag stashdb");

    fireEvent.click(screen.getAllByRole("button", { name: "Delete" })[0]!);

    await waitFor(() => {
      expect(
        calls.some(
          (c) =>
            c.method === "DELETE" &&
            c.url.endsWith("/api/stashbox-databases/1"),
        ),
      ).toBe(true);
    });
    expect(confirm).toHaveBeenCalled();
  });

  it("does not delete when the confirm is declined", async () => {
    const calls = stubFetch(seeded());
    vi.stubGlobal(
      "confirm",
      vi.fn(() => false),
    );
    render(() => <StashBoxDatabases />);
    await screen.findByLabelText("Drag stashdb");

    fireEvent.click(screen.getAllByRole("button", { name: "Delete" })[0]!);

    await waitFor(() => expect(confirm).toHaveBeenCalled());
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);
  });

  it("tints a row whose stored test failed (AC6/AC12)", async () => {
    stubFetch(seeded(), (url, init) => {
      if ((init?.method ?? "GET") === "POST" && url.includes("/test-stored")) {
        return jsonResponse({ ok: false, error: "connection test failed" });
      }
      return undefined;
    });
    render(() => <StashBoxDatabases />);
    await screen.findByLabelText("Test stashdb");

    // The panel auto-tests every row holding a key as soon as the list
    // resolves, the same convention ConnectionServiceTable follows — so the
    // tint appears without the operator pressing anything.
    await waitFor(() => expect(isTinted("stashdb")).toBe(true));
  });

  it("clears the tint when a retest passes", async () => {
    let ok = false;
    stubFetch(seeded(), (url, init) => {
      if ((init?.method ?? "GET") === "POST" && url.includes("/test-stored")) {
        return jsonResponse(ok ? { ok: true } : { ok: false, error: "x" });
      }
      return undefined;
    });
    render(() => <StashBoxDatabases />);
    await waitFor(() => expect(isTinted("stashdb")).toBe(true));

    ok = true;
    fireEvent.click(screen.getByLabelText("Test stashdb"));

    await waitFor(() => expect(isTinted("stashdb")).toBe(false));
  });

  it("leaves a keyless row untested and untinted", async () => {
    const calls = stubFetch([
      database({ hasApiKey: false, keySuffix: undefined }),
    ]);
    render(() => <StashBoxDatabases />);
    await screen.findByLabelText("Drag stashdb");

    // Nothing to authenticate with, so no test is attempted and the row is
    // not reported as broken — "not configured yet" is not a failure.
    await waitFor(() => expect(calls.length).toBeGreaterThan(0));
    expect(calls.some((c) => c.url.includes("/test-stored"))).toBe(false);
    expect(isTinted("stashdb")).toBe(false);
  });

  it("omits apiKey when an edit never touches the key field (three-state rule)", async () => {
    const calls = stubFetch(seeded());
    render(() => <StashBoxDatabases />);
    await screen.findByLabelText("Edit stashdb");

    fireEvent.click(screen.getByLabelText("Edit stashdb"));
    const endpoint = await screen.findByLabelText("Endpoint");
    fireEvent.input(endpoint, {
      target: { value: "https://mirror.test/graphql" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => {
      const put = calls.find((c) => c.method === "PUT");
      expect(put).toBeTruthy();
      const body = put!.body as Record<string, unknown>;
      expect(body.endpoint).toBe("https://mirror.test/graphql");
      // The criterion: absent, NOT present-and-empty. Sending "" here would
      // wipe a working stored key.
      expect("apiKey" in body).toBe(false);
    });
  });

  it("sends the new key when the key field is edited", async () => {
    const calls = stubFetch(seeded());
    render(() => <StashBoxDatabases />);
    await screen.findByLabelText("Edit stashdb");

    fireEvent.click(screen.getByLabelText("Edit stashdb"));
    const key = await screen.findByLabelText("API key");
    fireEvent.input(key, { target: { value: "replacement-key" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => {
      const put = calls.find((c) => c.method === "PUT");
      expect((put!.body as Record<string, unknown>).apiKey).toBe(
        "replacement-key",
      );
    });
  });

  it("creates a database with the typed name, endpoint and key", async () => {
    const calls = stubFetch(seeded());
    render(() => <StashBoxDatabases />);

    fireEvent.click(
      await screen.findByRole("button", { name: "+ Add database" }),
    );
    fireEvent.input(await screen.findByLabelText("Name"), {
      target: { value: "pmvstash" },
    });
    fireEvent.input(screen.getByLabelText("Endpoint"), {
      target: { value: "https://pmvstash.org/graphql" },
    });
    fireEvent.input(screen.getByLabelText("API key"), {
      target: { value: "pmv-key" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Add database" }));

    await waitFor(() => {
      const post = calls.find((c) => c.method === "POST");
      expect(post!.body).toEqual({
        name: "pmvstash",
        endpoint: "https://pmvstash.org/graphql",
        apiKey: "pmv-key",
      });
    });
  });

  it("toggles a database's enabled flag without touching its key", async () => {
    const calls = stubFetch(seeded());
    render(() => <StashBoxDatabases />);
    await screen.findByLabelText("stashdb enabled");

    fireEvent.click(screen.getByLabelText("stashdb enabled"));

    await waitFor(() => {
      const put = calls.find((c) => c.method === "PUT");
      expect(put!.body).toEqual({ enabled: false });
    });
  });

  it("surfaces a server-side rejection (cap, duplicate or reserved name)", async () => {
    stubFetch(seeded(), (_url, init) => {
      if ((init?.method ?? "GET") === "PUT") {
        return new Response('stashboxdb: "tpdb" is reserved', { status: 400 });
      }
      return undefined;
    });
    render(() => <StashBoxDatabases />);
    await screen.findByLabelText("stashdb enabled");

    fireEvent.click(screen.getByLabelText("stashdb enabled"));

    await waitFor(() => expect(screen.getByText(/reserved/)).toBeTruthy());
  });
});
