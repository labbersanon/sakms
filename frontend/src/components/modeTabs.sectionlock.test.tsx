// ModeTabs + section lock — the embedded-screen decision the plan pins (§7):
// when `adult-content` is locked but the parent tab is not, ModeTabs HIDES the
// Adult tab entirely, mirroring what useAdultEnabled already does when Adult
// mode is off.
//
// Every one of the five screens that renders ModeTabs (Rename, Purge, Dedup,
// Tag, calendar/History) calls it identically — `<ModeTabs current={mode}
// onSelect={setMode} />` — so this ONE change covers all five and none of them
// needed an edit. That is why this test lives on the component rather than
// being copy-pasted into five screen suites.
//
// The backend gate remains the real boundary; this hiding is UX polish. A
// hidden tab is not enforcement and must never be mistaken for it.

import { describe, expect, it } from "vitest";
import { createSignal } from "solid-js";
import { render, screen } from "@solidjs/testing-library";
import type { Mode } from "../api/discover";
import {
  ModeTabs,
  SectionLockContext,
  type SectionLockControl,
} from "./ui";
import { ADULT_CONTENT_SECTION } from "../api/sectionLock";

const control = (locked: boolean): SectionLockControl => ({
  isLocked: (s) => locked && s === ADULT_CONTENT_SECTION,
  lockedSections: () => (locked ? [ADULT_CONTENT_SECTION] : []),
  pinSet: () => locked,
  unlocked: () => false,
  enforcementAvailable: () => true,
  refetch: () => {},
});

// Rendered with no ScreenTabsContext (as every standalone screen test is), so
// ModeTabs draws its bar inline — the same fallback path those suites use.
function renderTabs(locked: boolean, initial: Mode = "movies") {
  const [mode, setMode] = createSignal<Mode>(initial);
  render(() => (
    <SectionLockContext.Provider value={control(locked)}>
      <ModeTabs current={mode} onSelect={setMode} />
    </SectionLockContext.Provider>
  ));
  return mode;
}

describe("ModeTabs under a section lock", () => {
  it("shows Movies/Series/Adult when adult-content is not locked", () => {
    renderTabs(false);
    expect(screen.getByText("Movies")).toBeInTheDocument();
    expect(screen.getByText("Series")).toBeInTheDocument();
    expect(screen.getByText("Adult")).toBeInTheDocument();
  });

  it("hides the Adult tab when adult-content is locked", () => {
    renderTabs(true);
    expect(screen.getByText("Movies")).toBeInTheDocument();
    expect(screen.getByText("Series")).toBeInTheDocument();
    expect(screen.queryByText("Adult")).toBeNull();
  });

  it("falls back to Movies for a screen already sitting on Adult", () => {
    // The lock's predicate feeds the fall-back effect too, not just the tab
    // list. Without that, a screen left on Adult when the lock engages would
    // keep rendering Adult content with no tab left to navigate away by.
    const mode = renderTabs(true, "adult");
    expect(mode()).toBe("movies");
  });

  it("with no SectionLockContext Provider at all, nothing is hidden", () => {
    // The context default is "nothing locked", which is what keeps the ~370
    // existing standalone screen tests passing unmodified.
    const [mode, setMode] = createSignal<Mode>("movies");
    render(() => <ModeTabs current={mode} onSelect={setMode} />);
    expect(screen.getByText("Adult")).toBeInTheDocument();
  });
});
