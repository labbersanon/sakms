// Dashboard view test — the screen opens one EventSource, shows a loading
// placeholder until the first snapshot arrives, renders the snapshot values,
// and surfaces a reconnecting notice on an EventSource error. EventSource is
// mocked globally (same spirit as other screens' fetch stubs) with a
// controllable instance the test can drive.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@solidjs/testing-library";
import type { SysinfoSnapshot } from "@dto";
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

beforeEach(() => {
  MockEventSource.last = null;
  vi.stubGlobal("EventSource", MockEventSource);
});

afterEach(() => vi.unstubAllGlobals());

describe("Dashboard view", () => {
  it("shows a loading placeholder until the first event arrives", () => {
    render(() => <Dashboard />);
    expect(screen.getByText(/Waiting for the first live reading/i)).toBeInTheDocument();
  });

  it("renders snapshot values once a message arrives", async () => {
    render(() => <Dashboard />);
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
    const { container } = render(() => <Dashboard />);
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
    const { container } = render(() => <Dashboard />);
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
    const { container } = render(() => <Dashboard />);
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
    render(() => <Dashboard />);
    MockEventSource.last!.fail();
    expect(await screen.findByText(/reconnecting/i)).toBeInTheDocument();
  });

  it("shows a metric-read-failed banner on a sampleError event", async () => {
    render(() => <Dashboard />);
    MockEventSource.last!.emitEvent("sampleError", "cpu.stat unreadable");
    expect(
      await screen.findByText(/Metric read failed: cpu\.stat unreadable/),
    ).toBeInTheDocument();
  });

  it("renders 'unlimited' when the memory limit is -1", async () => {
    render(() => <Dashboard />);
    MockEventSource.last!.emit(snapshot({ memLimitBytes: -1 }));
    expect(await screen.findByText(/used \/ unlimited/)).toBeInTheDocument();
  });

  it("renders a GPU card with a utilization gauge", async () => {
    render(() => <Dashboard />);
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
    render(() => <Dashboard />);
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

  it("renders a sparkline polyline after multiple events", async () => {
    const { container } = render(() => <Dashboard />);
    // A single point renders no line; a second point produces a polyline.
    MockEventSource.last!.emit(snapshot({ cpuPercent: 10 }));
    MockEventSource.last!.emit(snapshot({ cpuPercent: 20 }));
    MockEventSource.last!.emit(snapshot({ cpuPercent: 30 }));
    await screen.findByText("30.0%");
    expect(container.querySelector("polyline")).not.toBeNull();
  });
});
