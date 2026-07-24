// Organize groups Rename / Purge / Dedup — the three review-queue workflows an
// operator runs to keep the library clean — under a single sidebar entry as
// client-side tabs, replacing their three former separate top-level routes and
// nav entries. It follows Settings' section-tab pattern exactly: its own tab set
// registers with the app shell (ScreenTabs draws the bar in the shell's one
// consistent slot), and each tab renders the full existing screen component
// unchanged via <Show>. The active tab is remembered across reloads in
// localStorage (createPersistedString), falling back to "rename" when the stored
// value is missing, unreadable, or not one of the three known ids.
//
// The embedded Rename/Purge/Dedup each render ModeTabs, which — like this
// screen's own ScreenTabs — registers a tab set with the shell's SINGLE tab
// slot. Left alone, the child's Movies/Series/Adult registration would mount
// last and clobber this screen's Rename/Purge/Dedup workflow tabs, hiding the
// workflow switcher entirely. So the children are wrapped in a shadowing
// ScreenTabsContext.Provider (value={undefined}): with no shell setter in scope,
// their ModeTabs falls back to rendering its Movies/Series/Adult bar INLINE in
// the body — exactly as it does in each child's own standalone unit test — while
// this screen's ScreenTabs keeps the shell slot. Mirrors Settings, whose inline
// mode selector is likewise a body-level bar, not a shell registration.

import { type Component, Show } from "solid-js";
import { ScreenTabs, ScreenTabsContext, type TabDef } from "../components/ui";
import { createPersistedString } from "./AppShell";
import { Rename } from "./Rename";
import { Purge } from "./Purge";
import { Dedup } from "./Dedup";

// ORGANIZE_TAB_KEY persists the active workflow tab across reloads. Follows
// SIDEBAR_COLLAPSED_KEY's short dotted app-prefixed convention.
const ORGANIZE_TAB_KEY = "sakms.organize.tab";

// WORKFLOW_TABS is Organize's section-level tab set: the three review-queue
// workflows, in the same order they held in the old sidebar.
const WORKFLOW_TABS: TabDef[] = [
  { id: "rename", label: "Rename" },
  { id: "purge", label: "Purge" },
  { id: "dedup", label: "Dedup" },
];

const TAB_IDS = WORKFLOW_TABS.map((t) => t.id);

export const Organize: Component = () => {
  const [stored, setTab] = createPersistedString(ORGANIZE_TAB_KEY, "rename");
  // Sanitize the persisted value against the known ids: a stale/garbage stored
  // string falls back to "rename" for display without rewriting storage until
  // the operator actually picks a tab.
  const tab = () => (TAB_IDS.includes(stored()) ? stored() : "rename");

  return (
    <div>
      <ScreenTabs tabs={WORKFLOW_TABS} current={tab} onSelect={setTab} />

      {/* Shadow the shell's tab-registration context so the embedded screens'
          ModeTabs render their Movies/Series/Adult bar inline instead of
          hijacking the shell slot (see file header). */}
      <ScreenTabsContext.Provider value={undefined}>
        <Show when={tab() === "rename"}>
          <Rename />
        </Show>
        <Show when={tab() === "purge"}>
          <Purge />
        </Show>
        <Show when={tab() === "dedup"}>
          <Dedup />
        </Show>
      </ScreenTabsContext.Provider>
    </div>
  );
};
