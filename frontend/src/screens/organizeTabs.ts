// Shared Organize workflow tab ids + URL/localStorage helpers.
// Used by Organize.tsx (body) and AppShell Sidebar (nested nav / flyout).
//
// Claude 2026-08-05: sidebar collapsible Organize group (deep-interview-organize-nav-collapse).
// Reason: single source of truth for ?tab= values and persistence keys.
// Troubleshooting: invalid tab → rename; keep in sync with localStorage.
// Review if: nested paths replace query params.

export const ORGANIZE_TAB_KEY = "sakms.organize.tab";
export const ORGANIZE_NAV_EXPANDED_KEY = "sakms.organize.navExpanded";

export const ORGANIZE_WORKFLOWS = [
  { id: "rename", label: "Rename" },
  { id: "purge", label: "Clean-up" },
  { id: "dedup", label: "Dedup" },
  // Claude 2026-08-27: Browse tab (file manager).
  // Reason: confirm-then-mutate rename/move/delete under the mounted roots;
  //   id is "browse" (URL ?tab=browse), not "files".
  // Troubleshooting: invalid tab still falls back to rename.
  // Review if: nested paths replace query params.
  { id: "browse", label: "Browse" },
] as const;

export type OrganizeTabId = (typeof ORGANIZE_WORKFLOWS)[number]["id"];

const TAB_IDS: readonly string[] = ORGANIZE_WORKFLOWS.map((t) => t.id);

export function isOrganizeTabId(v: string | undefined | null): v is OrganizeTabId {
  return !!v && TAB_IDS.includes(v);
}

/** Last-used tab from localStorage, or rename. */
export function readStoredOrganizeTab(): OrganizeTabId {
  try {
    const raw = localStorage.getItem(ORGANIZE_TAB_KEY);
    if (isOrganizeTabId(raw)) return raw;
  } catch {
    /* storage blocked */
  }
  return "rename";
}

export function writeStoredOrganizeTab(tab: OrganizeTabId): void {
  try {
    localStorage.setItem(ORGANIZE_TAB_KEY, tab);
  } catch {
    /* storage blocked */
  }
}

export function sanitizeOrganizeTab(
  raw: string | undefined | null,
): OrganizeTabId {
  return isOrganizeTabId(raw) ? raw : readStoredOrganizeTab();
}

export function organizeHref(tab: OrganizeTabId): string {
  return `/organize?tab=${tab}`;
}
