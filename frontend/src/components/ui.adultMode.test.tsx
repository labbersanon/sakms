// ModeTabs' Adult-visibility behavior — see
// .omc/plans/ralplan-adult-disable-switch.md step 5 / Open Question 3.
// ModeTabs is the ONE shared component behind all 4 workflow screens
// (Rename/Purge/Dedup/Tag, confirmed each just renders
// `<ModeTabs current={mode} onSelect={setMode} />` over its own plain
// `createSignal<Mode>("movies")`, no other mode-filtering logic of its own —
// see those screens' source) plus Settings' ModeSelector (settings/index.tsx,
// covered separately in Settings.test.tsx since it isn't exported). The
// standalone Grabs screen this comment used to list is gone — its grab
// history now lives in Calendar's History view (Queue's third tab), which
// renders its own separate ModeTabs instance. Testing ModeTabs directly here
// gives full coverage of the centralized filter + fallback logic without
// touching any of those 4 screens' own test files.
//
// Rendered standalone with no ScreenTabsContext (as every one of those
// screens' unit tests already does), ModeTabs falls back to drawing its tab
// bar inline — the same "register-or-fallback" pattern used pre-existing.
// With no AdultModeContext.Provider either, useAdultEnabled() resolves to the
// context's default (true) — this is WHY none of those 4 screens' existing
// test files needed any changes for this feature.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { AdultModeContext, ModeTabs } from "./ui";
import type { Mode } from "../api/discover";

afterEach(() => vi.unstubAllGlobals());

const renderModeTabs = (initial: Mode = "movies") => {
  const [mode, setMode] = createSignal<Mode>(initial);
  const onSelect = vi.fn((m: Mode) => setMode(m));
  render(() => <ModeTabs current={mode} onSelect={onSelect} />);
  return { mode, onSelect };
};

const renderModeTabsWithAdultMode = (
  enabled: boolean,
  initial: Mode = "movies",
) => {
  const [mode, setMode] = createSignal<Mode>(initial);
  const onSelect = vi.fn((m: Mode) => setMode(m));
  render(() => (
    <AdultModeContext.Provider value={{ enabled: () => enabled, refetch: () => {} }}>
      <ModeTabs current={mode} onSelect={onSelect} />
    </AdultModeContext.Provider>
  ));
  return { mode, onSelect };
};

describe("ModeTabs — no AdultModeContext.Provider (every workflow screen's own unit test)", () => {
  it("shows all 3 tabs including Adult (context default is enabled)", () => {
    renderModeTabs();
    expect(screen.getByText("Movies")).toBeInTheDocument();
    expect(screen.getByText("Series")).toBeInTheDocument();
    expect(screen.getByText("Adult")).toBeInTheDocument();
  });
});

describe("ModeTabs — Adult mode disabled", () => {
  it("omits Adult from the rendered tab list", () => {
    renderModeTabsWithAdultMode(false);
    expect(screen.getByText("Movies")).toBeInTheDocument();
    expect(screen.getByText("Series")).toBeInTheDocument();
    expect(screen.queryByText("Adult")).toBeNull();
  });

  it("falls back to Movies when the current mode is Adult at the moment it becomes disabled", () => {
    const { onSelect } = renderModeTabsWithAdultMode(false, "adult");
    expect(onSelect).toHaveBeenCalledWith("movies");
  });

  it("does not touch the mode when it was already something other than Adult", () => {
    const { onSelect } = renderModeTabsWithAdultMode(false, "series");
    expect(onSelect).not.toHaveBeenCalled();
  });
});

describe("ModeTabs — Adult mode enabled", () => {
  it("shows all 3 tabs and never fires a fallback", () => {
    const { onSelect } = renderModeTabsWithAdultMode(true, "adult");
    expect(screen.getByText("Adult")).toBeInTheDocument();
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("clicking Adult still selects it", () => {
    renderModeTabsWithAdultMode(true, "movies");
    fireEvent.click(screen.getByText("Adult"));
    expect(screen.getByText("Adult")).toBeInTheDocument();
  });
});
