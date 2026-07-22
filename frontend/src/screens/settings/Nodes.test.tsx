// Nodes.tsx tests. Path mappings are node-owned now (D3/D1 — the
// operator-auth settings write ignores any submitted PathMap, only the node
// itself can author them via its own bearer-authed push). These tests assert
// the resulting read-only behavior: (1) EditSettingsModal renders the node's
// self-reported mappings as plain text, never an editable control, (2) a row
// whose library path isn't configured yet still renders with its
// configure-first note, (3) a blank NodePath (never set, or explicitly
// cleared by the node — D7) renders as "not set", (4) saving maxJobs sends an
// empty pathMap rather than any operator-edited value, and (5) ApproveModal
// no longer collects or displays any path-mapping state at all.
//
// Also covers the node-pause-dispatch plan's Stage 4/5: the pause switch —
// now a NodeRow list-row control (relocated out of EditSettingsModal, Stage
// 5) — fires updateNodePause immediately (not gated by "Save settings" or
// requiring the settings modal to be open at all), rolls back on failure,
// and — most importantly — the bidirectional separation between the pause
// toggle and the maxJobs save: a MaxJobs save never calls the pause endpoint
// or sends `paused`, and a pause toggle never calls the settings endpoint or
// sends `maxJobs`. The former "Paused" text badge was removed as redundant
// once the switch itself sits in the row (see NodeRow's doc comment); there
// is no longer a test for it.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { NodesSection } from "./Nodes";

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
const noContent = (): Response => new Response(null, { status: 204 });

type Call = { url: string; method: string; body: unknown };

const FIVE_ENTRIES = [
  {
    key: "movies_library_root_folder",
    serverPath: "/data/movies",
    nodePath: "/mnt/movies",
    configured: true,
  },
  {
    key: "series_library_root_folder",
    serverPath: "/data/series",
    // Blank NodePath on a configured row — either never set by the node, or
    // explicitly cleared (D7). Either way it must render as "not set", not a
    // stale cached value.
    nodePath: "",
    configured: true,
  },
  { key: "adult_library_root_folder", serverPath: "", nodePath: "", configured: false },
  { key: "movies_kids_root_path", serverPath: "", nodePath: "", configured: false },
  { key: "series_kids_root_path", serverPath: "", nodePath: "", configured: false },
];

const stubFetch = (
  override?: (url: string, init?: RequestInit) => Response | undefined,
) => {
  const calls: Call[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = (init?.method ?? "GET").toUpperCase();
    calls.push({
      url,
      method,
      body: init?.body ? JSON.parse(init.body as string) : undefined,
    });
    const r = override?.(url, init);
    if (r) return r;
    if (url.includes("/path-mappings")) {
      return jsonResponse({ entries: FIVE_ENTRIES });
    }
    if (url.includes("/api/nodes") && method === "GET") {
      return jsonResponse({
        nodes: [
          {
            id: "node-a",
            name: "render-box",
            status: "online",
            capabilities: [],
            lastHeartbeat: new Date().toISOString(),
            // Non-zero on purpose: a known, previously-saved concurrency cap
            // that EditSettingsModal must preload rather than default to 0.
            maxJobs: 4,
            pauseDispatch: false,
          },
        ],
        pending: [
          {
            id: "pending-1",
            name: "new-box",
            pairingCode: "ABC123",
            requestedAt: new Date().toISOString(),
          },
        ],
      });
    }
    return noContent();
  });
  vi.stubGlobal("fetch", fn);
  return calls;
};

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("EditSettingsModal", () => {
  it("renders the node-reported path mappings as read-only text, not an input", async () => {
    stubFetch();
    render(() => <NodesSection />);

    fireEvent.click(await screen.findByRole("button", { name: "Settings" }));

    await screen.findByText("/mnt/movies");
    // No editable control for path-mapping fields remains reachable — the
    // only input left in the modal is the maxJobs number field (the pause
    // toggle moved out to the list row as a switch, Stage 5).
    expect(screen.queryByPlaceholderText("/mnt/media")).toBeNull();
    expect(document.querySelectorAll("input")).toHaveLength(1);
  });

  it("renders an unconfigured library path's row with a configure-first note", async () => {
    stubFetch();
    render(() => <NodesSection />);

    fireEvent.click(await screen.findByRole("button", { name: "Settings" }));

    await waitFor(async () => {
      expect(
        (await screen.findAllByText("configure this in Library settings first"))
          .length,
      ).toBe(3);
    });
  });

  it("renders a blank NodePath — never set or node-cleared — as 'not set'", async () => {
    stubFetch();
    render(() => <NodesSection />);

    fireEvent.click(await screen.findByRole("button", { name: "Settings" }));

    // series_library_root_folder is configured but its NodePath is blank
    // (either never set, or cleared by the node) — plus the 3 unconfigured
    // rows, whose NodePath is also blank. All 4 must read "not set".
    await waitFor(async () => {
      expect((await screen.findAllByText("not set")).length).toBe(4);
    });
  });

  it("saving maxJobs sends an empty pathMap, never an operator-edited value", async () => {
    const calls = stubFetch();
    render(() => <NodesSection />);

    fireEvent.click(await screen.findByRole("button", { name: "Settings" }));
    await screen.findByText("/mnt/movies");

    fireEvent.click(screen.getByRole("button", { name: "Save settings" }));

    await waitFor(() => {
      const save = calls.find((c) => c.url.endsWith("/api/nodes/node-a/settings"));
      expect(save).toBeDefined();
      const body = save!.body as { pathMap: unknown[]; maxJobs: number };
      expect(body.pathMap).toEqual([]);
      expect(typeof body.maxJobs).toBe("number");
    });
  });

  // Regression test: node-a's stubbed GET /api/nodes response reports a
  // known non-zero maxJobs (4, see FIVE_ENTRIES' sibling node stub above).
  // Before the fix, EditSettingsModal's maxJobs signal always started at a
  // hardcoded 0, so opening the modal (e.g. only to look at path mappings)
  // and saving without touching the field would silently reset the node's
  // stored concurrency cap to 0 — updateNodeSettingsOperatorAuth applies
  // whatever maxJobs is submitted unconditionally. This asserts the field is
  // preloaded with the real stored value AND that an untouched save
  // preserves it rather than sending 0.
  it("preloads the stored maxJobs value and preserves it on an untouched save", async () => {
    const calls = stubFetch();
    render(() => <NodesSection />);

    fireEvent.click(await screen.findByRole("button", { name: "Settings" }));
    await screen.findByText("/mnt/movies");

    const input = screen.getByRole("spinbutton") as HTMLInputElement;
    expect(input.value).toBe("4");

    fireEvent.click(screen.getByRole("button", { name: "Save settings" }));

    await waitFor(() => {
      const save = calls.find((c) => c.url.endsWith("/api/nodes/node-a/settings"));
      expect(save).toBeDefined();
      const body = save!.body as { pathMap: unknown[]; maxJobs: number };
      expect(body.maxJobs).toBe(4);
    });
  });
});

describe("NodeRow — pause dispatch switch", () => {
  it("preloads the switch from props.node.pauseDispatch, with no settings modal open", async () => {
    stubFetch((url) => {
      if (url.includes("/api/nodes") && !url.includes("/path-mappings")) {
        return jsonResponse({
          nodes: [
            {
              id: "node-a",
              name: "render-box",
              status: "online",
              capabilities: [],
              lastHeartbeat: new Date().toISOString(),
              maxJobs: 4,
              pauseDispatch: true,
            },
          ],
          pending: [],
        });
      }
      return undefined;
    });
    render(() => <NodesSection />);

    // No click on "Settings" here — the switch is reachable straight off the
    // list row now, without opening EditSettingsModal at all.
    const toggle = await screen.findByLabelText("render-box dispatch enabled");
    // pauseDispatch: true means the node IS paused, so the (inverted) switch
    // renders unchecked — checked means dispatch enabled/running.
    expect(toggle).toHaveAttribute("aria-checked", "false");
  });

  it("toggling the switch calls updateNodePause immediately, not gated by Save settings", async () => {
    const calls = stubFetch();
    render(() => <NodesSection />);

    const toggle = await screen.findByLabelText("render-box dispatch enabled");
    // pauseDispatch: false (default stub) means dispatch is enabled, so the
    // inverted switch renders checked.
    expect(toggle).toHaveAttribute("aria-checked", "true");

    fireEvent.click(toggle);

    await waitFor(() => {
      const pause = calls.find((c) => c.url.endsWith("/api/nodes/node-a/pause"));
      expect(pause).toBeDefined();
      expect(pause!.method).toBe("PUT");
    });
    // Never fired via "Save settings" — no click on that button, or even on
    // "Settings" to open the modal, happened here.
    expect(
      calls.some((c) => c.url.endsWith("/api/nodes/node-a/settings")),
    ).toBe(false);
  });

  it("rolls back the switch when the pause PUT fails", async () => {
    const calls = stubFetch((url, init) => {
      if (
        url.endsWith("/api/nodes/node-a/pause") &&
        (init?.method ?? "GET").toUpperCase() === "PUT"
      ) {
        return new Response("boom", { status: 500 });
      }
      return undefined;
    });
    render(() => <NodesSection />);

    const toggle = await screen.findByLabelText("render-box dispatch enabled");
    // pauseDispatch: false (default stub) means dispatch is enabled, so the
    // inverted switch renders checked; the rollback restores this same
    // checked state after the failed PUT.
    expect(toggle).toHaveAttribute("aria-checked", "true");

    fireEvent.click(toggle);

    await waitFor(() => {
      expect(
        calls.some((c) => c.url.endsWith("/api/nodes/node-a/pause")),
      ).toBe(true);
    });
    await waitFor(() =>
      expect(toggle).toHaveAttribute("aria-checked", "true"),
    );
  });
});

// Bidirectional separation between the pause toggle and the MaxJobs save —
// this is the most important test in this stage (mirrors the sibling
// node-path-config-ui feature's PathMap/MaxJobs separation tests, and proves
// the client-side half of P2's structural anti-footgun property).
describe("Pause/MaxJobs request separation", () => {
  it("direction 1: saving MaxJobs never calls the pause endpoint and never sends `paused`", async () => {
    const calls = stubFetch();
    render(() => <NodesSection />);

    fireEvent.click(await screen.findByRole("button", { name: "Settings" }));
    await screen.findByText("/mnt/movies");

    fireEvent.click(screen.getByRole("button", { name: "Save settings" }));

    await waitFor(() => {
      const save = calls.find((c) => c.url.endsWith("/api/nodes/node-a/settings"));
      expect(save).toBeDefined();
      expect(save!.body).not.toHaveProperty("paused");
    });
    expect(
      calls.some((c) => c.url.endsWith("/api/nodes/node-a/pause")),
    ).toBe(false);
  });

  it("direction 2: toggling pause never calls the settings endpoint and never sends `maxJobs`", async () => {
    const calls = stubFetch();
    render(() => <NodesSection />);

    const toggle = await screen.findByLabelText("render-box dispatch enabled");

    fireEvent.click(toggle);

    await waitFor(() => {
      const pause = calls.find((c) => c.url.endsWith("/api/nodes/node-a/pause"));
      expect(pause).toBeDefined();
      expect(pause!.body).toEqual({ paused: true });
      expect(pause!.body).not.toHaveProperty("maxJobs");
    });
    expect(
      calls.some((c) => c.url.endsWith("/api/nodes/node-a/settings")),
    ).toBe(false);
  });
});

describe("NodeRow — pause switch state (redundant 'Paused' badge removed)", () => {
  it("reflects each node's own pauseDispatch independently via the switch, with no separate text badge", async () => {
    stubFetch((url) => {
      if (url.includes("/api/nodes") && !url.includes("/path-mappings")) {
        return jsonResponse({
          nodes: [
            {
              id: "node-a",
              name: "paused-box",
              status: "online",
              capabilities: [],
              lastHeartbeat: new Date().toISOString(),
              maxJobs: 0,
              pauseDispatch: true,
            },
            {
              id: "node-b",
              name: "running-box",
              status: "online",
              capabilities: [],
              lastHeartbeat: new Date().toISOString(),
              maxJobs: 0,
              pauseDispatch: false,
            },
          ],
          pending: [],
        });
      }
      return undefined;
    });
    render(() => <NodesSection />);

    const pausedToggle = await screen.findByLabelText(
      "paused-box dispatch enabled",
    );
    const runningToggle = await screen.findByLabelText(
      "running-box dispatch enabled",
    );
    // Inverted: paused-box (pauseDispatch: true) renders unchecked, and
    // running-box (pauseDispatch: false) renders checked.
    expect(pausedToggle).toHaveAttribute("aria-checked", "false");
    expect(runningToggle).toHaveAttribute("aria-checked", "true");

    // The old text badge is gone — the switch is the only paused/running
    // indicator in the row now (see NodeRow's doc comment for why keeping
    // both would be redundant).
    expect(screen.queryByText("Paused")).toBeNull();
  });
});

describe("ApproveModal", () => {
  it("collects only maxJobs — no path-mapping section, no read of path-mappings", async () => {
    const calls = stubFetch();
    render(() => <NodesSection />);

    fireEvent.click(await screen.findByRole("button", { name: "Approve" }));
    await screen.findByText(
      "Once approved, the node configures its own path mappings — nothing to set here.",
    );

    expect(screen.queryByText("Path mappings (library → node)")).toBeNull();
    expect(screen.queryByPlaceholderText("/mnt/media")).toBeNull();

    // Two "Approve" buttons exist once the modal is open: the PendingRow's
    // trigger (still rendered underneath the overlay) and the modal's own
    // submit button, which the component tree renders first.
    const approveButtons = screen.getAllByRole("button", { name: "Approve" });
    fireEvent.click(approveButtons[0]!);

    await waitFor(() => {
      const approve = calls.find((c) => c.url.endsWith("/api/nodes/pending-1/approve"));
      expect(approve).toBeDefined();
      const body = approve!.body as { pathMap: unknown[]; maxJobs: number };
      expect(body.pathMap).toEqual([]);
    });

    // ApproveModal never fetches path-mappings for the pending node anymore.
    expect(calls.some((c) => c.url.includes("/path-mappings"))).toBe(false);
  });
});
