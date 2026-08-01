// Queue screen tests — the single sidebar entry that groups Downloads /
// Requests / Grabs as client-side tabs (replacing their three former top-level
// routes).
//
// Covered:
//   - all three tabs render, are clickable, and switching shows the right
//     embedded screen's content;
//   - the active tab is remembered across reloads (a preset localStorage value
//     selects that tab at mount), an unrecognized stored value falls back to
//     Downloads, and that fallback is display-only — it does NOT rewrite storage;
//   - selecting a tab persists that choice to localStorage;
//   - the load-bearing registration guard (mirrors Organize.test.tsx): mounted
//     inside a real ScreenTabsContext, Queue's OWN Downloads/Requests/Grabs tabs
//     own the shell slot, and the embedded Grabs' Movies/Series/Adult ModeTabs
//     never clobbers it — it falls back to an inline body bar instead.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@solidjs/testing-library";
import { createSignal, Show } from "solid-js";
import {
  ScreenTabBar,
  ScreenTabsContext,
  type ScreenTabsRegistration,
} from "../components/ui";
import { Queue } from "./Queue";

const QUEUE_TAB_KEY = "sakms.queue.tab";

// jsdom has no EventSource, and Downloads — the DEFAULT tab — opens one on
// mount, so every single render() in this file needs the stub. Mirrors
// Organize.test.tsx / Downloads.test.tsx.
class MockEventSource {
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  url: string;
  constructor(url: string) {
    this.url = url;
  }
  close() {}
}

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

// stubFetch answers the mount GETs the embedded screens fire with empty arrays
// so each lands in its "nothing yet" empty state. The one exception is
// Downloads' global pause-state read, which is an OBJECT — a bare [] leaves
// `d.paused` undefined instead of a real boolean.
const stubFetch = () => {
  const fn = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.includes("/api/downloads/pause-state")) {
      return jsonResponse({ paused: false });
    }
    return jsonResponse([]);
  });
  vi.stubGlobal("fetch", fn);
};

// Per-tab markers, each unique to one embedded screen. Downloads' is the
// fallback shown while hasData() is false — always the case here, since the
// stubbed EventSource never delivers a frame.
const DOWNLOADS_MARKER = "Connecting to the download engine…";
const REQUESTS_MARKER = "No requests match this filter.";
const GRABS_MARKER = "No grabs yet for this mode.";

beforeEach(() => {
  localStorage.clear();
  vi.stubGlobal("EventSource", MockEventSource);
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  localStorage.clear();
});

describe("Queue — in-flight tabs", () => {
  it("renders all three tabs and defaults to Downloads", async () => {
    stubFetch();
    render(() => <Queue />);
    // The three tab buttons are present (inline ScreenTabBar fallback, since
    // there's no shell context here).
    expect(screen.getByRole("button", { name: "Downloads" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Requests" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Grabs" })).toBeInTheDocument();
    // Default tab is Downloads: its own markers show, and neither sibling's does.
    expect(
      await screen.findByRole("button", { name: "Pause all downloads" }),
    ).toBeInTheDocument();
    expect(screen.getByText(DOWNLOADS_MARKER)).toBeInTheDocument();
    expect(screen.queryByText(REQUESTS_MARKER)).toBeNull();
    expect(screen.queryByText(GRABS_MARKER)).toBeNull();
  });

  it("switching to Requests shows the Requests worklist (its empty state)", async () => {
    stubFetch();
    render(() => <Queue />);
    fireEvent.click(screen.getByRole("button", { name: "Requests" }));
    expect(await screen.findByText(REQUESTS_MARKER)).toBeInTheDocument();
  });

  it("switching to Grabs shows the Grabs log (its empty state)", async () => {
    stubFetch();
    render(() => <Queue />);
    fireEvent.click(screen.getByRole("button", { name: "Grabs" }));
    expect(await screen.findByText(GRABS_MARKER)).toBeInTheDocument();
  });

  it("persists the selected tab to localStorage", async () => {
    stubFetch();
    render(() => <Queue />);
    fireEvent.click(screen.getByRole("button", { name: "Grabs" }));
    await waitFor(() =>
      expect(localStorage.getItem(QUEUE_TAB_KEY)).toBe("grabs"),
    );
  });
});

describe("Queue — persisted default tab", () => {
  it("opens the persisted tab at mount (grabs) instead of Downloads", async () => {
    // Set the remembered tab BEFORE mount (in the test body — beforeEach clears
    // storage), simulating a reload after the operator last used Grabs.
    localStorage.setItem(QUEUE_TAB_KEY, "grabs");
    stubFetch();
    render(() => <Queue />);
    expect(await screen.findByText(GRABS_MARKER)).toBeInTheDocument();
    // Downloads is NOT the active tab, so its screen isn't mounted.
    expect(screen.queryByText(DOWNLOADS_MARKER)).toBeNull();
  });

  it("falls back to Downloads when the stored value is unrecognized", async () => {
    localStorage.setItem(QUEUE_TAB_KEY, "not-a-real-tab");
    stubFetch();
    render(() => <Queue />);
    expect(await screen.findByText(DOWNLOADS_MARKER)).toBeInTheDocument();
    expect(screen.queryByText(REQUESTS_MARKER)).toBeNull();
    expect(screen.queryByText(GRABS_MARKER)).toBeNull();
  });

  it("sanitizes an unrecognized stored value for display only, without rewriting it", async () => {
    localStorage.setItem(QUEUE_TAB_KEY, "not-a-real-tab");
    stubFetch();
    render(() => <Queue />);
    await screen.findByText(DOWNLOADS_MARKER);
    // The fallback is a display-time decision; storage stays untouched until the
    // operator actually picks a tab.
    expect(localStorage.getItem(QUEUE_TAB_KEY)).toBe("not-a-real-tab");
  });
});

// --- Shell tab-slot ownership (registration-collision guard) ----------------
//
// The regression this guards: Queue's ScreenTabs registers its
// Downloads/Requests/Grabs set with the app shell's single tab slot. The
// embedded Grabs also renders ModeTabs, which registers Movies/Series/Adult
// with that same slot — and mounts AFTER Queue, so without the shadowing
// ScreenTabsContext.Provider in Queue.tsx the child's registration would clobber
// the queue tabs and hide the switcher. A bare render() can't catch this (no
// shell context → both bars fall back to inline), so this suite mounts Queue
// inside a ScreenTabsContext.Provider exactly the way AppShell does and asserts
// the shell slot keeps holding the queue tabs, never the child's mode tabs —
// including AFTER the click that actually mounts Grabs.
describe("Queue — embedded ModeTabs do not hijack the shell tab slot", () => {
  const renderQueueInShell = () => {
    const Harness = () => {
      const [reg, setReg] = createSignal<ScreenTabsRegistration | null>(null);
      return (
        <ScreenTabsContext.Provider value={setReg}>
          <Show when={reg()}>
            {(r) => (
              <div data-testid="shell-slot">
                <ScreenTabBar
                  tabs={r().tabs}
                  current={r().current}
                  onSelect={r().onSelect}
                  trailing={r().trailing}
                />
              </div>
            )}
          </Show>
          <Queue />
        </ScreenTabsContext.Provider>
      );
    };
    return render(() => <Harness />);
  };

  it("keeps Downloads/Requests/Grabs in the shell slot and never adopts Movies/Series/Adult", async () => {
    stubFetch();
    const { getByTestId } = renderQueueInShell();
    const shellSlot = () => within(getByTestId("shell-slot"));

    // Queue registers its own tabs with the shell slot at mount.
    expect(
      await shellSlot().findByRole("button", { name: "Downloads" }),
    ).toBeInTheDocument();
    expect(shellSlot().getByRole("button", { name: "Requests" })).toBeInTheDocument();
    expect(shellSlot().getByRole("button", { name: "Grabs" })).toBeInTheDocument();
    // Downloads (the default tab) renders no ModeTabs at all, so nothing has had
    // a chance to clobber the slot yet.
    expect(shellSlot().queryByText("Movies")).toBeNull();

    // The load-bearing click: switching to Grabs mounts Grabs (and its ModeTabs).
    // If the shadow provider weren't in place, that registration would replace
    // the shell slot's contents with Movies/Series/Adult.
    fireEvent.click(shellSlot().getByRole("button", { name: "Grabs" }));
    await screen.findByText(GRABS_MARKER);
    expect(shellSlot().getByRole("button", { name: "Downloads" })).toBeInTheDocument();
    expect(shellSlot().getByRole("button", { name: "Requests" })).toBeInTheDocument();
    expect(shellSlot().getByRole("button", { name: "Grabs" })).toBeInTheDocument();
    // The discriminating assertion: the shell slot still has no mode tabs...
    expect(shellSlot().queryByText("Movies")).toBeNull();
    // ...and Grabs' bar didn't vanish — it fell back to rendering inline in the
    // body, which is exactly what the shadowing Provider is for.
    expect(screen.getByText("Movies")).toBeInTheDocument();
  });
});
