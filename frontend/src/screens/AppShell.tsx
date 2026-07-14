// The authed app shell. Past auth it renders a LEFT SIDEBAR (Discover / Grabs /
// Rename / Purge / Dedup / Tag / Settings, each an icon + label) beside the
// client-side router; the landing view is Discover. The sidebar collapses to
// icon-only and persists that choice in localStorage. The router must never
// claim an /api/* path (see APP_ROUTES).

import {
  type Component,
  type JSX,
  For,
  Show,
  createSignal,
} from "solid-js";
import { A, Route, Router } from "@solidjs/router";
import {
  Button,
  ErrorText,
  Muted,
  ScreenTabBar,
  ScreenTabsContext,
  type ScreenTabsRegistration,
} from "../components/ui";
import { Discover } from "./Discover";
import { Grabs } from "./Grabs";
import { Rename } from "./Rename";
import { Purge } from "./Purge";
import { Dedup } from "./Dedup";
import { Tag } from "./Tag";
import { Settings } from "./Settings";

// APP_ROUTES is the exhaustive list of client-side route patterns the router
// serves. Guardrail #2 / requirement #7: the router must NEVER claim any
// /api/* path (the OIDC callback /api/auth/oidc/callback is a real server
// route). A unit test asserts none of these start with "/api".
export const APP_ROUTES = ["/", "/discover", "/grabs", "/rename", "/purge", "/dedup", "/tag", "/settings"] as const;

// SIDEBAR_COLLAPSED_KEY persists the sidebar's collapsed/expanded choice across
// reloads. A single boolean is enough ("true" = collapsed).
export const SIDEBAR_COLLAPSED_KEY = "sakms.sidebar.collapsed";

// createPersistedBool is a boolean signal mirrored to localStorage. Reads are
// guarded so a blocked/absent storage (private mode, SSR) degrades to the
// fallback rather than throwing.
export function createPersistedBool(
  key: string,
  fallback: boolean,
): [() => boolean, (v: boolean) => void] {
  const read = (): boolean => {
    try {
      const raw = localStorage.getItem(key);
      return raw === null ? fallback : raw === "true";
    } catch {
      return fallback;
    }
  };
  const [value, setValue] = createSignal(read());
  const set = (v: boolean) => {
    setValue(v);
    try {
      localStorage.setItem(key, String(v));
    } catch {
      /* storage unavailable — keep the in-memory value only */
    }
  };
  return [value, set];
}

// ---- Inline icons (no icon-library dependency) --------------------------------
// Simple 20x20 stroke icons drawn in currentColor so they inherit link color.

const svgProps = {
  width: "20",
  height: "20",
  viewBox: "0 0 24 24",
  fill: "none",
  stroke: "currentColor",
  "stroke-width": "1.8",
  "stroke-linecap": "round" as const,
  "stroke-linejoin": "round" as const,
  "aria-hidden": true,
};

const IconDiscover: Component = () => (
  <svg {...svgProps}>
    <circle cx="12" cy="12" r="9" />
    <polygon points="15.5 8.5 11 11 8.5 15.5 13 13" />
  </svg>
);
const IconGrabs: Component = () => (
  <svg {...svgProps}>
    <path d="M12 3v12" />
    <path d="m7 10 5 5 5-5" />
    <path d="M4 20h16" />
  </svg>
);
const IconRename: Component = () => (
  <svg {...svgProps}>
    <path d="M12 20h9" />
    <path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4Z" />
  </svg>
);
const IconPurge: Component = () => (
  <svg {...svgProps}>
    <path d="M3 6h18" />
    <path d="M8 6V4h8v2" />
    <path d="M6 6v14a1 1 0 0 0 1 1h10a1 1 0 0 0 1-1V6" />
    <path d="M10 11v6M14 11v6" />
  </svg>
);
const IconDedup: Component = () => (
  <svg {...svgProps}>
    <rect x="9" y="9" width="12" height="12" rx="2" />
    <path d="M5 15H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1v1" />
  </svg>
);
const IconTag: Component = () => (
  <svg {...svgProps}>
    <path d="M20.5 12.5 12 21l-8-8V4h9Z" />
    <circle cx="7.5" cy="7.5" r="1.2" />
  </svg>
);
const IconSettings: Component = () => (
  <svg {...svgProps}>
    <circle cx="12" cy="12" r="3" />
    <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1Z" />
  </svg>
);
const IconChevron: Component<{ collapsed: boolean }> = (props) => (
  <svg {...svgProps}>
    <path d={props.collapsed ? "m9 6 6 6-6 6" : "m15 6-6 6 6 6"} />
  </svg>
);

type NavItem = { href: string; label: string; icon: Component };

const NAV_ITEMS: NavItem[] = [
  { href: "/discover", label: "Discover", icon: IconDiscover },
  { href: "/grabs", label: "Grabs", icon: IconGrabs },
  { href: "/rename", label: "Rename", icon: IconRename },
  { href: "/purge", label: "Purge", icon: IconPurge },
  { href: "/dedup", label: "Dedup", icon: IconDedup },
  { href: "/tag", label: "Tag", icon: IconTag },
  { href: "/settings", label: "Settings", icon: IconSettings },
];

// Sidebar is the presentational left nav. `collapsed` is an accessor so the
// caller owns (and persists) the state; `onToggle` flips it. Collapsed hides
// the labels and narrows the column while keeping icons + native `title`
// tooltips. Must be rendered inside a <Router> — <A> needs router context.
//
// bg-fixed here and on the header below is load-bearing, not decoration:
// `background-attachment: fixed` anchors each element's gradient to the
// VIEWPORT rather than its own box, so both panels sample the same
// continuous diagonal field instead of two independently-scaled gradients.
// Without it, the sidebar's gradient runs its own top-left→bottom-right
// across ~192px while the header's runs across the full remaining width —
// different scales meeting at their shared corner reads as a visible seam,
// not the single blended surface this is supposed to look like.
export const Sidebar: Component<{
  collapsed: () => boolean;
  onToggle: () => void;
}> = (props) => (
  <nav
    class="z-10 flex shrink-0 flex-col gap-1 bg-fixed bg-gradient-to-br from-chrome to-chrome-2 p-2 shadow-xl transition-all"
    classList={{ "w-48": !props.collapsed(), "w-14": props.collapsed() }}
    aria-label="Primary"
  >
    <button
      type="button"
      onClick={props.onToggle}
      class="mb-2 flex items-center rounded-md px-2 py-2 text-chrome-fg/60 transition hover:text-chrome-fg"
      title={props.collapsed() ? "Expand sidebar" : "Collapse sidebar"}
      aria-label={props.collapsed() ? "Expand sidebar" : "Collapse sidebar"}
      aria-expanded={!props.collapsed()}
    >
      <IconChevron collapsed={props.collapsed()} />
    </button>
    <For each={NAV_ITEMS}>
      {(item) => (
        <A
          href={item.href}
          title={item.label}
          class="flex items-center gap-3 rounded-md px-2 py-2 text-sm font-medium text-chrome-fg/60 transition hover:bg-white/10 hover:text-chrome-fg"
          activeClass="!bg-white/10 !text-chrome-fg"
        >
          <span class="flex shrink-0 items-center">{item.icon({})}</span>
          <Show when={!props.collapsed()}>
            <span>{item.label}</span>
          </Show>
        </A>
      )}
    </For>
  </nav>
);

const NotFound: Component = () => (
  <div class="rounded-xl border border-border bg-surface p-6">
    <h1 class="text-xl font-semibold text-fg">Not found</h1>
    <Muted class="mt-2">No such view. This is the SPA catch-all fallback.</Muted>
  </div>
);

export const AppShell: Component<{
  noneMode: boolean;
  connectionsSetupPending: boolean;
  onLoggedOut: () => void;
}> = (props) => {
  const [logoutError, setLogoutError] = createSignal("");
  const [collapsed, setCollapsed] = createPersistedBool(
    SIDEBAR_COLLAPSED_KEY,
    false,
  );

  const logout = async () => {
    setLogoutError("");
    try {
      await fetch("/api/auth/logout", { method: "POST" });
      props.onLoggedOut();
    } catch (err) {
      setLogoutError((err as Error).message);
    }
  };

  // ShellRoot is the Router root — the sidebar + top bar + the active screen's
  // tab bar above whatever route is active. Defined inside AppShell so it closes
  // over logout/banners/collapsed. Being inside <Router> is what gives <A> its
  // active-link context. The tab bar slot is driven by whichever screen is
  // mounted: a screen registers its own tab set via ScreenTabsContext, and the
  // shell renders it here in one consistent location (empty when a screen
  // registers nothing, e.g. Settings today).
  const ShellRoot: Component<{ children?: JSX.Element }> = (rootProps) => {
    const [tabReg, setTabReg] = createSignal<ScreenTabsRegistration | null>(null);
    return (
      <ScreenTabsContext.Provider value={setTabReg}>
        <div class="flex min-h-screen">
          <Sidebar collapsed={collapsed} onToggle={() => setCollapsed(!collapsed())} />
          <div class="flex min-w-0 flex-1 flex-col">
            <header class="z-10 flex items-center gap-4 bg-fixed bg-gradient-to-br from-chrome to-chrome-2 px-6 py-3 shadow-xl">
              <img src="/favicon.svg" alt="" class="h-6 w-6 shrink-0" />
              <span class="font-semibold text-chrome-fg">SAK Media Server</span>
              <div class="ml-auto">
                <Button onClick={logout}>Log out</Button>
              </div>
            </header>

            <Show when={props.noneMode}>
              <div class="border-b border-border bg-surface-2 px-6 py-2">
                <span class="text-sm text-danger">
                  Authentication is disabled for this instance — it and every
                  connected service is reachable by anyone who can reach it.
                  Switch to a different mode in Settings to fix this.
                </span>
              </div>
            </Show>

            <Show when={props.connectionsSetupPending}>
              <div class="border-b border-border bg-surface-2 px-6 py-2">
                <span class="text-sm text-muted">
                  First-run connections setup hasn't been dismissed yet — the
                  setup wizard lands in a later wave.
                </span>
              </div>
            </Show>

            {/* bg-fixed anchors the wallpaper to the viewport, not this box, so
                it doesn't scroll with the content beneath it — same technique
                as the sidebar/header gradient above, just applied to a
                background-image instead of a gradient. The two source images
                are pre-composed per sidebar width (collapsed-56 / expanded-192)
                so the ticket art's decorative elements land in the same
                on-screen position regardless of how much room the sidebar
                takes up next to this column; swap on `collapsed()` accordingly. */}
            <main
              class="min-w-0 flex-1 bg-fixed bg-cover bg-center p-6"
              style={{
                "background-image": `url(${collapsed() ? "/wallpaper-collapsed.webp" : "/wallpaper-expanded.webp"})`,
              }}
            >
              {logoutError() && <ErrorText>{logoutError()}</ErrorText>}
              <Show when={tabReg()}>
                {(reg) => (
                  <ScreenTabBar
                    tabs={reg().tabs}
                    current={reg().current}
                    onSelect={reg().onSelect}
                  />
                )}
              </Show>
              {rootProps.children}
            </main>
          </div>
        </div>
      </ScreenTabsContext.Provider>
    );
  };

  return (
    <Router root={ShellRoot}>
      <Route path="/" component={Discover} />
      <Route path="/discover" component={Discover} />
      <Route path="/grabs" component={Grabs} />
      <Route path="/rename" component={Rename} />
      <Route path="/purge" component={Purge} />
      <Route path="/dedup" component={Dedup} />
      <Route path="/tag" component={Tag} />
      <Route
        path="/settings"
        component={() => <Settings onReboot={props.onLoggedOut} />}
      />
      <Route path="*" component={NotFound} />
    </Router>
  );
};
