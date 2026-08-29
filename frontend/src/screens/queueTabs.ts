// Shared Queue tab ids + URL/localStorage helpers.
// Used by Queue.tsx (body) and AppShell Sidebar (nested nav / flyout).
//
// Claude 2026-08-29: sidebar collapsible Queue group (queue-sidebar-nest).
// Reason: single source of truth for ?tab= values and persistence keys.
// Troubleshooting: invalid tab → downloads; keep in sync with localStorage.
// Review if: nested paths replace query params.

export const QUEUE_TAB_KEY = "sakms.queue.tab";
export const QUEUE_NAV_EXPANDED_KEY = "sakms.queue.navExpanded";

export const QUEUE_TABS = [
  { id: "downloads", label: "Downloads" },
  { id: "requests", label: "Requests" },
  { id: "calendar", label: "Calendar" },
] as const;

export type QueueTabId = (typeof QUEUE_TABS)[number]["id"];

const TAB_IDS: readonly string[] = QUEUE_TABS.map((t) => t.id);

export function isQueueTabId(v: string | undefined | null): v is QueueTabId {
  return !!v && TAB_IDS.includes(v);
}

/** Last-used tab from localStorage, or downloads. */
export function readStoredQueueTab(): QueueTabId {
  try {
    const raw = localStorage.getItem(QUEUE_TAB_KEY);
    if (isQueueTabId(raw)) return raw;
  } catch {
    /* storage blocked */
  }
  return "downloads";
}

export function writeStoredQueueTab(tab: QueueTabId): void {
  try {
    localStorage.setItem(QUEUE_TAB_KEY, tab);
  } catch {
    /* storage blocked */
  }
}

export function sanitizeQueueTab(
  raw: string | undefined | null,
): QueueTabId {
  return isQueueTabId(raw) ? raw : readStoredQueueTab();
}

export function queueHref(tab: QueueTabId): string {
  return `/queue?tab=${tab}`;
}
