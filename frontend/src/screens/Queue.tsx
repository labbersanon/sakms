// Queue groups Downloads / Requests / Calendar under a single sidebar entry.
// Workflow switching lives in the sidebar (collapsible group / flyout); this
// screen reads `?tab=` and renders the matching embedded component. The active
// tab is also mirrored to localStorage (`sakms.queue.tab`).
//
// Stale stored values (e.g. the old "grabs" id) are no longer display-only
// fallbacks — they now canonicalize like Organize: the createEffect below
// replaces the URL and rewrites storage to a valid tab on first render.
//
// THE CALENDAR SUBTREE RENDERS ModeTabs, which registers a tab set with the
// shell's SINGLE tab slot. It mounts AFTER Queue, so left alone its
// Movies/Series/Adult registration would clobber the shell slot. So the
// children are wrapped in a shadowing ScreenTabsContext.Provider
// (value={undefined}): with no shell setter in scope, ModeTabs falls back to
// rendering its Movies/Series/Adult bar INLINE in the body — exactly as it
// does in calendar/History.test.tsx standalone. The Provider still wraps all
// three children, matching Organize.tsx line-for-line.
//
// Claude 2026-08-29: dropped ScreenTabs workflow bar; URL ?tab= is sole
//   switcher besides the sidebar (queue-sidebar-nest).
// Reason: sidebar nested nav replaces shell workflow pills.
// Troubleshooting: missing/invalid ?tab= → last-used or downloads via replace
//   navigate.
// Review if: nested paths replace query params.

import { type Component, Show, createEffect } from "solid-js";
import { useSearchParams } from "@solidjs/router";
import { ScreenTabsContext } from "../components/ui";
import { Downloads } from "./Downloads";
import { Requests } from "./Requests";
import { Calendar } from "./calendar";
import {
  type QueueTabId,
  isQueueTabId,
  readStoredQueueTab,
  sanitizeQueueTab,
  writeStoredQueueTab,
} from "./queueTabs";

export const Queue: Component = () => {
  const [params, setParams] = useSearchParams();

  const rawTab = () => {
    const t = params.tab;
    return typeof t === "string" ? t : Array.isArray(t) ? t[0] : undefined;
  };
  const tab = (): QueueTabId => sanitizeQueueTab(rawTab());

  // Keep URL + localStorage honest: bare /queue or garbage ?tab= become a
  // canonical ?tab= via replace (no history spam).
  createEffect(() => {
    const raw = rawTab();
    const next = isQueueTabId(raw) ? raw : readStoredQueueTab();
    if (raw !== next) {
      setParams({ tab: next }, { replace: true });
    }
    writeStoredQueueTab(next);
  });

  return (
    <div>
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
