// The authed app shell. Past auth it renders a LEFT SIDEBAR (Dashboard /
// Discover / Library / Queue / Organize / Tag / Collections / Settings, each
// an icon + label) beside the client-side router; the landing view is
// Discover. The sidebar collapses to icon-only and persists that choice in
// localStorage. The router must never claim an /api/* path (see APP_ROUTES).
//
// LAYOUT (2026-07-14 mobile-responsive pass): the shell root is a fixed-height
// flex row (`h-screen overflow-hidden`) with exactly one scroll region — the
// <main> content column. The sidebar and header are never in that scroll
// region, so neither scrolls away with page content; on desktop (md+) the
// sidebar is a normal static flex column, on mobile it's a `fixed` off-canvas
// drawer (translate-x-full when closed) toggled by a hamburger button in the
// header, with a tap-to-close backdrop. Collapse-to-icons (desktop) and
// open/closed (mobile) are two independent, independently-persisted states —
// collapsing icon-width on a full-width mobile drawer wouldn't read as
// anything sensible, so the collapse toggle itself is hidden below md.

import {
  type Component,
  type JSX,
  For,
  Show,
  createEffect,
  createResource,
  createSignal,
  onCleanup,
} from "solid-js";
import { A, Route, Router, useLocation } from "@solidjs/router";
import {
  AdultModeContext,
  Button,
  ErrorText,
  LockGlyph,
  Muted,
  ScreenTabBar,
  ScreenTabsContext,
  SectionLockContext,
  SectionLockOverlay,
  type SectionLockControl,
  type ScreenTabsRegistration,
  useSectionLock,
} from "../components/ui";
import { fetchAdultModeEnabled } from "../api/settings";
import { setOnSectionLocked } from "../api/client";
import {
  fetchSectionLockStatus,
  LOCKABLE_TAB_SECTIONS,
  sectionLabel,
} from "../api/sectionLock";
import { Dashboard } from "./Dashboard";
import { Discover } from "./Discover";
import { Library } from "./Library";
import { Queue } from "./Queue";
import { Organize } from "./Organize";
import { Tag } from "./Tag";
import { Collections } from "./Collections";
import { Settings } from "./Settings";
import { BrowserNotifications } from "../components/BrowserNotifications";

// APP_ROUTES is the exhaustive list of client-side route patterns the router
// serves. Guardrail #2 / requirement #7: the router must NEVER claim any
// /api/* path (the OIDC callback /api/auth/oidc/callback is a real server
// route). A unit test asserts none of these start with "/api".
export const APP_ROUTES = ["/dashboard", "/", "/discover", "/library", "/queue", "/organize", "/tag", "/collections", "/settings"] as const;

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

// createPersistedString is the string sibling of createPersistedBool above — a
// string signal mirrored to localStorage, same guarded try/catch shape. Reads
// degrade to the fallback when storage is blocked/absent (private mode, SSR) or
// the key is unset. A caller that only accepts a fixed set of values (e.g.
// Organize's active-tab id) validates the returned string itself; this helper
// only guards storage access, not the value's domain.
export function createPersistedString(
  key: string,
  fallback: string,
): [() => string, (v: string) => void] {
  const read = (): string => {
    try {
      const raw = localStorage.getItem(key);
      return raw === null ? fallback : raw;
    } catch {
      return fallback;
    }
  };
  const [value, setValue] = createSignal(read());
  const set = (v: string) => {
    setValue(v);
    try {
      localStorage.setItem(key, v);
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

const IconDashboard: Component = () => (
  <svg {...svgProps}>
    <rect x="3" y="3" width="7" height="7" rx="1" />
    <rect x="14" y="3" width="7" height="7" rx="1" />
    <rect x="3" y="14" width="7" height="7" rx="1" />
    <rect x="14" y="14" width="7" height="7" rx="1" />
  </svg>
);
const IconDiscover: Component = () => (
  <svg {...svgProps}>
    <circle cx="12" cy="12" r="9" />
    <polygon points="15.5 8.5 11 11 8.5 15.5 13 13" />
  </svg>
);
const IconLibrary: Component = () => (
  <svg {...svgProps}>
    <rect x="3" y="4" width="4" height="16" rx="1" />
    <rect x="9" y="4" width="4" height="16" rx="1" />
    <path d="m16.5 5.5 3.5 1-3.5 13-3.5-1z" />
  </svg>
);
const IconQueue: Component = () => (
  <svg {...svgProps}>
    <path d="M12 3v10" />
    <path d="m8 11 4 4 4-4" />
    <path d="M4 17v2a1 1 0 0 0 1 1h14a1 1 0 0 0 1-1v-2" />
  </svg>
);
const IconRename: Component = () => (
  <svg {...svgProps}>
    <path d="M12 20h9" />
    <path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4Z" />
  </svg>
);
const IconTag: Component = () => (
  <svg {...svgProps}>
    <path d="M20.5 12.5 12 21l-8-8V4h9Z" />
    <circle cx="7.5" cy="7.5" r="1.2" />
  </svg>
);
const IconCollections: Component = () => (
  <svg {...svgProps}>
    <rect x="2" y="7" width="16" height="13" rx="2" />
    <path d="M6 7V5a2 2 0 0 1 2-2h10a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2h-2" />
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
const IconMenu: Component = () => (
  <svg {...svgProps}>
    <path d="M4 6h16" />
    <path d="M4 12h16" />
    <path d="M4 18h16" />
  </svg>
);

type NavItem = { href: string; label: string; icon: Component };

// NAV_ITEMS is EXPORTED (it was module-private until the section PIN lock) so
// the nav-drift test can set-compare its hrefs against LOCKABLE_TAB_SECTIONS
// in src/api/sectionLock.ts. Adding a sidebar tab without adding the matching
// lockable section id fails that test — see sectionLock.nav.test.ts and the
// note on LOCKABLE_TAB_SECTIONS for why the two lists are authored
// independently rather than derived one from the other.
export const NAV_ITEMS: NavItem[] = [
  { href: "/dashboard", label: "Dashboard", icon: IconDashboard },
  { href: "/discover", label: "Discover", icon: IconDiscover },
  { href: "/library", label: "Library", icon: IconLibrary },
  { href: "/queue", label: "Queue", icon: IconQueue },
  { href: "/organize", label: "Organize", icon: IconRename },
  { href: "/tag", label: "Tag", icon: IconTag },
  { href: "/collections", label: "Collections", icon: IconCollections },
  { href: "/settings", label: "Settings", icon: IconSettings },
];

// Sidebar is the presentational left nav. `collapsed` is an accessor so the
// caller owns (and persists) the state; `onToggle` flips it. Collapsed hides
// the labels and narrows the column while keeping icons + native `title`
// tooltips. Must be rendered inside a <Router> — <A> needs router context.
//
// `mobileOpen`/`onCloseMobile` are optional (default: closed, no-op close) so
// existing standalone-Sidebar test harnesses that don't wire them keep
// working unchanged. On mobile the nav is a `fixed` off-canvas drawer
// (translate-x-full when closed); at md+ it reverts to a normal static flex
// column and the translate classes are neutralized. Clicking a nav link
// closes the mobile drawer (harmless no-op on desktop, where onCloseMobile is
// still called but nothing observes it).
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
  mobileOpen?: () => boolean;
  onCloseMobile?: () => void;
}> = (props) => {
  const mobileOpen = () => props.mobileOpen?.() ?? false;
  const closeMobile = () => props.onCloseMobile?.();
  // Locked tabs stay VISIBLE and stay clickable — clicking navigates
  // normally and the content area renders the PIN overlay. Only the badge is
  // added here. With no SectionLockContext Provider (a standalone Sidebar
  // test harness) isLocked is always false, so nothing changes for them.
  const lock = useSectionLock();

  return (
    <nav
      class="fixed inset-y-0 left-0 z-40 flex w-64 shrink-0 flex-col gap-1 overflow-y-auto bg-fixed bg-gradient-to-br from-chrome to-chrome-2 p-2 shadow-xl transition-transform duration-200 md:static md:translate-x-0 md:transition-[width]"
      classList={{
        "translate-x-0": mobileOpen(),
        "-translate-x-full": !mobileOpen(),
        "md:w-48": !props.collapsed(),
        "md:w-14": props.collapsed(),
      }}
      aria-label="Primary"
    >
      <button
        type="button"
        onClick={props.onToggle}
        class="mb-2 hidden items-center rounded-md px-2 py-2 text-chrome-fg/60 transition hover:text-chrome-fg md:flex"
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
            onClick={closeMobile}
            class="flex items-center gap-3 rounded-md px-2 py-2 text-sm font-medium text-chrome-fg/60 transition hover:bg-white/10 hover:text-chrome-fg"
            activeClass="!bg-white/10 !text-chrome-fg"
          >
            <span class="flex shrink-0 items-center">{item.icon({})}</span>
            <Show when={!props.collapsed()}>
              <span>{item.label}</span>
            </Show>
            <Show when={lock.isLocked(item.href.slice(1))}>
              <span
                class="ml-auto flex shrink-0 items-center text-chrome-fg/70"
                title={`${item.label} is locked`}
                aria-label={`${item.label} is locked`}
              >
                <LockGlyph />
              </span>
            </Show>
          </A>
        )}
      </For>
    </nav>
  );
};

// createSectionLockControl builds the SectionLockContext value: one
// GET /api/section-lock/status resource fetched at boot plus the paired
// refetch, mirroring how ShellRoot backs AdultModeContext.
//
// Exported (rather than inlined into ShellRoot) so the unlock/overlay test can
// exercise THIS control rather than a re-implementation of it — an
// isLocked() that got the three-way conjunction wrong would otherwise pass a
// test written against a hand-built stub.
//
// Must be called from a component body: createResource needs a reactive owner.
export function createSectionLockControl(): SectionLockControl {
  const [status, { refetch }] = createResource(fetchSectionLockStatus);

  // ready is true once the INITIAL fetch has SETTLED, successfully or not — a
  // later "refreshing" still counts (a previous value is in hand), only
  // "unresolved"/"pending" are the not-yet-known window. Consumers need this
  // to tell "we don't know yet" apart from "enforcement is off": both read as
  // enforcementAvailable() === false and only the second is worth a banner.
  const ready = () =>
    status.state !== "unresolved" && status.state !== "pending";
  const failed = () => status.state === "errored";

  // value() is what every accessor below reads INSTEAD of status(). A Solid
  // resource re-throws the fetcher's error on read (by design, for
  // ErrorBoundary), and these accessors run mid-render in the sidebar and the
  // content area — so a failed status fetch would throw out of the shell
  // rather than degrade. Reading through this guard is what actually makes
  // ShellRoot's "loading/error resolves to NOTHING LOCKED" note true.
  const value = () => (failed() ? undefined : status());

  return {
    ready,
    error: failed,
    // Three facts, folded here so no consumer can get the conjunction wrong.
    // `unlocked` is what makes one successful unlock clear every overlay at
    // once; `enforcementAvailable` is false on an instance where the backend
    // still REPORTS a stored locked set but gates nothing (disarmed by env
    // var, or auth mode "none"), where an overlay would hide content the
    // server serves happily.
    isLocked: (section) => {
      const s = value();
      if (!s || !s.enforcementAvailable || s.unlocked) return false;
      return s.lockedSections.includes(section);
    },
    lockedSections: () => value()?.lockedSections ?? [],
    pinSet: () => value()?.pinSet ?? false,
    unlocked: () => value()?.unlocked ?? false,
    enforcementAvailable: () => value()?.enforcementAvailable ?? false,
    refetch: () => void refetch(),
  };
}

// sectionForPath maps a client route to the lockable section id that gates
// it, or null for a route no section gates (the NotFound catch-all).
//
// "/" MAPS TO "discover" and that is the one non-obvious entry: "/" is in
// APP_ROUTES and renders Discover, but it is NOT in NAV_ITEMS (the sidebar
// links /discover), so a bare slice(1) would yield "" and leave the landing
// view ungated while /discover itself was overlaid.
export function sectionForPath(pathname: string): string | null {
  const id = (pathname === "/" ? "/discover" : pathname).slice(1);
  return (LOCKABLE_TAB_SECTIONS as readonly string[]).includes(id) ? id : null;
}

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
  const [mobileNavOpen, setMobileNavOpen] = createSignal(false);

  // Lock background scroll while the mobile drawer is open — without this,
  // touch-scrolling the backdrop scrolls the page underneath it. Guarded the
  // same way createPersistedBool guards localStorage: this only ever runs in
  // a real browser (or jsdom under test, which also has document.body), but
  // there's no reason to let a missing DOM API throw here either.
  createEffect(() => {
    try {
      document.body.style.overflow = mobileNavOpen() ? "hidden" : "";
    } catch {
      /* no document — nothing to lock */
    }
  });
  onCleanup(() => {
    try {
      document.body.style.overflow = "";
    } catch {
      /* no document — nothing to restore */
    }
  });

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
    // adultModeResource is fetched once here (not re-run on a settings
    // toggle — boot only runs once per auth session, see App.tsx:29-31), so
    // AdultModeSection's (Global.tsx) toggle handler calls the paired
    // refetch after a successful PUT to propagate the change everywhere else
    // without a page reload. Its own loading/error state defaults to false —
    // fail-safe toward hidden, per the plan's Risk table — DIFFERENT from
    // AdultModeContext's no-Provider default of true (see that context's doc
    // comment in ui.tsx for why these are two different scenarios).
    const [adultModeResource, { refetch: refetchAdultMode }] = createResource(
      fetchAdultModeEnabled,
    );
    const adultModeControl = {
      enabled: () => adultModeResource() ?? false,
      refetch: () => void refetchAdultMode(),
    };

    // The lock resource mirrors adultModeResource exactly: fetched once at
    // boot, with a paired refetch called after a successful config PUT
    // (SectionLock.tsx) and after a successful unlock (SectionLockOverlay).
    // Its own loading/error state resolves to NOTHING LOCKED — presentation
    // fail-open, because the middleware gate is the real boundary and fails
    // closed independently. An overlay drawn over content the server would
    // have served is a worse failure here than a missing one.
    const sectionLockControl = createSectionLockControl();

    // A 403 section_locked mid-session (the 30-minute non-sliding ticket
    // expiring under an open tab) refetches the status, which raises the
    // overlay instead of failing silently. Same registration shape as
    // setOnSessionExpired in App.tsx — and pointedly NOT the same effect:
    // this one never reboots the SPA, the operator stays logged in.
    setOnSectionLocked(() => sectionLockControl.refetch());
    onCleanup(() => setOnSectionLocked(null));

    const location = useLocation();
    const lockedSection = () => {
      const id = sectionForPath(location.pathname);
      return id && sectionLockControl.isLocked(id) ? id : null;
    };

    return (
      <AdultModeContext.Provider value={adultModeControl}>
        <SectionLockContext.Provider value={sectionLockControl}>
        <ScreenTabsContext.Provider value={setTabReg}>
          <div class="flex h-screen overflow-hidden">
            {/* No visible UI; lives here (persistent shell root, not a swapped
                <Route>) so the notifications stream stays open across in-app
                navigation. */}
            <BrowserNotifications />
            <Show when={mobileNavOpen()}>
              <div
                class="fixed inset-0 z-30 bg-black/50 md:hidden"
                aria-hidden="true"
                onClick={() => setMobileNavOpen(false)}
              />
            </Show>
            <Sidebar
              collapsed={collapsed}
              onToggle={() => setCollapsed(!collapsed())}
              mobileOpen={mobileNavOpen}
              onCloseMobile={() => setMobileNavOpen(false)}
            />
            <div class="flex min-w-0 flex-1 flex-col overflow-hidden">
              <header class="z-10 flex shrink-0 items-center gap-4 bg-fixed bg-gradient-to-br from-chrome to-chrome-2 px-4 py-3 shadow-xl sm:px-6">
                <button
                  type="button"
                  onClick={() => setMobileNavOpen(true)}
                  class="flex items-center rounded-md p-1 text-chrome-fg/80 transition hover:text-chrome-fg md:hidden"
                  aria-label="Open navigation"
                >
                  <IconMenu />
                </button>
                <img src="/favicon.svg" alt="" class="h-6 w-6 shrink-0" />
                <span class="truncate font-semibold text-chrome-fg">SAK Media Server</span>
                <div class="ml-auto">
                  <Button onClick={logout}>Log out</Button>
                </div>
              </header>

              <Show when={props.noneMode}>
                <div class="shrink-0 border-b border-border bg-surface-2 px-4 py-2 sm:px-6">
                  <span class="text-sm text-danger">
                    Authentication is disabled for this instance — it and every
                    connected service is reachable by anyone who can reach it.
                    Switch to a different mode in Settings to fix this.
                  </span>
                </div>
              </Show>

              <Show when={props.connectionsSetupPending}>
                <div class="shrink-0 border-b border-border bg-surface-2 px-4 py-2 sm:px-6">
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
                class="min-w-0 flex-1 overflow-y-auto bg-fixed bg-cover bg-center p-4 sm:p-6"
                style={{
                  "background-image": `url(${collapsed() ? "/wallpaper-collapsed.webp" : "/wallpaper-expanded.webp"})`,
                }}
              >
                {logoutError() && <ErrorText>{logoutError()}</ErrorText>}
                {/* A locked tab replaces the screen — including its tab bar,
                    which is why this Show wraps both. The screen itself is
                    never mounted, so it fires none of its own requests (each
                    of which the backend would deny anyway). */}
                <Show
                  when={lockedSection()}
                  fallback={
                    <>
                      <Show when={tabReg()}>
                        {(reg) => (
                          <ScreenTabBar
                            tabs={reg().tabs}
                            current={reg().current}
                            onSelect={reg().onSelect}
                            trailing={reg().trailing}
                          />
                        )}
                      </Show>
                      {rootProps.children}
                    </>
                  }
                >
                  {(section) => (
                    <SectionLockOverlay label={sectionLabel(section())} />
                  )}
                </Show>
              </main>
            </div>
          </div>
        </ScreenTabsContext.Provider>
        </SectionLockContext.Provider>
      </AdultModeContext.Provider>
    );
  };

  return (
    <Router root={ShellRoot}>
      <Route path="/" component={Discover} />
      <Route path="/dashboard" component={Dashboard} />
      <Route path="/discover" component={Discover} />
      <Route path="/library" component={Library} />
      <Route path="/queue" component={Queue} />
      <Route path="/organize" component={Organize} />
      <Route path="/tag" component={Tag} />
      <Route path="/collections" component={Collections} />
      <Route
        path="/settings"
        component={() => <Settings onReboot={props.onLoggedOut} />}
      />
      <Route path="*" component={NotFound} />
    </Router>
  );
};
