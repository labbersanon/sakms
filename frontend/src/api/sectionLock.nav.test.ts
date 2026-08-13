// FE-1 — the nav-drift guard (plan §5.4 layer 2).
//
// LOCKABLE_TAB_SECTIONS is authored by hand in sectionLock.ts rather than
// derived from NAV_ITEMS, because deriving it would make api/sectionLock.ts
// import screens/AppShell.tsx, which imports it back — a hard ESM cycle (see
// that constant's own doc comment). This test is what keeps the two honest,
// and it only means anything BECAUSE they are authored independently: a
// derived list would compare against itself and catch nothing.
//
// ADDING A SIDEBAR TAB FAILS THIS TEST. That is the intended failure — add
// the new id to LOCKABLE_TAB_SECTIONS (and to internal/sectionlock/sections.go,
// whose TestTabSections is the Go-side half of the same guard) to fix it.

import { describe, expect, it } from "vitest";
import { NAV_ITEMS, sectionForPath } from "../screens/AppShell";
import { LOCKABLE_TAB_SECTIONS } from "./sectionLock";

describe("FE-1 — NAV_ITEMS / LOCKABLE_TAB_SECTIONS drift", () => {
  it("every sidebar item maps to a lockable section id, and vice versa", () => {
    const fromNav = NAV_ITEMS.map((i) => i.section);
    expect([...fromNav].sort()).toEqual([...LOCKABLE_TAB_SECTIONS].sort());
  });

  it("no sidebar href is bare '/'", () => {
    // "/" itself IS an app route, but it lives in APP_ROUTES, not NAV_ITEMS, and
    // is mapped to "discover" explicitly by sectionForPath.
    for (const item of NAV_ITEMS) {
      expect(item.href.startsWith("/")).toBe(true);
      expect(item.href.length).toBeGreaterThan(1);
    }
  });
});

describe("sectionForPath — the route table's one special case", () => {
  it("maps '/' to discover", () => {
    // "/" is in APP_ROUTES and renders Discover, but it is NOT in NAV_ITEMS
    // (the sidebar links /discover). A bare slice(1) would yield "" and leave
    // the landing view ungated while /discover itself was overlaid.
    expect(sectionForPath("/")).toBe("discover");
  });

  it("maps every sidebar href to its own section", () => {
    for (const item of NAV_ITEMS) {
      expect(sectionForPath(item.href)).toBe(item.section);
    }
  });

  it("returns null for a route no section gates", () => {
    expect(sectionForPath("/nope")).toBeNull();
  });
});
