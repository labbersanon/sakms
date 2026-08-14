// Queue screen tests — the single sidebar entry that groups Downloads /
// Requests / Calendar as client-side tabs (replacing their three former
// top-level routes).
//
// Covered:
//   - all three tabs render, are clickable, and switching shows the right
//     embedded screen's content — and no "Grabs" tab exists any more, since
//     Calendar took tab 3 and the standalone Grabs screen was deleted outright
//     (see Queue.tsx's header);
//   - the active tab is remembered across reloads (a preset localStorage value
//     selects that tab at mount), an unrecognized stored value falls back to
//     Downloads, and that fallback is display-only — it does NOT rewrite storage;
//   - the legacy stored value "grabs" is simply one of those unrecognized values
//     now, so it opens Downloads. That is deliberate and deliberately NOT a
//     "grabs" → "calendar" rewrite — the display-only fallback already handles
//     the stale value honestly;
//   - selecting a tab persists that choice to localStorage;
//   - the load-bearing registration guard (mirrors Organize.test.tsx): mounted
//     inside a real ScreenTabsContext, Queue's OWN Downloads/Requests/Calendar
//     tabs own the shell slot, and the Movies/Series/Adult ModeTabs rendered by
//     Calendar's History view never clobbers it — it falls back to an inline
//     body bar instead.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@solidjs/testing-library";
import { createSignal, Show } from "solid-js";
import {
  ScreenTabBar,
  ScreenTabsContext,
  type ScreenTabsRegistration,
} from "../components/ui";
import { Queue } from "./Queue";
import { jsonResponse } from "../testing/http";

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
//
// Calendar's marker is the accessible NAME of a button, not body text: it is
// the "Upcoming" half of Calendar's own History/Upcoming view switch, which no
// other tab renders (verified). Deliberately not History's body copy — that
// varies with the grid/list display mode and with whether any grabs came back,
// while the switch is present unconditionally. Query it with *ByRole, never
// *ByText.
const DOWNLOADS_MARKER = "Connecting to the download engine…";
const REQUESTS_MARKER = "No requests match this filter.";
const CALENDAR_MARKER = "Upcoming";

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
    expect(screen.getByRole("button", { name: "Calendar" })).toBeInTheDocument();
    // Calendar REPLACED Grabs as tab 3 — the old tab is gone, not merely
    // relabelled, and the component behind it is deleted.
    expect(screen.queryByRole("button", { name: "Grabs" })).toBeNull();
    // Default tab is Downloads: its own markers show, and neither sibling's does.
    expect(
      await screen.findByRole("button", { name: "Pause all downloads" }),
    ).toBeInTheDocument();
    expect(screen.getByText(DOWNLOADS_MARKER)).toBeInTheDocument();
    expect(screen.queryByText(REQUESTS_MARKER)).toBeNull();
    expect(screen.queryByRole("button", { name: CALENDAR_MARKER })).toBeNull();
  });

  it("switching to Requests shows the Requests worklist (its empty state)", async () => {
    stubFetch();
    render(() => <Queue />);
    fireEvent.click(screen.getByRole("button", { name: "Requests" }));
    expect(await screen.findByText(REQUESTS_MARKER)).toBeInTheDocument();
  });

  it("switching to Calendar shows the Calendar screen (its view switch)", async () => {
    stubFetch();
    render(() => <Queue />);
    fireEvent.click(screen.getByRole("button", { name: "Calendar" }));
    expect(
      await screen.findByRole("button", { name: CALENDAR_MARKER }),
    ).toBeInTheDocument();
  });

  it("persists the selected tab to localStorage", async () => {
    stubFetch();
    render(() => <Queue />);
    fireEvent.click(screen.getByRole("button", { name: "Calendar" }));
    await waitFor(() =>
      expect(localStorage.getItem(QUEUE_TAB_KEY)).toBe("calendar"),
    );
  });
});

describe("Queue — persisted default tab", () => {
  it("opens the persisted tab at mount (calendar) instead of Downloads", async () => {
    // Set the remembered tab BEFORE mount (in the test body — beforeEach clears
    // storage), simulating a reload after the operator last used Calendar.
    localStorage.setItem(QUEUE_TAB_KEY, "calendar");
    stubFetch();
    render(() => <Queue />);
    expect(
      await screen.findByRole("button", { name: CALENDAR_MARKER }),
    ).toBeInTheDocument();
    // Downloads is NOT the active tab, so its screen isn't mounted.
    expect(screen.queryByText(DOWNLOADS_MARKER)).toBeNull();
  });

  it("opens Downloads for a legacy stored 'grabs', without rewriting it", async () => {
    // The inverse of the pre-Calendar behaviour, and the reason this test still
    // exists rather than having been deleted with the Grabs tab: every operator
    // who last used Grabs still has this exact value in localStorage. "grabs" is
    // no longer a known id, so it takes the same display-only fallback as any
    // other unrecognized value — landing on Downloads, and NOT being migrated to
    // "calendar" (plan §1.1: a rewrite would be the app's only storage-migration
    // path, and the fallback already handles it honestly).
    localStorage.setItem(QUEUE_TAB_KEY, "grabs");
    stubFetch();
    render(() => <Queue />);
    expect(await screen.findByText(DOWNLOADS_MARKER)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: CALENDAR_MARKER })).toBeNull();
    expect(localStorage.getItem(QUEUE_TAB_KEY)).toBe("grabs");
  });

  it("falls back to Downloads when the stored value is unrecognized", async () => {
    localStorage.setItem(QUEUE_TAB_KEY, "not-a-real-tab");
    stubFetch();
    render(() => <Queue />);
    expect(await screen.findByText(DOWNLOADS_MARKER)).toBeInTheDocument();
    expect(screen.queryByText(REQUESTS_MARKER)).toBeNull();
    expect(screen.queryByRole("button", { name: CALENDAR_MARKER })).toBeNull();
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
// Downloads/Requests/Calendar set with the app shell's single tab slot. The
// embedded Calendar subtree also renders ModeTabs, which registers
// Movies/Series/Adult with that same slot — and mounts AFTER Queue, so without
// the shadowing ScreenTabsContext.Provider in Queue.tsx the child's registration
// would clobber the queue tabs and hide the switcher. A bare render() can't
// catch this (no shell context → both bars fall back to inline), so this suite
// mounts Queue inside a ScreenTabsContext.Provider exactly the way AppShell does
// and asserts the shell slot keeps holding the queue tabs, never the mode tabs —
// including AFTER the click that actually mounts Calendar.
//
// The registering component is Calendar's HISTORY child, not Calendar itself:
// calendar/index.tsx's History/Upcoming switch is a plain ScreenTabBar that
// draws inline and registers nothing, while calendar/History.tsx renders the
// real three-mode ModeTabs. History is Calendar's default view, so one click on
// the Calendar tab is enough to put the collision in play — no second click into
// History is needed, and adding one would not make the test stronger.
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

  it("keeps Downloads/Requests/Calendar in the shell slot and never adopts Movies/Series/Adult", async () => {
    stubFetch();
    const { getByTestId } = renderQueueInShell();
    const shellSlot = () => within(getByTestId("shell-slot"));

    // Queue registers its own tabs with the shell slot at mount.
    expect(
      await shellSlot().findByRole("button", { name: "Downloads" }),
    ).toBeInTheDocument();
    expect(shellSlot().getByRole("button", { name: "Requests" })).toBeInTheDocument();
    expect(shellSlot().getByRole("button", { name: "Calendar" })).toBeInTheDocument();
    // Downloads (the default tab) renders no ModeTabs at all, so nothing has had
    // a chance to clobber the slot yet.
    expect(shellSlot().queryByText("Movies")).toBeNull();

    // The load-bearing click: switching to Calendar mounts Calendar, whose
    // default History view renders ModeTabs. If the shadow provider weren't in
    // place, that registration would replace the shell slot's contents with
    // Movies/Series/Adult.
    fireEvent.click(shellSlot().getByRole("button", { name: "Calendar" }));
    await screen.findByRole("button", { name: CALENDAR_MARKER });
    expect(shellSlot().getByRole("button", { name: "Downloads" })).toBeInTheDocument();
    expect(shellSlot().getByRole("button", { name: "Requests" })).toBeInTheDocument();
    expect(shellSlot().getByRole("button", { name: "Calendar" })).toBeInTheDocument();
    // The discriminating assertion: the shell slot still has no mode tabs...
    expect(shellSlot().queryByText("Movies")).toBeNull();
    // ...and History's bar didn't vanish — it fell back to rendering inline in
    // the body, which is exactly what the shadowing Provider is for.
    expect(screen.getByText("Movies")).toBeInTheDocument();
  });
});
