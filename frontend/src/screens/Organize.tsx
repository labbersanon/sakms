// Organize groups Rename / Purge / Dedup — the three review-queue workflows an
// operator runs to keep the library clean — under a single sidebar entry.
// Workflow switching lives in the sidebar (collapsible group / flyout); this
// screen reads `?tab=` and renders the matching embedded component. The active
// tab is also mirrored to localStorage (`sakms.organize.tab`).
//
// The embedded Rename/Purge/Dedup each render ModeTabs, which registers a tab
// set with the shell's SINGLE tab slot. Left alone, the child's Movies/Series/
// Adult registration would mount last and claim that slot. So the children are
// wrapped in a shadowing ScreenTabsContext.Provider (value={undefined}): with
// no shell setter in scope, their ModeTabs falls back to rendering its
// Movies/Series/Adult bar INLINE in the body.
//
// Claude 2026-08-05: dropped ScreenTabs workflow bar; URL ?tab= is sole switcher
//   besides the sidebar (deep-interview-organize-nav-collapse).
// Reason: sidebar nested nav replaces shell workflow pills.
// Troubleshooting: missing/invalid ?tab= → last-used or rename via replace navigate.
// Review if: nested paths replace query params.

import { type Component, Show, createEffect } from "solid-js";
import { useSearchParams } from "@solidjs/router";
import { ScreenTabsContext } from "../components/ui";
import { Rename } from "./Rename";
import { Purge } from "./Purge";
import { Dedup } from "./Dedup";
import {
  type OrganizeTabId,
  isOrganizeTabId,
  readStoredOrganizeTab,
  sanitizeOrganizeTab,
  writeStoredOrganizeTab,
} from "./organizeTabs";

export const Organize: Component = () => {
  const [params, setParams] = useSearchParams();

  const rawTab = () => {
    const t = params.tab;
    return typeof t === "string" ? t : Array.isArray(t) ? t[0] : undefined;
  };
  const tab = (): OrganizeTabId => sanitizeOrganizeTab(rawTab());

  // Keep URL + localStorage honest: bare /organize or garbage ?tab= become a
  // canonical ?tab= via replace (no history spam).
  createEffect(() => {
    const raw = rawTab();
    const next = isOrganizeTabId(raw) ? raw : readStoredOrganizeTab();
    if (raw !== next) {
      setParams({ tab: next }, { replace: true });
    }
    writeStoredOrganizeTab(next);
  });

  return (
    <div>
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
