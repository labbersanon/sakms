// Queue groups Downloads / Requests / Calendar — the three screens an operator
// watches to see what is in flight and what is coming — under a single sidebar
// entry as client-side tabs. Downloads and Requests were formerly separate
// top-level routes with their own nav entries, and so was Grabs, which held the
// third slot until Calendar replaced it (see below). Calendar itself is new: it
// never had a route of its own, so APP_ROUTES gained nothing when it arrived —
// the three routes that were collapsed into /queue are still /downloads,
// /grabs and /requests, which is what routing.test.ts asserts are dead.
//
// This screen follows Organize's section-tab pattern exactly: its own tab set
// registers with the app shell (ScreenTabs draws the bar in the shell's one
// consistent slot), and each tab renders the full existing screen component
// unchanged via <Show>. The active tab is remembered across reloads in
// localStorage (createPersistedString), falling back to "downloads" when the
// stored value is missing, unreadable, or not one of the three known ids.
//
// TAB 3 WAS GRABS UNTIL THE CALENDAR FEATURE REPLACED IT
// (.omc/plans/autopilot-impl-calendar-grabs-requests.md §1.1). The standalone
// Grabs screen (Grabs.tsx + Grabs.test.tsx) was DELETED outright in that same
// change, per this repo's "no dead code left behind" convention — not kept
// alongside Calendar. Nothing was lost: Calendar's History view carries the
// read-only completed/imported grab log forward, still with no bulk actions and
// no mutate-many affordances. Do not re-add a Grabs tab or resurrect the
// component. An operator whose stored sakms.queue.tab is still "grabs" lands on
// Downloads through the sanitizing fallback below; that is the deliberate,
// tested outcome, and deliberately NOT a "grabs" → "calendar" storage rewrite —
// the fallback already handles the stale value honestly, and a rewrite would be
// the only storage-migration path in the app.
//
// THE CALENDAR SUBTREE RENDERS ModeTabs, which — like this screen's own
// ScreenTabs — registers a tab set with the shell's SINGLE tab slot. It mounts
// AFTER Queue, so left alone its Movies/Series/Adult registration would clobber
// this screen's Downloads/Requests/Calendar bar, hiding the tab switcher
// entirely. So the children are wrapped in a shadowing ScreenTabsContext.Provider
// (value={undefined}): with no shell setter in scope, ModeTabs falls back to
// rendering its Movies/Series/Adult bar INLINE in the body — exactly as it does
// in calendar/History.test.tsx standalone — while this screen's ScreenTabs keeps
// the shell slot. Be precise about WHICH component registers: it is Calendar's
// History child, not Calendar itself. calendar/index.tsx's own History/Upcoming
// switch is a plain ScreenTabBar that draws inline and registers nothing, while
// calendar/History.tsx renders the real three-mode ModeTabs — and History is
// Calendar's default view, so the collision is live from the moment the Calendar
// tab is opened. Calendar is the only child that needs this: Downloads and
// Requests render neither ModeTabs nor ScreenTabs at all (verified). The
// Provider still wraps all three, matching Organize.tsx line-for-line.

import { type Component, Show } from "solid-js";
import { ScreenTabs, ScreenTabsContext, type TabDef } from "../components/ui";
import { createPersistedString } from "./AppShell";
import { Downloads } from "./Downloads";
import { Requests } from "./Requests";
import { Calendar } from "./calendar";

// QUEUE_TAB_KEY persists the active in-flight tab across reloads. Follows
// ORGANIZE_TAB_KEY's short dotted app-prefixed convention.
const QUEUE_TAB_KEY = "sakms.queue.tab";

// QUEUE_TABS is Queue's section-level tab set. The order is Downloads →
// Requests → Calendar. Calendar holds the slot the standalone Grabs screen used
// to occupy (see the file header) — that screen is deleted, so there is no
// "grabs" tab to restore here.
const QUEUE_TABS: TabDef[] = [
  { id: "downloads", label: "Downloads" },
  { id: "requests", label: "Requests" },
  { id: "calendar", label: "Calendar" },
];

const TAB_IDS = QUEUE_TABS.map((t) => t.id);

export const Queue: Component = () => {
  const [stored, setTab] = createPersistedString(QUEUE_TAB_KEY, "downloads");
  // Sanitize the persisted value against the known ids: a stale/garbage stored
  // string falls back to "downloads" for display without rewriting storage until
  // the operator actually picks a tab.
  const tab = () => (TAB_IDS.includes(stored()) ? stored() : "downloads");

  return (
    <div>
      <ScreenTabs tabs={QUEUE_TABS} current={tab} onSelect={setTab} />

      {/* Shadow the shell's tab-registration context so the ModeTabs inside
          Calendar's History view renders its Movies/Series/Adult bar inline
          instead of hijacking the shell slot (see file header). */}
      <ScreenTabsContext.Provider value={undefined}>
        <Show when={tab() === "downloads"}>
          <Downloads />
        </Show>
        <Show when={tab() === "requests"}>
          <Requests />
        </Show>
        <Show when={tab() === "calendar"}>
          <Calendar />
        </Show>
      </ScreenTabsContext.Provider>
    </div>
  );
};
