// Shared UI primitives used across the app — the auth screens (setup / login /
// SSO notice) and the workflow/browse screens alike. Styling uses the theme
// tokens from src/index.css (bg-surface, text-fg, text-muted, bg-accent,
// border-border, text-danger) so the palette stays in one place.

import {
  type Component,
  type JSX,
  type Setter,
  For,
  Show,
  createContext,
  createSignal,
  onCleanup,
  onMount,
  splitProps,
  useContext,
} from "solid-js";
import type { Mode } from "../api/discover";
import type { ApplyBatchResponse } from "@dto";

export const inputClass =
  "w-full truncate rounded-md border border-border bg-bg px-3 py-2 text-sm text-fg " +
  "outline-none focus:border-accent";

export const labelClass = "block text-xs font-medium text-muted";

// AuthScreen centers a single auth panel on the page — the setup wizard, the
// login form, and the SSO notice all share this frame. The full logo (not
// just the favicon glyph) gets its one prominent moment here — this is the
// only screen an operator sees before the persistent app shell (with its own
// small header) takes over.
export function AuthScreen(props: {
  title: string;
  children: JSX.Element;
}): JSX.Element {
  return (
    <div class="flex min-h-screen items-center justify-center p-6">
      <div class="w-full max-w-md rounded-xl border border-border bg-surface p-6 shadow-2xl">
        <img src="/logo.svg" alt="SAK Media Server" class="mx-auto mb-4 w-full max-w-xs" />
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

// Card is the fieldset frame every settings-style panel shares (originally
// Settings.tsx-local; hoisted here so other screens, e.g. the admin slider
// editor, can reuse the same frame instead of duplicating its styling).
// Card is the bordered panel frame shared by Settings and Discover screens.
// Deliberately NOT a <fieldset>/<legend> pair: browsers render <legend>
// straddling the fieldset's own top border by default (half above it, half
// below) — with this card's rounded border and bg-surface fill, that reads as
// the title text bleeding out of the box into the page background behind it.
// A plain div + heading avoids that native straddle-the-border behavior
// entirely.
// NOTE: Dedup.tsx's card-view tiles reproduce these two class strings inline
// (fixed-width + truncated title, which this component's plain {title:
// string} prop can't express) instead of using Card directly — keep them in
// sync if these classes change.
export const Card: Component<{ title: string; children: JSX.Element }> = (
  props,
) => (
  <div class="mb-4 rounded-xl border border-border bg-surface p-4">
    <h3 class="mb-2 px-2 text-sm font-semibold text-fg">{props.title}</h3>
    {props.children}
  </div>
);

// SaveStatus renders the inline "saved" / error line every panel's Save button
// pairs with. text is empty until an action runs.
export const SaveStatus: Component<{ text: string; error: boolean }> = (
  props,
) => (
  <Show when={props.text}>
    <span class={`text-sm ${props.error ? "text-danger" : "text-muted"}`}>
      {props.text}
    </span>
  </Show>
);

// useSaveStatus is the tiny per-panel status signal helper.
export function useSaveStatus() {
  const [status, setStatus] = createSignal<{ text: string; error: boolean }>({
    text: "",
    error: false,
  });
  return {
    status,
    saved: () => setStatus({ text: "saved", error: false }),
    failed: (e: unknown) =>
      setStatus({ text: (e as Error).message, error: true }),
    set: (text: string) => setStatus({ text, error: false }),
  };
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
// `trailing` is an optional screen-level slot rendered after the tab buttons
// (e.g. Discover's Edit-mode toggle) — most callers omit it.
export type ScreenTabsRegistration = {
  tabs: TabDef[];
  current: () => string;
  onSelect: (id: string) => void;
  trailing?: JSX.Element;
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
// TabDef set, plus an optional trailing slot rendered after them (e.g.
// Discover's Edit-mode toggle). The shell renders it in its consistent
// location; screens rendered standalone (no shell) render it inline via
// ModeTabs' fallback.
export function ScreenTabBar(props: {
  tabs: TabDef[];
  current: () => string;
  onSelect: (id: string) => void;
  trailing?: JSX.Element;
  class?: string;
}): JSX.Element {
  return (
    <div class={props.class ?? "mb-4 flex items-center gap-1"}>
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
      {props.trailing}
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
  trailing?: JSX.Element;
  class?: string;
}): JSX.Element {
  const registered = useScreenTabs({
    tabs: props.tabs,
    current: props.current,
    onSelect: props.onSelect,
    trailing: props.trailing,
  });
  if (registered) return null as unknown as JSX.Element;
  return (
    <ScreenTabBar
      tabs={props.tabs}
      current={props.current}
      onSelect={props.onSelect}
      trailing={props.trailing}
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

// BatchResultSummary renders the outcome of one "Apply Selected" batch: an
// "N applied, M failed" line plus, when any failed, a per-item list of the
// skipped titles and their errors. The backend applies items sequentially and
// skips-and-continues, so a batch can partially succeed — this surfaces exactly
// which items were left Pending and why. Failed items carry no proposal in the
// response (only OK items do), so the screen passes `titleOf` to resolve a
// still-known id back to its row title; an id no longer in the list falls back
// to "#id".
export const BatchResultSummary: Component<{
  result: ApplyBatchResponse;
  titleOf: (id: number) => string;
}> = (props) => {
  const applied = () => props.result.results.filter((r) => r.ok).length;
  const failed = () => props.result.results.filter((r) => !r.ok);
  return (
    <div class="mt-3 rounded-md border border-border bg-surface-2 p-3 text-sm">
      <p class="text-fg">
        {applied()} applied, {failed().length} failed
      </p>
      <Show when={failed().length > 0}>
        <ul class="mt-1 list-disc pl-5 text-danger">
          <For each={failed()}>
            {(r) => (
              <li>
                {props.titleOf(r.id) || `#${r.id}`}: {r.error || "failed"}
              </li>
            )}
          </For>
        </ul>
      </Show>
    </div>
  );
};

// Switch is a pill-shaped, sliding-thumb toggle (iOS/Material style) — the
// shared visual for any row-level immediate-apply boolean control. No
// existing switch component predates this one: Advanced.tsx's toggle-shaped
// settings are all plain checkboxes or range sliders, and nothing in
// components/ was a switch either (checked when the Nodes list's pause/resume
// control was relocated out of a modal checkbox onto the node row — see
// Nodes.tsx). Added here, not screen-local, so the next row-level toggle
// reuses it instead of inventing its own styling.
export const Switch: Component<{
  checked: boolean;
  onChange: (next: boolean) => void;
  disabled?: boolean;
  ariaLabel: string;
}> = (props) => (
  <button
    type="button"
    role="switch"
    aria-checked={props.checked}
    aria-label={props.ariaLabel}
    disabled={props.disabled}
    onClick={() => props.onChange(!props.checked)}
    class="relative inline-flex h-5 w-9 shrink-0 items-center rounded-full transition-colors disabled:opacity-50"
    classList={{
      "bg-accent": props.checked,
      "bg-surface-2": !props.checked,
    }}
  >
    <span
      class="inline-block h-3.5 w-3.5 rounded-full bg-white transition-transform"
      classList={{
        "translate-x-[18px]": props.checked,
        "translate-x-[2px]": !props.checked,
      }}
    />
  </button>
);

// PillSelector is the labeled row of pill buttons shared by the Discover
// detail popup's resolution/tier/protocol selectors and Settings' per-mode
// quality-preference pickers — the same visual/interaction pattern
// (`rounded-md border px-2 py-1 text-xs font-medium`, accent fill when
// selected, `disabled:opacity-40` when not selectable) used identically in
// both places. `isDisabled(v)` returns true when that option should be
// disabled — absent means every option is always enabled (Settings' case;
// there's no live availability grid to grey against at config time). The
// popup passes `(v) => !resolutionEnabled(v)` etc. — its own predicates are
// phrased as "enabled," so they're inverted at the call site to match this
// prop's "disabled" phrasing.
export function PillSelector<T extends string>(props: {
  label: string;
  options: T[];
  optionLabels: Record<T, string>;
  selected: T | null;
  onSelect: (v: T) => void;
  isDisabled?: (v: T) => boolean;
}): JSX.Element {
  return (
    <div class="mb-2">
      <div class={labelClass}>{props.label}</div>
      <div class="mt-1 flex flex-wrap gap-1.5">
        <For each={props.options}>
          {(opt) => (
            <button
              type="button"
              class="rounded-md border px-2 py-1 text-xs font-medium disabled:opacity-40"
              classList={{
                "border-accent bg-accent text-accent-fg": props.selected === opt,
                "border-border bg-surface-2 text-fg": props.selected !== opt,
              }}
              disabled={props.isDisabled ? props.isDisabled(opt) : false}
              onClick={() => props.onSelect(opt)}
            >
              {props.optionLabels[opt]}
            </button>
          )}
        </For>
      </div>
    </div>
  );
}

// yearOf pulls the leading 4-digit year from a TMDB/TPDB date string
// ("YYYY-.."), as a number — undefined when there is no parseable year. Shared
// by Discover (display) and Rename's Re-pick (the numeric year sent with a new
// match).
export function yearOf(date: string): number | undefined {
  const y = date && date.length >= 4 ? parseInt(date.slice(0, 4), 10) : NaN;
  return Number.isFinite(y) ? y : undefined;
}
