// index.tsx — the Calendar screen (Wave 3 FE-CAL of
// .omc/plans/autopilot-impl-calendar-grabs-requests.md §7.2). It owns the three
// pieces of state its two child views deliberately do NOT own: the
// History/Upcoming view switch, the grid/list display toggle (AC3), and the
// month navigation. History.tsx and Upcoming.tsx are pure views taking
// `{ year, month, display }` as plain-value props.
//
// TWO LEVELS OF SWITCHING, AND THEY ARE DIFFERENT THINGS (§7.2). This file owns
// only the TOP level — History ↔ Upcoming. Mode selection is the second level
// and stays inside each child, because the two children need different sets:
// History is three-mode (grabs exist for Movies, Series AND Adult) via ModeTabs;
// Upcoming is Movies/Series only (§0.6 — Adult has no release calendar source at
// all). Do not hoist either one up here "for symmetry"; there is no single
// selector that is correct for both.
//
// THE VIEW SWITCH IS ScreenTabBar, NOT ScreenTabs — this is the load-bearing
// choice in this file. The app shell has exactly ONE tab slot. Calendar is a
// Queue sidebar child (?tab=calendar); Queue wraps children in a shadowing
// ScreenTabsContext.Provider so History's ModeTabs cannot claim that slot.
// ScreenTabs here would still fight that contract; ScreenTabBar draws inline.
// Upcoming.tsx's own content selector made the same call for the same reason.
//
// EXPORT SHAPE: a named `Calendar: Component` taking no props, so Queue.tsx can
// render `<Calendar />` exactly as it renders `<Downloads />` / `<Requests />`.
// Queue's shadowing ScreenTabsContext.Provider stays load-bearing under this
// screen (§0.7): History's ModeTabs still registers a tab set, and without the
// shadow it would hijack the shell slot the moment History mounts.
//
// MONTH NAVIGATION mirrors discover/CalendarView.tsx:117-130 structurally — the
// same `‹`/`›` Buttons, the same "Previous month"/"Next month" aria-labels, the
// same single-Date over/underflow normalization in step(). That is convention
// reuse, not code reuse: nothing is imported from that screen, matching the
// deliberate-duplication stance grid.ts's header sets out in full (§0.3).
// MONTH_NAMES is duplicated here for the same reason.
//
// BOTH PERSISTED KEYS SANITIZE FOR DISPLAY WITHOUT REWRITING STORAGE: an
// unreadable or unrecognized stored value falls back to the default for DISPLAY
// only, and storage is left untouched until the operator actually picks
// something. (Queue's own ?tab= key now canonicalizes like Organize; these
// Calendar keys keep the older display-only sanitize.) These are brand-new keys
// with no legacy values in the wild — the sanitization exists for corruption
// and for future value changes (§7.2).

import { batch, type Component, createSignal, Show } from "solid-js";
import { Button, ScreenTabBar, type TabDef } from "../../components/ui";
import { createPersistedString } from "../AppShell";
import { History } from "./History";
import { Upcoming } from "./Upcoming";

// Both keys follow QUEUE_TAB_KEY's short dotted app-prefixed convention.
const VIEW_KEY = "sakms.calendar.view";
const DISPLAY_KEY = "sakms.calendar.display";

const VIEW_TABS: TabDef[] = [
  { id: "history", label: "History" },
  { id: "upcoming", label: "Upcoming" },
];
const VIEW_IDS = VIEW_TABS.map((t) => t.id);

const DISPLAY_TABS: TabDef[] = [
  { id: "grid", label: "Grid" },
  { id: "list", label: "List" },
];
const DISPLAY_IDS = DISPLAY_TABS.map((t) => t.id);

const MONTH_NAMES = [
  "January", "February", "March", "April", "May", "June",
  "July", "August", "September", "October", "November", "December",
];

export const Calendar: Component = () => {
  const [storedView, setView] = createPersistedString(VIEW_KEY, "history");
  const view = () => (VIEW_IDS.includes(storedView()) ? storedView() : "history");

  const [storedDisplay, setDisplay] = createPersistedString(DISPLAY_KEY, "grid");
  // The cast is safe precisely because it is guarded by the membership check —
  // DISPLAY_IDS is derived from DISPLAY_TABS, so the two can never drift.
  const display = (): "grid" | "list" =>
    DISPLAY_IDS.includes(storedDisplay())
      ? (storedDisplay() as "grid" | "list")
      : "grid";

  // The month is NOT persisted, deliberately: reopening Calendar should show
  // the current month, not whatever month the operator last browsed to.
  const now = new Date();
  const [year, setYear] = createSignal(now.getFullYear());
  const [month, setMonth] = createSignal(now.getMonth());

  const step = (delta: number) => {
    // Normalize month over/underflow into the adjacent year via a single Date,
    // so December → January rolls the year without any modular arithmetic here.
    const d = new Date(year(), month() + delta, 1);
    // batch() is REQUIRED, not tidiness. Without it the two writes land as two
    // separate updates and the mounted child sees an intermediate, nonexistent
    // month: stepping forward from December 2026 renders {2027, 11} —
    // December 2027 — before settling on January 2027. Upcoming keys its
    // resource on {content, year, month}, so that intermediate state fires a
    // real fetch of a month nobody asked for, on an endpoint costing up to 22
    // TMDB calls per month view (plan §4.3). Caught by this file's own
    // "passes the navigated year/month down to the mounted child" test, which
    // counts the requests rather than only inspecting the last one.
    batch(() => {
      setYear(d.getFullYear());
      setMonth(d.getMonth());
    });
  };

  return (
    <div>
      <ScreenTabBar
        tabs={VIEW_TABS}
        current={view}
        onSelect={setView}
        trailing={
          <ScreenTabBar
            tabs={DISPLAY_TABS}
            current={display}
            onSelect={setDisplay}
            class="ml-auto flex items-center gap-1"
          />
        }
      />

      <div class="mb-3 flex items-center gap-3">
        <Button class="!px-3 !py-1" onClick={() => step(-1)} aria-label="Previous month">
          ‹
        </Button>
        <div class="min-w-[10rem] text-center text-sm font-semibold text-fg">
          {MONTH_NAMES[month()]} {year()}
        </div>
        <Button class="!px-3 !py-1" onClick={() => step(1)} aria-label="Next month">
          ›
        </Button>
      </div>

      {/* <Show> unmounts the inactive view rather than hiding it, same as
          Queue.tsx does with its three tabs: the inactive view must not keep
          fetching, and Upcoming's movies endpoint costs up to 22 TMDB calls per
          month view. */}
      <Show when={view() === "history"}>
        <History year={year()} month={month()} display={display()} />
      </Show>
      <Show when={view() === "upcoming"}>
        <Upcoming year={year()} month={month()} display={display()} />
      </Show>
    </div>
  );
};
