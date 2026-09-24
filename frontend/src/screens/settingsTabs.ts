// Shared Settings screen ids + URL/localStorage helpers.
// Used by settings/index.tsx (body) and AppShell Sidebar (nested nav / flyout).
//
// Claude 2026-09-24: Settings is a sidebar group, same pattern as Organize/Queue.
// Reason: one 9-tab page was not cohesive with Library/Discover/Organize/Queue.
// Troubleshooting: invalid ?tab= → last-used or roots; keep in sync with localStorage.
// Review if: nested paths replace query params.

export const SETTINGS_TAB_KEY = "sakms.settings.tab";
export const SETTINGS_NAV_EXPANDED_KEY = "sakms.settings.navExpanded";

export const SETTINGS_TABS = [
  { id: "roots", label: "Roots" },
  { id: "metadata", label: "Metadata" },
  { id: "quality", label: "Quality" },
  { id: "scans", label: "Organize scans" },
  { id: "download", label: "Download" },
  { id: "discover", label: "Discover UI" },
  { id: "ai", label: "AI" },
  { id: "auth", label: "Auth" },
  { id: "notifications", label: "Notifications" },
  { id: "nodes", label: "Nodes" },
  { id: "connections", label: "Connections" },
  { id: "global", label: "Global" },
] as const;

export type SettingsTabId = (typeof SETTINGS_TABS)[number]["id"];

const TAB_IDS: readonly string[] = SETTINGS_TABS.map((t) => t.id);

// LEGACY_SETTINGS_TABS maps the old in-page section ids onto the finer screens
// so a bookmark or leftover localStorage value still lands somewhere real.
const LEGACY_SETTINGS_TABS: Record<string, SettingsTabId> = {
  library: "roots",
  organize: "scans",
  ui: "discover",
  webhooks: "notifications",
  advanced: "global",
};

export function isSettingsTabId(
  v: string | undefined | null,
): v is SettingsTabId {
  return !!v && TAB_IDS.includes(v);
}

function resolveSettingsTab(raw: string | undefined | null): SettingsTabId | null {
  if (isSettingsTabId(raw)) return raw;
  if (raw && raw in LEGACY_SETTINGS_TABS) return LEGACY_SETTINGS_TABS[raw]!;
  return null;
}

/** Last-used tab from localStorage, or roots. */
export function readStoredSettingsTab(): SettingsTabId {
  try {
    const raw = localStorage.getItem(SETTINGS_TAB_KEY);
    const resolved = resolveSettingsTab(raw);
    if (resolved) return resolved;
  } catch {
    /* storage blocked */
  }
  return "roots";
}

export function writeStoredSettingsTab(tab: SettingsTabId): void {
  try {
    localStorage.setItem(SETTINGS_TAB_KEY, tab);
  } catch {
    /* storage unavailable */
  }
}

export function sanitizeSettingsTab(
  raw: string | undefined | null,
): SettingsTabId {
  return resolveSettingsTab(raw) ?? readStoredSettingsTab();
}

export function settingsHref(tab: SettingsTabId): string {
  return `/settings?tab=${tab}`;
}

export function settingsTabLabel(tab: SettingsTabId): string {
  return SETTINGS_TABS.find((t) => t.id === tab)?.label ?? tab;
}
