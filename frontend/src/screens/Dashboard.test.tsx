// Dashboard view test — the screen opens one EventSource, shows a loading
// placeholder until the first snapshot arrives, renders the snapshot values,
// and surfaces a reconnecting notice on an EventSource error. EventSource is
// mocked globally (same spirit as other screens' fetch stubs) with a
// controllable instance the test can drive.
//
// Every case mounts through renderDashboard(), a <Router> harness: the Storage
// Allocation cells link with <A>, which reads router context and throws when
// mounted bare. fetch is stubbed globally too — that section is the screen's
// one REST call, and an unstubbed fetch would reject in every case, not just
// the ones asserting on it.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@solidjs/testing-library";
import { Route, Router } from "@solidjs/router";
import type { StorageAllocation, SysinfoSnapshot } from "@dto";
import { Dashboard } from "./Dashboard";

// MockEventSource is a minimal, controllable EventSource stand-in. The most
// recently constructed instance is captured so a test can fire events at it.
class MockEventSource {
  static last: MockEventSource | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  // named-event handlers registered via addEventListener (e.g. "sampleError").
  listeners: Record<string, ((ev: Event) => void)[]> = {};
  url: string;
  closed = false;

  constructor(url: string) {
    this.url = url;
    MockEventSource.last = this;
  }

  addEventListener(type: string, handler: (ev: Event) => void) {
    (this.listeners[type] ??= []).push(handler);
  }

  close() {
    this.closed = true;
  }

  // emit fires a data message the way the real SSE `onmessage` path does.
  emit(data: unknown) {
    this.onmessage?.({ data: JSON.stringify(data) } as MessageEvent);
  }

  // emitEvent fires a named SSE event (e.g. "sampleError") with a string
  // payload, the way EventSource delivers a `event:`/`data:` frame.
  emitEvent(type: string, data: string) {
    for (const h of this.listeners[type] ?? []) {
      h({ data } as MessageEvent);
    }
  }

  fail() {
    this.onerror?.(new Event("error"));
  }
}

const snapshot = (over: Partial<SysinfoSnapshot> = {}): SysinfoSnapshot => ({
  cpuPercent: 42.5,
  memUsedBytes: 2 * 1024 * 1024 * 1024, // 2 GB
  memLimitBytes: 8 * 1024 * 1024 * 1024, // 8 GB
  netRxBps: 2 * 1024 * 1024, // 2 MB/s
  netTxBps: 500 * 1024, // 500 KB/s
  containerDiskReadBps: 1024, // 1 KB/s
  containerDiskWriteBps: 0,
  serverDisks: [{ name: "sda", readBps: 3 * 1024 * 1024, writeBps: 0 }],
  storageMounts: [
    {
      name: "App data",
      totalBytes: 10737418240, // 10 GB
      availBytes: 5368709120, // 5 GB avail → 5 GB used
      configured: true,
    },
  ],
  gpus: [],
  ...over,
});

// TB-scale fixture values, deliberately clear of the snapshot() factory's GB
// figures so a size assertion can never match the SSE sections by accident.
const TB = 1024 ** 4;

const cell = (tier: string, totalBytes: number, itemCount: number) => ({
  tier,
  totalBytes,
  itemCount,
});

const allocation = (): StorageAllocation => ({
  tiers: ["low", "medium", "high", "lossless", "unknown"],
  rows: [
    {
      mode: "movies",
      cells: [
        cell("low", 0, 0),
        cell("medium", 2 * TB, 40),
        cell("high", 0, 0),
        cell("lossless", 5 * TB, 12),
        cell("unknown", 0, 0),
      ],
      rowTotalBytes: 7 * TB,
      rowItemCount: 52,
    },
    {
      mode: "series",
      cells: [
        cell("low", 0, 0),
        cell("medium", 0, 0),
        cell("high", 3 * TB, 8),
        cell("lossless", 0, 0),
        cell("unknown", 1 * TB, 2),
      ],
      rowTotalBytes: 4 * TB,
      rowItemCount: 10,
    },
    {
      mode: "adult",
      cells: [
        cell("low", 6 * TB, 100),
        cell("medium", 0, 0),
        cell("high", 0, 0),
        cell("lossless", 0, 0),
        cell("unknown", 0, 0),
      ],
      rowTotalBytes: 6 * TB,
      rowItemCount: 100,
    },
  ],
  grandTotalBytes: 17 * TB,
});

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

// renderDashboard mounts the screen inside a <Router>: the Storage Allocation
// cells use <A>, which throws outside router context.
function renderDashboard() {
  return render(() => (
    <Router>
      <Route path="/" component={Dashboard} />
    </Router>
  ));
}

beforeEach(() => {
  MockEventSource.last = null;
  vi.stubGlobal("EventSource", MockEventSource);
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => jsonResponse(allocation())),
  );
});

afterEach(() => vi.unstubAllGlobals());

describe("Dashboard view", () => {
  it("shows a loading placeholder until the first event arrives", () => {
    renderDashboard();
    expect(screen.getByText(/Waiting for the first live reading/i)).toBeInTheDocument();
  });

  it("renders snapshot values once a message arrives", async () => {
    renderDashboard();
    MockEventSource.last!.emit(snapshot());

    // CPU percentage rendered.
    expect(await screen.findByText("42.5%")).toBeInTheDocument();
    // RAM used / limit in GB.
    expect(screen.getByText(/2\.0 GB used \/ 8\.0 GB limit/)).toBeInTheDocument();
    // Network rates.
    expect(screen.getByText(/↓ 2\.0 MB\/s/)).toBeInTheDocument();
    expect(screen.getByText(/↑ 500 KB\/s/)).toBeInTheDocument();
    // Server disk row.
    expect(screen.getByText("sda")).toBeInTheDocument();
    // App data storage mount: 5 GB used of 10 GB.
    expect(screen.getByText("App data")).toBeInTheDocument();
    expect(screen.getByText(/5\.0 GB used of 10\.0 GB/)).toBeInTheDocument();
  });

  it("renders the storage-mount donut at the correct used proportion", async () => {
    const { container } = renderDashboard();
    // 5 GB used of 10 GB → the used slice fills 50% of the ring.
    MockEventSource.last!.emit(snapshot());
    await screen.findByText(/5\.0 GB used of 10\.0 GB/);

    const donut = container.querySelector<SVGSVGElement>(
      "[data-used-percent]",
    );
    expect(donut).not.toBeNull();
    expect(donut!.getAttribute("data-used-percent")).toBe("50");
    // Both slices actually draw (used + free arcs), guarding the arc geometry
    // against a regression that data-used-percent alone (input-only) can't see.
    const arcs = donut!.querySelectorAll("path");
    expect(arcs.length).toBe(2);
    for (const p of arcs) expect(p.getAttribute("d")).toBeTruthy();
    // The center label directly labels the used slice (identity is never
    // color-alone), and a native <title> carries the exact used/free readout.
    expect(donut!.querySelector("title")?.textContent).toMatch(
      /5\.0 GB used · 5\.0 GB free/,
    );
  });

  it("renders a 79%-used storage-mount donut for an uneven snapshot", async () => {
    const { container } = renderDashboard();
    MockEventSource.last!.emit(
      snapshot({
        storageMounts: [
          {
            name: "Media",
            totalBytes: 10737418240, // 10 GB
            availBytes: 2254857831, // ~2.1 GB avail → ~79% used
            configured: true,
          },
        ],
      }),
    );
    await screen.findByText("Media");
    const donut = container.querySelector<SVGSVGElement>(
      "[data-used-percent]",
    );
    expect(donut!.getAttribute("data-used-percent")).toBe("79");
  });

  // A full disk (0 bytes available) is the real-world case most likely to
  // expose an SVG arc-degeneracy regression (the classic "a 360°+ sweep
  // renders as nothing" bug) — pin it explicitly rather than relying on the
  // 50%/79% cases alone to cover the edge.
  it("renders a 100%-used storage-mount donut without a degenerate arc", async () => {
    const { container } = renderDashboard();
    MockEventSource.last!.emit(
      snapshot({
        storageMounts: [
          {
            name: "Full Disk",
            totalBytes: 10737418240, // 10 GB
            availBytes: 0, // 0 avail → 100% used
            configured: true,
          },
        ],
      }),
    );
    await screen.findByText("Full Disk");
    const donut = container.querySelector<SVGSVGElement>(
      "[data-used-percent]",
    );
    expect(donut).not.toBeNull();
    expect(donut!.getAttribute("data-used-percent")).toBe("100");
    // The used slice draws a near-full ring; the free slice is empty (its
    // sweep is non-positive once the gap is subtracted) — both must be
    // present as <path> elements, but only one carries a real "d".
    const arcs = donut!.querySelectorAll("path");
    expect(arcs.length).toBe(2);
    expect(arcs[1]!.getAttribute("d")).toBeTruthy(); // used
    expect(arcs[0]!.getAttribute("d")).toBeFalsy(); // free
  });

  it("shows a reconnecting notice on an EventSource error", async () => {
    renderDashboard();
    MockEventSource.last!.fail();
    expect(await screen.findByText(/reconnecting/i)).toBeInTheDocument();
  });

  it("shows a metric-read-failed banner on a sampleError event", async () => {
    renderDashboard();
    MockEventSource.last!.emitEvent("sampleError", "cpu.stat unreadable");
    expect(
      await screen.findByText(/Metric read failed: cpu\.stat unreadable/),
    ).toBeInTheDocument();
  });

  it("renders 'unlimited' when the memory limit is -1", async () => {
    renderDashboard();
    MockEventSource.last!.emit(snapshot({ memLimitBytes: -1 }));
    expect(await screen.findByText(/used \/ unlimited/)).toBeInTheDocument();
  });

  it("renders a GPU card with a utilization gauge", async () => {
    renderDashboard();
    MockEventSource.last!.emit(
      snapshot({
        gpus: [
          {
            name: "RTX 4070",
            utilPercent: 65,
            vramUsedBytes: 4 * 1024 * 1024 * 1024,
            vramTotalBytes: 12 * 1024 * 1024 * 1024,
            powerMicrowatts: 150_000_000,
          },
        ],
      }),
    );
    // The GPU card's title and its gauge's center percentage both render.
    expect(await screen.findByText("RTX 4070")).toBeInTheDocument();
    expect(screen.getByText("65%")).toBeInTheDocument();
  });

  it("shows 'Utilization unavailable' when a GPU reports utilPercent -1", async () => {
    renderDashboard();
    MockEventSource.last!.emit(
      snapshot({
        gpus: [
          {
            name: "Radeon RX 7900",
            utilPercent: -1,
            vramUsedBytes: 0,
            vramTotalBytes: 0,
            powerMicrowatts: 0,
          },
        ],
      }),
    );
    expect(
      await screen.findByText(/Utilization unavailable/),
    ).toBeInTheDocument();
  });

  it("renders the storage allocation table", async () => {
    const { container } = renderDashboard();
    await screen.findByText("2.0 TB");

    // The tier axis comes from the response, plus the mode and total columns.
    for (const h of ["Mode", "Low", "Medium", "High", "Lossless", "Unknown", "Total"]) {
      expect(screen.getByText(h)).toBeInTheDocument();
    }

    // Three mode rows, each a full 5-tier axis plus its row total.
    const rows = container.querySelectorAll("tbody tr");
    expect(rows.length).toBe(3);
    for (const row of rows) expect(row.querySelectorAll("td").length).toBe(6);
    expect(screen.getByText("Movies")).toBeInTheDocument();
    expect(screen.getByText("Series")).toBeInTheDocument();
    expect(screen.getByText("Adult")).toBeInTheDocument();

    // Sizes are on the full unit ladder (formatGB would read "5120.0 GB"),
    // with the item count beneath each.
    expect(screen.getByText("5.0 TB")).toBeInTheDocument();
    expect(screen.getByText("12 items")).toBeInTheDocument();
    expect(screen.getByText("3.0 TB")).toBeInTheDocument();
    expect(screen.getByText("8 items")).toBeInTheDocument();
    // Row totals render too.
    expect(screen.getByText("7.0 TB")).toBeInTheDocument();
    expect(screen.getByText("52 items")).toBeInTheDocument();
  });

  // Placement proof: the section lives OUTSIDE <Show when={snap()}>, so it must
  // paint on its own data with no SSE frame ever emitted. Nested inside that
  // gate, this test would see nothing but the loading placeholder.
  it("renders the storage allocation table without an SSE frame", async () => {
    renderDashboard();
    expect(
      screen.getByText(/Waiting for the first live reading/i),
    ).toBeInTheDocument();
    expect(await screen.findByText("5.0 TB")).toBeInTheDocument();
    expect(screen.getByText("Storage Allocation")).toBeInTheDocument();
  });

  it("links Movies and Series cells into Library with mode and tier", async () => {
    const { container } = renderDashboard();
    await screen.findByText("5.0 TB");

    // Read hrefs off the anchors rather than selecting on them: jsdom's CSS
    // attribute matcher won't match a value containing the "?" of a query
    // string, and .href would be absolutized to http://localhost/…
    const links = [...container.querySelectorAll("a")];
    const hrefs = links.map((a) => a.getAttribute("href"));
    expect(hrefs).toContain("/library?mode=movies&tier=lossless");
    expect(hrefs).toContain("/library?mode=movies&tier=medium");
    expect(hrefs).toContain("/library?mode=series&tier=high");
    // The Unknown tier is a real, linkable drill-down target, not a dead cell.
    expect(hrefs).toContain("/library?mode=series&tier=unknown");

    const movies = links.find(
      (a) => a.getAttribute("href") === "/library?mode=movies&tier=lossless",
    );
    expect(movies!.textContent).toContain("5.0 TB");
    expect(movies!.textContent).toContain("12 items");
  });

  it("renders Adult cells as non-interactive with an explanation", async () => {
    const { container } = renderDashboard();
    await screen.findByText("5.0 TB");

    const adultRow = container.querySelectorAll("tbody tr")[2]!;
    expect(adultRow.textContent).toContain("Adult");
    expect(adultRow.querySelector("a")).toBeNull();
    for (const a of container.querySelectorAll("a")) {
      expect(a.getAttribute("href")).not.toContain("mode=adult");
    }

    const disabled = adultRow.querySelector(
      '[aria-disabled="true"][title]',
    );
    expect(disabled).not.toBeNull();
    expect(disabled!.getAttribute("title")).toBe(
      "Adult isn't browsable in Library yet",
    );
    expect(disabled!.textContent).toContain("6.0 TB");
    expect(disabled!.textContent).toContain("100 items");

    // The spec required the drill-down be VISIBLY stubbed, not just marked up
    // for assistive tech — assert the dimming class, since aria-disabled and a
    // title alone leave the cell indistinguishable from a live one at rest.
    // Opacity is what actually dims it: body()'s own text-fg on the size line
    // beats any colour class on this wrapper, but opacity applies subtree-wide.
    expect(disabled!.className).toContain("opacity-50");
    expect(disabled!.className).toContain("cursor-not-allowed");
  });

  it("explains the Series row total's tier overlap, and only that row's", async () => {
    const { container } = renderDashboard();
    await screen.findByText("5.0 TB");

    // Last cell of each row is the Total column. Rows are movies/series/adult.
    const totalOf = (rowIndex: number) => {
      const cells = container
        .querySelectorAll("tbody tr")
        [rowIndex]!.querySelectorAll("td");
      return cells[cells.length - 1]!;
    };

    expect(totalOf(1).getAttribute("title")).toContain(
      "can exceed the number of distinct series",
    );
    // Movies/Adult items hold exactly one tier, so their totals can't
    // over-count — they must carry no tooltip at all. This also guards the
    // conditional itself: Solid assigning `title = undefined` as a property
    // would surface here as the literal string "undefined".
    expect(totalOf(0).getAttribute("title")).toBeNull();
    expect(totalOf(2).getAttribute("title")).toBeNull();
  });

  it("renders a zero cell as a dash with no link", async () => {
    const { container } = renderDashboard();
    await screen.findByText("5.0 TB");

    // Movies/Low is 0 bytes and 0 items in the fixture.
    const lowCell = container.querySelectorAll("tbody tr")[0]!.querySelectorAll("td")[0]!;
    expect(lowCell.textContent).toContain("—");
    expect(lowCell.textContent).not.toContain("items");
    expect(lowCell.querySelector("a")).toBeNull();
    const hrefs = [...container.querySelectorAll("a")].map((a) =>
      a.getAttribute("href"),
    );
    expect(hrefs).not.toContain("/library?mode=movies&tier=low");
  });

  it("shows an error banner when the storage allocation endpoint fails", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response("storage query failed", { status: 500 })),
    );
    const { container } = renderDashboard();

    expect(
      await screen.findByText(/Storage allocation unavailable — storage query failed/),
    ).toBeInTheDocument();
    expect(container.querySelector("table")).toBeNull();
  });

  it("renders a sparkline polyline after multiple events", async () => {
    const { container } = renderDashboard();
    // A single point renders no line; a second point produces a polyline.
    MockEventSource.last!.emit(snapshot({ cpuPercent: 10 }));
    MockEventSource.last!.emit(snapshot({ cpuPercent: 20 }));
    MockEventSource.last!.emit(snapshot({ cpuPercent: 30 }));
    await screen.findByText("30.0%");
    expect(container.querySelector("polyline")).not.toBeNull();
  });
});
