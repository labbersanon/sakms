// Organize screen tests — the single sidebar entry that groups Rename / Purge /
// Dedup as client-side tabs (replacing their three former top-level routes).
//
// Covered:
//   - all three workflow tabs render, are clickable, and switching shows the
//     right embedded screen's content;
//   - the active tab is remembered across reloads (a preset localStorage value
//     selects that tab at mount) and an unrecognized stored value falls back to
//     Rename;
//   - selecting a tab persists that choice to localStorage;
//   - the load-bearing registration guard (mirrors Settings' "UI tab — inner
//     sub-tabs do not hijack the shell tab slot" test): mounted inside a real
//     ScreenTabsContext, Organize's OWN Rename/Purge/Dedup tabs own the shell
//     slot, and the embedded screens' Movies/Series/Adult ModeTabs never clobber
//     it — they fall back to an inline body bar instead.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@solidjs/testing-library";
import { createSignal, Show } from "solid-js";
import {
  ScreenTabBar,
  ScreenTabsContext,
  type ScreenTabsRegistration,
} from "../components/ui";
import { Organize } from "./Organize";

const ORGANIZE_TAB_KEY = "sakms.organize.tab";

// jsdom has no EventSource, and the embedded Dedup screen opens one on mount —
// stub it (a no-op is enough; these tests never drive scan frames). Mirrors
// Dedup.test.tsx.
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

// stubFetch answers the mount GETs every embedded screen fires (proposals and
// Purge's allowlist) with empty arrays, so each lands in its "nothing yet"
// empty state. Nothing here mutates, so a blanket [] default is enough.
const stubFetch = () => {
  const fn = vi.fn(async (input: RequestInfo | URL) => {
    void input;
    return jsonResponse([]);
  });
  vi.stubGlobal("fetch", fn);
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

describe("Organize — workflow tabs", () => {
  it("renders all three workflow tabs and defaults to Rename", async () => {
    stubFetch();
    render(() => <Organize />);
    // The three tab buttons are present (inline ScreenTabBar fallback, since
    // there's no shell context here).
    expect(screen.getByRole("button", { name: "Rename" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Purge" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Dedup" })).toBeInTheDocument();
    // Default tab is Rename: its empty-state text shows and Purge's Allowlist
    // heading (Purge-only, synchronous) is absent — Purge isn't mounted.
    expect(
      await screen.findByText("No proposals yet — click Scan."),
    ).toBeInTheDocument();
    expect(screen.queryByText("Allowlist")).toBeNull();
  });

  it("switching to Purge shows the Purge queue (its Allowlist section)", async () => {
    stubFetch();
    render(() => <Organize />);
    fireEvent.click(screen.getByRole("button", { name: "Purge" }));
    // Allowlist is a Purge-only heading, rendered synchronously on mount.
    expect(await screen.findByText("Allowlist")).toBeInTheDocument();
  });

  it("switching to Dedup shows the Dedup queue (its duplicate-groups empty state)", async () => {
    stubFetch();
    render(() => <Organize />);
    fireEvent.click(screen.getByRole("button", { name: "Dedup" }));
    expect(
      await screen.findByText("No duplicate groups yet — click Scan."),
    ).toBeInTheDocument();
  });

  it("persists the selected tab to localStorage", async () => {
    stubFetch();
    render(() => <Organize />);
    fireEvent.click(screen.getByRole("button", { name: "Dedup" }));
    await waitFor(() =>
      expect(localStorage.getItem(ORGANIZE_TAB_KEY)).toBe("dedup"),
    );
  });
});

describe("Organize — persisted default tab", () => {
  it("opens the persisted tab at mount (dedup) instead of Rename", async () => {
    // Set the remembered tab BEFORE mount (in the test body — beforeEach clears
    // storage), simulating a reload after the operator last used Dedup.
    localStorage.setItem(ORGANIZE_TAB_KEY, "dedup");
    stubFetch();
    render(() => <Organize />);
    expect(
      await screen.findByText("No duplicate groups yet — click Scan."),
    ).toBeInTheDocument();
    // Rename is NOT the active tab, so its empty-state text isn't shown.
    expect(screen.queryByText("No proposals yet — click Scan.")).toBeNull();
  });

  it("falls back to Rename when the stored value is unrecognized", async () => {
    localStorage.setItem(ORGANIZE_TAB_KEY, "not-a-real-tab");
    stubFetch();
    render(() => <Organize />);
    expect(
      await screen.findByText("No proposals yet — click Scan."),
    ).toBeInTheDocument();
    expect(screen.queryByText("Allowlist")).toBeNull();
  });
});

// --- Shell tab-slot ownership (registration-collision guard) ----------------
//
// The regression this guards: Organize's ScreenTabs registers its
// Rename/Purge/Dedup set with the app shell's single tab slot. Each embedded
// screen also renders ModeTabs, which registers Movies/Series/Adult with that
// same slot — and mounts AFTER Organize, so without the shadowing
// ScreenTabsContext.Provider in Organize.tsx the child's registration would
// clobber the workflow tabs and hide the switcher. A bare render() can't catch
// this (no shell context → both bars fall back to inline), so this suite mounts
// Organize inside a ScreenTabsContext.Provider exactly the way AppShell does and
// asserts the shell slot keeps holding the workflow tabs, never the child's mode
// tabs — even after switching tabs.
describe("Organize — embedded ModeTabs do not hijack the shell tab slot", () => {
  const renderOrganizeInShell = () => {
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
    return render(() => <Harness />);
  };

  it("keeps Rename/Purge/Dedup in the shell slot and never adopts Movies/Series/Adult", async () => {
    stubFetch();
    const { getByTestId } = renderOrganizeInShell();
    const shellSlot = () => within(getByTestId("shell-slot"));

    // Organize registers the workflow tabs with the shell slot at mount.
    expect(
      await shellSlot().findByRole("button", { name: "Rename" }),
    ).toBeInTheDocument();
    expect(shellSlot().getByRole("button", { name: "Purge" })).toBeInTheDocument();
    expect(shellSlot().getByRole("button", { name: "Dedup" })).toBeInTheDocument();
    // The embedded Rename's Movies/Series/Adult bar renders inline in the body,
    // NOT in the shell slot.
    expect(shellSlot().queryByText("Movies")).toBeNull();

    // The load-bearing click: switching to Purge mounts Purge (and its ModeTabs).
    // If the shadow provider weren't in place, that registration would replace
    // the shell slot's contents with Movies/Series/Adult.
    fireEvent.click(shellSlot().getByRole("button", { name: "Purge" }));
    await screen.findByText("Allowlist");
    expect(shellSlot().getByRole("button", { name: "Rename" })).toBeInTheDocument();
    expect(shellSlot().getByRole("button", { name: "Purge" })).toBeInTheDocument();
    expect(shellSlot().getByRole("button", { name: "Dedup" })).toBeInTheDocument();
    expect(shellSlot().queryByText("Movies")).toBeNull();
  });
});
