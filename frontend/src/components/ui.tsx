// Shared UI primitives used across the app — the auth screens (setup / login /
// SSO notice) and the workflow/browse screens alike. Styling uses the theme
// tokens from src/index.css (bg-surface, text-fg, text-muted, bg-accent,
// border-border, text-danger) so the palette stays in one place.

import {
  type Component,
  type JSX,
  type Setter,
  For,
  createContext,
  onCleanup,
  onMount,
  splitProps,
  useContext,
} from "solid-js";
import type { Mode } from "../api/discover";

export const inputClass =
  "w-full rounded-md border border-border bg-bg px-3 py-2 text-sm text-fg " +
  "outline-none focus:border-accent";

export const labelClass = "block text-xs font-medium text-muted";

// AuthScreen centers a single auth panel on the page — the setup wizard, the
// login form, and the SSO notice all share this frame.
export function AuthScreen(props: {
  title: string;
  children: JSX.Element;
}): JSX.Element {
  return (
    <div class="flex min-h-screen items-center justify-center p-6">
      <div class="w-full max-w-md rounded-xl border border-border bg-surface p-6 shadow-lg">
        <h2 class="mb-3 text-lg font-semibold text-fg">{props.title}</h2>
        {props.children}
      </div>
    </div>
  );
}

// Field wraps a labeled control.
export function Field(props: {
  label: string;
  children: JSX.Element;
}): JSX.Element {
  return (
    <label class="mb-3 block">
      <span class={labelClass}>{props.label}</span>
      <div class="mt-1">{props.children}</div>
    </label>
  );
}

type ButtonProps = JSX.ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: "primary" | "secondary";
};

// Button defaults to type="button". Every button that is NOT the form's submit
// control must keep that default (or set it explicitly) — a bare <button> in a
// <form> is type="submit", which fires the form's onSubmit. That exact trap
// wiped the break-glass reveal panel in a live incident (see the old frontend's
// note at index.html:2009). Submit buttons pass type="submit" explicitly.
export function Button(props: ButtonProps): JSX.Element {
  const [local, rest] = splitProps(props, ["variant", "class", "type"]);
  const base =
    "rounded-md px-4 py-2 text-sm font-medium transition disabled:opacity-50";
  const variant =
    local.variant === "primary"
      ? "bg-accent text-accent-fg hover:opacity-90"
      : "border border-border bg-surface-2 text-fg hover:opacity-90";
  return (
    <button
      type={local.type ?? "button"}
      class={`${base} ${variant} ${local.class ?? ""}`}
      {...rest}
    />
  );
}

// Muted renders secondary explanatory text.
export function Muted(props: {
  children: JSX.Element;
  class?: string;
}): JSX.Element {
  return (
    <p class={`text-sm text-muted ${props.class ?? ""}`}>{props.children}</p>
  );
}

// ErrorText renders an error line (empty content renders nothing).
export function ErrorText(props: { children: JSX.Element }): JSX.Element {
  return <div class="mt-2 text-sm text-danger">{props.children}</div>;
}

// MODES is the canonical Movies/Series/Adult tab set every workflow/browse
// screen shares — defined once here so a mode is never added in one screen and
// missed in another.
export const MODES: { id: Mode; label: string }[] = [
  { id: "movies", label: "Movies" },
  { id: "series", label: "Series" },
  { id: "adult", label: "Adult" },
];

// TabDef is one entry in a screen's tab set. `id` is an opaque string (Movies/
// Series/Adult for the workflow screens today; Mainstream/Adult for Discover
// and section names for Settings in later waves) — the tab mechanism is
// deliberately not tied to `Mode`.
export type TabDef = { id: string; label: string };

// ScreenTabsRegistration is what a screen hands the app shell so the shell can
// render that screen's tab bar in its one consistent location above the page
// content. `current` is a Solid accessor (NOT a frozen value) so the active-tab
// highlight tracks the screen's own selection signal; `onSelect` sets it.
export type ScreenTabsRegistration = {
  tabs: TabDef[];
  current: () => string;
  onSelect: (id: string) => void;
};

// ScreenTabsContext lets the shell (provider) receive the active screen's tab
// set. The value is the shell's registration setter. When absent (a screen
// rendered standalone, e.g. in its own unit test) registration is a no-op and
// callers fall back to rendering their tab bar inline.
export const ScreenTabsContext =
  createContext<Setter<ScreenTabsRegistration | null>>();

// useScreenTabs registers a screen's tab set with the shell for the lifetime of
// the calling component. Returns true when a shell context was found (so the
// caller renders nothing inline), false when standalone (caller renders inline).
// The cleanup only clears the slot if it still holds THIS registration, so a
// route swap where the new screen registers before the old one disposes stays
// correct regardless of order.
export function useScreenTabs(reg: ScreenTabsRegistration): boolean {
  const setReg = useContext(ScreenTabsContext);
  if (!setReg) return false;
  onMount(() => setReg(reg));
  onCleanup(() => setReg((prev) => (prev === reg ? null : prev)));
  return true;
}

// ScreenTabBar is the generic tab bar: a row of pill buttons over an opaque
// TabDef set. The shell renders it in its consistent location; screens rendered
// standalone (no shell) render it inline via ModeTabs' fallback.
export function ScreenTabBar(props: {
  tabs: TabDef[];
  current: () => string;
  onSelect: (id: string) => void;
  class?: string;
}): JSX.Element {
  return (
    <div class={props.class ?? "mb-4 flex gap-1"}>
      <For each={props.tabs}>
        {(t) => (
          <button
            type="button"
            class="rounded-md px-3 py-1.5 text-sm font-medium transition"
            classList={{
              "bg-accent text-accent-fg": props.current() === t.id,
              "bg-surface-2 text-muted hover:text-fg": props.current() !== t.id,
            }}
            onClick={() => props.onSelect(t.id)}
          >
            {t.label}
          </button>
        )}
      </For>
    </div>
  );
}

// ScreenTabs is the generic tab-registration wrapper over an arbitrary TabDef
// set: it registers the set with the app shell (which then draws the bar in its
// one consistent location) and renders nothing inline, OR — rendered standalone
// with no shell context (a screen's own unit test) — falls back to drawing the
// bar inline. This is the same register-or-fallback pattern ModeTabs applies to
// the fixed Movies/Series/Adult set, hoisted out so any screen with its own tab
// set (Discover's Mainstream/Adult, Settings' section tabs) reuses it instead of
// hand-rolling useScreenTabs + an inline Show/ScreenTabBar fallback.
export function ScreenTabs(props: {
  tabs: TabDef[];
  current: () => string;
  onSelect: (id: string) => void;
  class?: string;
}): JSX.Element {
  const registered = useScreenTabs({
    tabs: props.tabs,
    current: props.current,
    onSelect: props.onSelect,
  });
  if (registered) return null as unknown as JSX.Element;
  return (
    <ScreenTabBar
      tabs={props.tabs}
      current={props.current}
      onSelect={props.onSelect}
      class={props.class}
    />
  );
}

// ModeTabs is the shared Movies/Series/Adult tab set for the workflow/browse
// screens. Inside the app shell it registers its tab set so the shell draws the
// bar in its consistent location and renders nothing inline. Rendered standalone
// (a screen's own unit test, which has no shell context) it falls back to
// drawing the bar inline — preserving the pre-sidebar behavior every existing
// screen test relies on. `current` is a Solid accessor; `class` only affects the
// inline fallback (the shell owns placement).
export function ModeTabs(props: {
  current: () => Mode;
  onSelect: (mode: Mode) => void;
  class?: string;
}): JSX.Element {
  const registered = useScreenTabs({
    tabs: MODES,
    current: props.current,
    onSelect: (id) => props.onSelect(id as Mode),
  });
  if (registered) return null as unknown as JSX.Element;
  return (
    <ScreenTabBar
      tabs={MODES}
      current={props.current}
      onSelect={(id) => props.onSelect(id as Mode)}
      class={props.class}
    />
  );
}

// STATUS_STYLE colors the proposal status pill — pending amber, applied green,
// unmatched/dismissed muted. Shared by every review-queue screen
// (Rename/Purge/Dedup) so the review state reads identically across them.
export const STATUS_STYLE: Record<string, string> = {
  pending: "bg-warn/20 text-warn",
  applied: "bg-ok/20 text-ok",
  unmatched: "bg-surface-2 text-muted",
  dismissed: "bg-surface-2 text-muted",
};

// StatusPill renders one proposal's lifecycle state as a small colored pill.
export const StatusPill: Component<{ status: string }> = (props) => (
  <span
    class="inline-block rounded-full px-2 py-0.5 text-[11px] font-medium"
    classList={{
      [STATUS_STYLE[props.status] ?? "bg-surface-2 text-muted"]: true,
    }}
  >
    {props.status}
  </span>
);

// yearOf pulls the leading 4-digit year from a TMDB/TPDB date string
// ("YYYY-.."), as a number — undefined when there is no parseable year. Shared
// by Discover (display) and Rename's Re-pick (the numeric year sent with a new
// match).
export function yearOf(date: string): number | undefined {
  const y = date && date.length >= 4 ? parseInt(date.slice(0, 4), 10) : NaN;
  return Number.isFinite(y) ? y : undefined;
}
