// Organize screen tests — sidebar-driven workflows via ?tab= (Rename / Purge /
// Dedup). Shell ScreenTabs for workflows were removed
// (deep-interview-organize-nav-collapse).
//
// Covered:
//   - ?tab= selects the embedded screen; default/invalid → rename (or stored);
//   - active tab mirrored to localStorage;
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
import { Organize } from "./Organize";
import { ORGANIZE_TAB_KEY } from "./organizeTabs";

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

const stubFetch = () => {
  const fn = vi.fn(async (input: RequestInfo | URL) => {
    void input;
    // Proposal pages are {items,total,...}; pruning-rules/events are arrays.
    const url = String(input);
    if (url.includes("/proposals")) {
      return jsonResponse({ items: [], total: 0, limit: 50, offset: 0 });
    }
    return jsonResponse([]);
  });
  vi.stubGlobal("fetch", fn);
};

const renderOrganize = (url = "/organize") => {
  const history = createMemoryHistory();
  history.set({ value: url, replace: true });
  return render(() => (
    <MemoryRouter history={history}>
      <Route path="/organize" component={Organize} />
      <Route path="*/*" component={Organize} />
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

describe("Organize — query tab", () => {
  it("defaults to Rename when no ?tab=", async () => {
    stubFetch();
    renderOrganize("/organize");
    expect(
      await screen.findByText("No proposals yet — click Scan."),
    ).toBeInTheDocument();
    expect(screen.queryByText("Rules")).toBeNull();
    await waitFor(() =>
      expect(localStorage.getItem(ORGANIZE_TAB_KEY)).toBe("rename"),
    );
  });

  // Claude 2026-08-11: re-targeted from the retired "Allowlist" section onto
  // the Rules card's <summary>, which now occupies that slot on the Clean-up
  // screen. Asserting a landmark unique to this tab is the point — the shared
  // "No proposals yet" empty state would pass on any of the three.
  it("?tab=purge shows Clean-up (its Rules card)", async () => {
    stubFetch();
    renderOrganize("/organize?tab=purge");
    expect(await screen.findByText("Rules")).toBeInTheDocument();
  });

  it("?tab=dedup shows Dedup empty state", async () => {
    stubFetch();
    renderOrganize("/organize?tab=dedup");
    expect(
      await screen.findByText("No duplicate groups yet — click Scan."),
    ).toBeInTheDocument();
  });

  it("opens the persisted tab when ?tab= missing", async () => {
    localStorage.setItem(ORGANIZE_TAB_KEY, "dedup");
    stubFetch();
    renderOrganize("/organize");
    expect(
      await screen.findByText("No duplicate groups yet — click Scan."),
    ).toBeInTheDocument();
    expect(screen.queryByText("No proposals yet — click Scan.")).toBeNull();
  });

  it("falls back to Rename when ?tab= is unrecognized", async () => {
    stubFetch();
    renderOrganize("/organize?tab=not-a-real-tab");
    expect(
      await screen.findByText("No proposals yet — click Scan."),
    ).toBeInTheDocument();
    expect(screen.queryByText("Rules")).toBeNull();
  });

  it("does not render workflow ScreenTabs buttons", async () => {
    stubFetch();
    renderOrganize("/organize?tab=rename");
    await screen.findByText("No proposals yet — click Scan.");
    // Mode tabs still exist (Movies/…); workflow pills must not.
    expect(screen.queryByRole("button", { name: "Clean-up" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Dedup" })).toBeNull();
  });
});

describe("Organize — embedded ModeTabs do not hijack the shell tab slot", () => {
  const renderOrganizeInShell = (url = "/organize?tab=rename") => {
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
          <Organize />
        </ScreenTabsContext.Provider>
      );
    };
    return render(() => (
      <MemoryRouter history={history}>
        <Route path="/organize" component={Harness} />
        <Route path="*/*" component={Harness} />
      </MemoryRouter>
    ));
  };

  it("leaves the shell slot empty (no workflow registration) and keeps ModeTabs inline", async () => {
    stubFetch();
    const { queryByTestId } = renderOrganizeInShell();
    await screen.findByText("No proposals yet — click Scan.");
    // Organize no longer registers ScreenTabs — shell slot stays empty.
    expect(queryByTestId("shell-slot")).toBeNull();
    // Movies mode tab is inline in the body.
    expect(screen.getByRole("button", { name: "Movies" })).toBeInTheDocument();
  });
});
