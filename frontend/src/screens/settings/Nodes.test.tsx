// Nodes.tsx tests. The two things this feature review process kept surfacing
// as real bugs (see the plan's own Revision History) are exactly what these
// assert: (1) EditSettingsModal must load REAL persisted values, not always
// start blank, and (2) a row whose library path isn't configured yet must
// render disabled with a note, not a crash or a silently-droppable row. Also
// covers ApproveModal's "plain text, no live browse" rule and the save
// payload's keyed (not server/local) shape.

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
  it("prefills real persisted node path values instead of starting blank", async () => {
    stubFetch();
    render(() => <NodesSection />);

    fireEvent.click(await screen.findByRole("button", { name: "Settings" }));

    const moviesInput = (
      await screen.findAllByPlaceholderText("/mnt/media")
    )[0] as HTMLInputElement;
    await waitFor(() => expect(moviesInput.value).toBe("/mnt/movies"));
  });

  it("renders an unconfigured library path's row disabled with a configure-first note", async () => {
    stubFetch();
    render(() => <NodesSection />);

    fireEvent.click(await screen.findByRole("button", { name: "Settings" }));

    await waitFor(async () => {
      expect(
        (await screen.findAllByText("configure this in Library settings first"))
          .length,
      ).toBe(3);
    });
    const inputs = (await screen.findAllByPlaceholderText(
      "/mnt/media",
    )) as HTMLInputElement[];
    // 5 fixed rows: movies (configured), series (configured), adult/movies-kids/series-kids (not).
    expect(inputs).toHaveLength(5);
    const disabledCount = inputs.filter((i) => i.disabled).length;
    expect(disabledCount).toBe(3);
  });

  it("saves with the keyed pathMap shape (key/nodePath), not server/local", async () => {
    const calls = stubFetch();
    render(() => <NodesSection />);

    fireEvent.click(await screen.findByRole("button", { name: "Settings" }));
    const seriesInput = (await screen.findAllByPlaceholderText(
      "/mnt/media",
    ))[1] as HTMLInputElement;
    fireEvent.input(seriesInput, { target: { value: "/mnt/series-v2" } });

    fireEvent.click(screen.getByRole("button", { name: "Save settings" }));

    await waitFor(() => {
      const save = calls.find((c) => c.url.endsWith("/api/nodes/node-a/settings"));
      expect(save).toBeDefined();
      const body = save!.body as { pathMap: { key: string; nodePath: string }[] };
      const series = body.pathMap.find(
        (p) => p.key === "series_library_root_folder",
      );
      expect(series?.nodePath).toBe("/mnt/series-v2");
      expect(series).not.toHaveProperty("server");
      expect(series).not.toHaveProperty("local");
    });
  });
});

describe("ApproveModal", () => {
  it("uses a plain text input, not a live browse picker", async () => {
    const calls = stubFetch();
    render(() => <NodesSection />);

    fireEvent.click(await screen.findByRole("button", { name: "Approve" }));
    await screen.findByText(
      "Live directory browsing is available after approval — type the node-side path for now.",
    );

    const moviesInput = (await screen.findAllByPlaceholderText(
      "/mnt/media",
    ))[0] as HTMLInputElement;
    fireEvent.focus(moviesInput);
    fireEvent.input(moviesInput, { target: { value: "/mnt/movies" } });

    // A live picker would fire GET .../browse on focus/input; a plain text
    // input never does.
    await new Promise((r) => setTimeout(r, 50));
    expect(calls.some((c) => c.url.includes("/browse"))).toBe(false);
  });
});
