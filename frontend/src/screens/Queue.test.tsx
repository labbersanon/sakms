// Queue screen tests — sidebar-driven tabs via ?tab= (Downloads / Requests /
// Calendar). Shell ScreenTabs for workflows were removed (queue-sidebar-nest).
//
// Covered:
//   - ?tab= selects the embedded screen; default/invalid → downloads (or stored);
//   - active tab mirrored to localStorage and stale values (e.g. "grabs") now
//     canonicalize like Organize — URL + storage are rewritten on first render;
//   - embedded ModeTabs never register with the shell tab slot (shadow provider).

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@solidjs/testing-library";
import { createSignal, Show } from "solid-js";
import { MemoryRouter, Route, createMemoryHistory } from "@solidjs/router";
import {
  ScreenTabBar,
  ScreenTabsContext,
  type ScreenTabsRegistration,
} from "../components/ui";
import { Queue } from "./Queue";
import { QUEUE_TAB_KEY } from "./queueTabs";
import { jsonResponse } from "../testing/http";

class MockEventSource {
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  url: string;
  constructor(url: string) {
    this.url = url;
  }
  close() {}
}

// stubFetch answers the mount GETs the embedded screens fire with empty arrays
// so each lands in its "nothing yet" empty state. Downloads' pause-state read
// returns an object (a bare [] leaves `d.paused` undefined).
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

// Per-tab markers, each unique to one embedded screen.
const DOWNLOADS_MARKER = "Connecting to the download engine…";
const REQUESTS_MARKER = "No requests match this filter.";
const CALENDAR_MARKER = "Upcoming";

const renderQueue = (url = "/queue") => {
  const history = createMemoryHistory();
  history.set({ value: url, replace: true });
  return render(() => (
    <MemoryRouter history={history}>
      <Route path="/queue" component={Queue} />
      <Route path="*/*" component={Queue} />
    </MemoryRouter>
  ));
};

beforeEach(() => {
  localStorage.clear();
  vi.stubGlobal("EventSource", MockEventSource);
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  localStorage.clear();
});

describe("Queue — query tab", () => {
  it("defaults to Downloads when no ?tab=", async () => {
    stubFetch();
    renderQueue("/queue");
    expect(await screen.findByText(DOWNLOADS_MARKER)).toBeInTheDocument();
    expect(screen.queryByText(REQUESTS_MARKER)).toBeNull();
    await waitFor(() =>
      expect(localStorage.getItem(QUEUE_TAB_KEY)).toBe("downloads"),
    );
  });

  it("?tab=requests shows Requests empty state", async () => {
    stubFetch();
    renderQueue("/queue?tab=requests");
    expect(await screen.findByText(REQUESTS_MARKER)).toBeInTheDocument();
  });

  it("?tab=calendar shows Calendar view switch", async () => {
    stubFetch();
    renderQueue("/queue?tab=calendar");
    expect(
      await screen.findByRole("button", { name: CALENDAR_MARKER }),
    ).toBeInTheDocument();
  });

  it("opens the persisted tab when ?tab= missing", async () => {
    localStorage.setItem(QUEUE_TAB_KEY, "calendar");
    stubFetch();
    renderQueue("/queue");
    expect(
      await screen.findByRole("button", { name: CALENDAR_MARKER }),
    ).toBeInTheDocument();
    expect(screen.queryByText(DOWNLOADS_MARKER)).toBeNull();
  });

  it("falls back to Downloads when ?tab= is unrecognized", async () => {
    stubFetch();
    renderQueue("/queue?tab=not-a-real-tab");
    expect(await screen.findByText(DOWNLOADS_MARKER)).toBeInTheDocument();
    expect(screen.queryByText(REQUESTS_MARKER)).toBeNull();
  });

  it("canonicalizes stale 'grabs' storage to downloads and rewrites storage", async () => {
    localStorage.setItem(QUEUE_TAB_KEY, "grabs");
    stubFetch();
    renderQueue("/queue");
    expect(await screen.findByText(DOWNLOADS_MARKER)).toBeInTheDocument();
    await waitFor(() =>
      expect(localStorage.getItem(QUEUE_TAB_KEY)).toBe("downloads"),
    );
  });

  it("does not render workflow ScreenTabs buttons", async () => {
    stubFetch();
    renderQueue("/queue?tab=downloads");
    await screen.findByText(DOWNLOADS_MARKER);
    // Sidebar-driven nav: no inline tab pills.
    expect(screen.queryByRole("button", { name: "Requests" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Calendar" })).toBeNull();
  });
});

describe("Queue — embedded ModeTabs do not hijack the shell tab slot", () => {
  const renderQueueInShell = (url = "/queue?tab=calendar") => {
    const history = createMemoryHistory();
    history.set({ value: url, replace: true });
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
    return render(() => (
      <MemoryRouter history={history}>
        <Route path="/queue" component={Harness} />
        <Route path="*/*" component={Harness} />
      </MemoryRouter>
    ));
  };

  it("leaves the shell slot empty (no workflow registration) and keeps ModeTabs inline", async () => {
    stubFetch();
    const { queryByTestId } = renderQueueInShell();
    await screen.findByRole("button", { name: CALENDAR_MARKER });
    // Queue no longer registers ScreenTabs — shell slot stays empty.
    expect(queryByTestId("shell-slot")).toBeNull();
    // Movies mode tab is inline in the body.
    expect(screen.getByText("Movies")).toBeInTheDocument();
  });
});
