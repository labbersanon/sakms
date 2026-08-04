// Downloads tests: the new bulk multi-select (cancel-batch), the single-item
// cancel's file-deletion confirm wording, and the global pause toggle
// (reads/writes pause-state, UI reflects the paused state). Mirrors this repo's
// conventions: a MockEventSource harness (from BrowserNotifications/Dashboard
// tests) feeds the SSE queue frames, a captured-calls fetch stub asserts exact
// request bodies (from Discover.grab.test.tsx), and window.confirm is spied.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import type { Download } from "@dto";
import { Downloads } from "./Downloads";

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

const noContent = (): Response => new Response(null, { status: 204 });

const dl = (over: Partial<Download> = {}): Download => ({
  gid: "g1",
  status: "active",
  filename: "Movie.1080p.mkv",
  totalLength: 1000,
  completedLength: 400,
  downloadSpeed: 100,
  seedCount: 4,
  uploadSpeed: 0,
  protocol: "torrent",
  errorMessage: "",
  ...over,
});

// MockEventSource captures the most recently constructed instance so a test can
// push queue frames at it — the same harness BrowserNotifications.test.tsx uses.
// The Downloads screen's onmessage parses ev.data as a Download[] directly, so a
// frame IS the array.
class MockEventSource {
  static last: MockEventSource | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  url: string;
  closed = false;
  constructor(url: string) {
    this.url = url;
    MockEventSource.last = this;
  }
  close() {
    this.closed = true;
  }
  emit(list: Download[]) {
    this.onmessage?.({ data: JSON.stringify(list) } as MessageEvent);
  }
}

type Call = { url: string; method: string; body: unknown };

const stubFetch = (
  handler: (url: string, init?: RequestInit) => Response,
): Call[] => {
  const calls: Call[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push({
      url,
      method: (init?.method ?? "GET").toUpperCase(),
      body: init?.body ? JSON.parse(init.body as string) : undefined,
    });
    return handler(url, init);
  });
  vi.stubGlobal("fetch", fn);
  return calls;
};

beforeEach(() => {
  MockEventSource.last = null;
  vi.stubGlobal("EventSource", MockEventSource);
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("Downloads — single cancel (confirm reflects file deletion)", () => {
  it("cancels via DELETE only after a confirm that mentions deleting files", async () => {
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
    const calls = stubFetch((url) => {
      if (url.includes("/api/downloads/pause-state"))
        return jsonResponse({ paused: false });
      if (url.includes("/api/downloads/g1")) return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Downloads />);
    MockEventSource.last!.emit([dl({ gid: "g1" })]);

    fireEvent.click(await screen.findByText("Cancel"));

    expect(confirmSpy).toHaveBeenCalledWith(
      expect.stringContaining("delete its downloaded files"),
    );
    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.method === "DELETE" && c.url.includes("/api/downloads/g1"),
        ),
      ).toBe(true),
    );
  });

  it("does NOT cancel when the confirm is declined", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(false);
    const calls = stubFetch((url) => {
      if (url.includes("/api/downloads/pause-state"))
        return jsonResponse({ paused: false });
      if (url.includes("/api/downloads/g1")) return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Downloads />);
    MockEventSource.last!.emit([dl({ gid: "g1" })]);
    fireEvent.click(await screen.findByText("Cancel"));

    expect(calls.some((c) => c.method === "DELETE")).toBe(false);
  });
});

describe("Downloads — bulk cancel (cancel-batch)", () => {
  it("cancels every selected gid in one cancel-batch call, after a confirm", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const calls = stubFetch((url) => {
      if (url.includes("/api/downloads/pause-state"))
        return jsonResponse({ paused: false });
      if (url.includes("/api/downloads/cancel-batch"))
        return jsonResponse({
          results: [
            { gid: "g1", ok: true },
            { gid: "g2", ok: true },
          ],
        });
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Downloads />);
    MockEventSource.last!.emit([
      dl({ gid: "g1", filename: "One.mkv" }),
      dl({ gid: "g2", filename: "Two.mkv" }),
    ]);

    fireEvent.click(await screen.findByLabelText("Select One.mkv"));
    fireEvent.click(screen.getByLabelText("Select Two.mkv"));
    fireEvent.click(screen.getByRole("button", { name: /Cancel Selected \(2\)/ }));

    expect(window.confirm).toHaveBeenCalledWith(
      expect.stringContaining("delete their files"),
    );
    await waitFor(() =>
      expect(
        calls.some((c) => c.url.includes("/api/downloads/cancel-batch")),
      ).toBe(true),
    );
    const batch = calls.find((c) =>
      c.url.includes("/api/downloads/cancel-batch"),
    )!;
    expect(batch.method).toBe("POST");
    expect(batch.body).toEqual({ gids: ["g1", "g2"] });
  });
});

describe("Downloads — protocol-scoped metrics", () => {
  // Every test here only reads pause-state; none pauses/resumes/cancels.
  const stubPauseStateOnly = (): void => {
    stubFetch((url) => {
      if (url.includes("/api/downloads/pause-state"))
        return jsonResponse({ paused: false });
      throw new Error("unexpected fetch: " + url);
    });
  };

  it("V-1: shows upload speed at exactly 0 as '0 KB/s', not '—'", async () => {
    stubPauseStateOnly();
    render(() => <Downloads />);
    MockEventSource.last!.emit([dl({ uploadSpeed: 0 })]);

    const upload = await screen.findByLabelText("Upload speed");
    expect(upload.textContent).toContain("0 KB/s");
    expect(upload.textContent).not.toContain("—");
  });

  it("V-2: shows a non-zero upload speed", async () => {
    stubPauseStateOnly();
    render(() => <Downloads />);
    MockEventSource.last!.emit([dl({ uploadSpeed: 240 * 1024 })]);

    const upload = await screen.findByLabelText("Upload speed");
    expect(upload.textContent).toContain("240 KB/s");
  });

  it("V-3: usenet row hides both upload speed and seed count entirely (absent, not zero)", async () => {
    stubPauseStateOnly();
    render(() => <Downloads />);
    MockEventSource.last!.emit([
      dl({ protocol: "usenet", seedCount: 0, uploadSpeed: 0 }),
    ]);

    const row = await screen.findByRole("listitem");
    expect(screen.queryByLabelText("Upload speed")).toBeNull();
    expect(screen.queryByLabelText("Connected seeders")).toBeNull();
    expect(row.textContent).not.toContain("0 KB/s");
    expect(row.textContent).not.toContain("0 seeds");
    expect(row.textContent).not.toContain("N/A");
  });

  it("V-4: shows seed count on a torrent row", async () => {
    stubPauseStateOnly();
    render(() => <Downloads />);
    MockEventSource.last!.emit([dl({ seedCount: 4 })]);

    const seeds = await screen.findByLabelText("Connected seeders");
    expect(seeds.textContent).toContain("4 seeds");
  });

  it("V-5: singular label at exactly 1 seed", async () => {
    stubPauseStateOnly();
    render(() => <Downloads />);
    MockEventSource.last!.emit([dl({ seedCount: 1 })]);

    const seeds = await screen.findByLabelText("Connected seeders");
    expect(seeds.textContent).toContain("1 seed");
    expect(seeds.textContent).not.toContain("1 seeds");
  });

  it("V-6: a seeding (non-active) torrent row shows upload speed and seeds but not download speed", async () => {
    stubPauseStateOnly();
    render(() => <Downloads />);
    MockEventSource.last!.emit([
      dl({
        status: "complete",
        completedLength: 1000,
        totalLength: 1000,
        seedCount: 11,
        uploadSpeed: 240 * 1024,
      }),
    ]);

    const upload = await screen.findByLabelText("Upload speed");
    expect(upload.textContent).toContain("240 KB/s");
    expect(screen.getByLabelText("Connected seeders").textContent).toContain("11 seeds");
    expect(screen.queryByLabelText("Download speed")).toBeNull();
  });

  it("V-7: filename, status badge, progress bytes, and progress bar are unchanged by protocol scoping", async () => {
    stubPauseStateOnly();
    render(() => <Downloads />);
    MockEventSource.last!.emit([
      dl({
        filename: "Movie.1080p.mkv",
        status: "active",
        completedLength: 400 * 1024,
        totalLength: 1000 * 1024,
      }),
    ]);

    expect(await screen.findByText("Movie.1080p.mkv")).toBeInTheDocument();
    expect(screen.getByText("active")).toBeInTheDocument();
    expect(screen.getByText("400 KB / 1000 KB")).toBeInTheDocument();
    const bar = document.querySelector<HTMLElement>(".bg-accent");
    expect(bar?.style.width).toBe("40%");
  });

  it("V-8: an active usenet row keeps its download speed", async () => {
    stubPauseStateOnly();
    render(() => <Downloads />);
    MockEventSource.last!.emit([
      dl({
        protocol: "usenet",
        status: "active",
        downloadSpeed: 4.2 * 1024 * 1024,
      }),
    ]);

    const download = await screen.findByLabelText("Download speed");
    expect(download.textContent).toContain("4.2 MB/s");
  });
});

describe("Downloads — global pause toggle", () => {
  it("reads pause-state on mount and writes the flipped value, reflecting it in the UI", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/downloads/pause-state")) {
        // Deterministic read→write: GET (mount) resolves to not-paused, and the
        // PUT (toggle) echoes its own body — the real backend's behavior. This
        // avoids a race where a mount-GET of {paused:true} would make the toggle
        // PUT false instead.
        if ((init?.method ?? "GET").toUpperCase() === "PUT")
          return jsonResponse(JSON.parse(init!.body as string));
        return jsonResponse({ paused: false });
      }
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Downloads />);
    // Not-paused once the mount GET resolves.
    const toggle = await screen.findByRole("button", {
      name: "Pause all downloads",
    });
    fireEvent.click(toggle);

    // Banner + relabelled button appear once the PUT resolves to paused:true.
    expect(await screen.findByText(/globally paused/)).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Resume all downloads" }),
    ).toBeInTheDocument();

    const put = calls.find(
      (c) => c.method === "PUT" && c.url.includes("/api/downloads/pause-state"),
    )!;
    expect(put.body).toEqual({ paused: true });
    // The mount GET fired too.
    expect(
      calls.some(
        (c) =>
          c.method === "GET" && c.url.includes("/api/downloads/pause-state"),
      ),
    ).toBe(true);
  });
});
