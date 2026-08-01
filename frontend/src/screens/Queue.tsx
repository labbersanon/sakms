// Queue groups Downloads / Requests / Grabs — the three screens an operator
// watches while things are in flight — under a single sidebar entry as
// client-side tabs, replacing their three former separate top-level routes and
// nav entries. It follows Organize's section-tab pattern exactly: its own tab
// set registers with the app shell (ScreenTabs draws the bar in the shell's one
// consistent slot), and each tab renders the full existing screen component
// unchanged via <Show>. The active tab is remembered across reloads in
// localStorage (createPersistedString), falling back to "downloads" when the
// stored value is missing, unreadable, or not one of the three known ids.
//
// The embedded Grabs renders ModeTabs, which — like this screen's own
// ScreenTabs — registers a tab set with the shell's SINGLE tab slot. Grabs
// mounts AFTER Queue, so left alone its Movies/Series/Adult registration would
// clobber this screen's Downloads/Requests/Grabs bar, hiding the tab switcher
// entirely. So the children are wrapped in a shadowing ScreenTabsContext.Provider
// (value={undefined}): with no shell setter in scope, ModeTabs falls back to
// rendering its Movies/Series/Adult bar INLINE in the body — exactly as it does
// in Grabs.test.tsx standalone — while this screen's ScreenTabs keeps the shell
// slot. Grabs is the only child that needs this: Downloads and Requests render
// neither ModeTabs nor ScreenTabs at all (verified). The Provider still wraps
// all three, matching Organize.tsx line-for-line.

import { type Component, Show } from "solid-js";
import { ScreenTabs, ScreenTabsContext, type TabDef } from "../components/ui";
import { createPersistedString } from "./AppShell";
import { Downloads } from "./Downloads";
import { Requests } from "./Requests";
import { Grabs } from "./Grabs";

// QUEUE_TAB_KEY persists the active in-flight tab across reloads. Follows
// ORGANIZE_TAB_KEY's short dotted app-prefixed convention.
const QUEUE_TAB_KEY = "sakms.queue.tab";

// QUEUE_TABS is Queue's section-level tab set. The order is Downloads →
// Requests → Grabs, which is deliberately NOT the old sidebar order
// (Downloads/Grabs/Requests) — don't "restore" that.
const QUEUE_TABS: TabDef[] = [
  { id: "downloads", label: "Downloads" },
  { id: "requests", label: "Requests" },
  { id: "grabs", label: "Grabs" },
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

      {/* Shadow the shell's tab-registration context so the embedded Grabs'
          ModeTabs renders its Movies/Series/Adult bar inline instead of
          hijacking the shell slot (see file header). */}
      <ScreenTabsContext.Provider value={undefined}>
        <Show when={tab() === "downloads"}>
          <Downloads />
        </Show>
        <Show when={tab() === "requests"}>
          <Requests />
        </Show>
        <Show when={tab() === "grabs"}>
          <Grabs />
        </Show>
      </ScreenTabsContext.Provider>
    </div>
  );
};
