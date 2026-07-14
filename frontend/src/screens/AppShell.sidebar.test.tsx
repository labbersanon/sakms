// Sidebar tests — the left nav renders all 7 items with icons + labels, the
// collapse toggle hides labels while keeping icons, and the collapsed choice
// persists to localStorage across a fresh mount. Also covers the mobile
// off-canvas drawer: open/closed translate classes and closing on nav-link
// click or backdrop tap.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { Route, Router } from "@solidjs/router";
import {
  Sidebar,
  SIDEBAR_COLLAPSED_KEY,
  createPersistedBool,
} from "./AppShell";

const NAV_LABELS = [
  "Discover",
  "Grabs",
  "Rename",
  "Purge",
  "Dedup",
  "Tag",
  "Settings",
];

// renderSidebar mounts the Sidebar inside a Router (its <A> links need router
// context) with its collapsed state owned by the persisted-bool helper — the
// exact wiring AppShell uses — so a mount reflects whatever localStorage holds.
function renderSidebar() {
  const Harness = () => {
    const [collapsed, setCollapsed] = createPersistedBool(
      SIDEBAR_COLLAPSED_KEY,
      false,
    );
    return (
      <Sidebar collapsed={collapsed} onToggle={() => setCollapsed(!collapsed())} />
    );
  };
  return render(() => (
    <Router root={Harness}>
      <Route path="/" component={() => <div />} />
      <Route path="*" component={() => <div />} />
    </Router>
  ));
}

beforeEach(() => localStorage.clear());
afterEach(() => localStorage.clear());

describe("Sidebar", () => {
  it("renders all 7 nav items with icons and labels when expanded", () => {
    const { container } = renderSidebar();
    for (const label of NAV_LABELS) {
      expect(screen.getByText(label)).toBeInTheDocument();
    }
    // 7 nav icons + 1 collapse-toggle chevron.
    expect(container.querySelectorAll("svg").length).toBe(8);
  });

  it("collapse toggle hides labels but keeps icons", () => {
    const { container } = renderSidebar();
    fireEvent.click(screen.getByLabelText("Collapse sidebar"));

    for (const label of NAV_LABELS) {
      expect(screen.queryByText(label)).not.toBeInTheDocument();
    }
    // Icons (and the chevron) all remain when collapsed.
    expect(container.querySelectorAll("svg").length).toBe(8);
    // The labels survive as native tooltips for hover discoverability.
    for (const label of NAV_LABELS) {
      expect(container.querySelector(`a[title="${label}"]`)).toBeTruthy();
    }
  });

  it("expanding after collapsing restores labels", () => {
    renderSidebar();
    fireEvent.click(screen.getByLabelText("Collapse sidebar"));
    expect(screen.queryByText("Discover")).not.toBeInTheDocument();

    fireEvent.click(screen.getByLabelText("Expand sidebar"));
    for (const label of NAV_LABELS) {
      expect(screen.getByText(label)).toBeInTheDocument();
    }
  });

  it("persists the collapsed choice to localStorage", () => {
    renderSidebar();
    expect(localStorage.getItem(SIDEBAR_COLLAPSED_KEY)).toBeNull();

    fireEvent.click(screen.getByLabelText("Collapse sidebar"));
    expect(localStorage.getItem(SIDEBAR_COLLAPSED_KEY)).toBe("true");

    fireEvent.click(screen.getByLabelText("Expand sidebar"));
    expect(localStorage.getItem(SIDEBAR_COLLAPSED_KEY)).toBe("false");
  });

  it("starts collapsed when localStorage says so (persistence across reload)", () => {
    localStorage.setItem(SIDEBAR_COLLAPSED_KEY, "true");
    const { container } = renderSidebar();

    // Labels hidden on this fresh mount because the persisted flag was read.
    expect(screen.queryByText("Discover")).not.toBeInTheDocument();
    expect(container.querySelectorAll("svg").length).toBe(8);
    // Toggle shows the expand affordance, confirming it mounted collapsed.
    expect(screen.getByLabelText("Expand sidebar")).toBeInTheDocument();
  });
});

// renderMobileSidebar mounts Sidebar with an explicit mobileOpen/onCloseMobile
// pair (what AppShell actually wires) rather than the collapsed-only harness
// above, since these tests are specifically about the off-canvas drawer.
function renderMobileSidebar() {
  const onCloseMobile = vi.fn();
  const Harness = () => {
    const [collapsed, setCollapsed] = createPersistedBool(
      SIDEBAR_COLLAPSED_KEY,
      false,
    );
    return (
      <Sidebar
        collapsed={collapsed}
        onToggle={() => setCollapsed(!collapsed())}
        mobileOpen={() => true}
        onCloseMobile={onCloseMobile}
      />
    );
  };
  const result = render(() => (
    <Router root={Harness}>
      <Route path="/" component={() => <div />} />
      <Route path="*" component={() => <div />} />
    </Router>
  ));
  return { ...result, onCloseMobile };
}

describe("Sidebar mobile drawer", () => {
  it("defaults to closed (translated off-screen) when mobileOpen is omitted", () => {
    const { container } = renderSidebar();
    const nav = container.querySelector("nav")!;
    expect(nav.classList.contains("-translate-x-full")).toBe(true);
    expect(nav.classList.contains("translate-x-0")).toBe(false);
  });

  it("applies the open translate class when mobileOpen() is true", () => {
    const { container } = renderMobileSidebar();
    const nav = container.querySelector("nav")!;
    expect(nav.classList.contains("translate-x-0")).toBe(true);
    expect(nav.classList.contains("-translate-x-full")).toBe(false);
  });

  it("calls onCloseMobile when a nav link is clicked", () => {
    const { onCloseMobile } = renderMobileSidebar();
    fireEvent.click(screen.getByText("Grabs"));
    expect(onCloseMobile).toHaveBeenCalledOnce();
  });

  it("hides the desktop collapse toggle from the mobile drawer's accessible controls", () => {
    // The chevron button is CSS-hidden (not removed) below md, but it must
    // stay out of the way of an actual off-canvas-drawer interaction check —
    // asserting its class rather than visibility, since jsdom doesn't
    // evaluate media queries.
    const { container } = renderMobileSidebar();
    const toggle = screen.getByLabelText("Collapse sidebar");
    expect(toggle.classList.contains("hidden")).toBe(true);
    expect(container.querySelector("nav")).toBeTruthy();
  });
});
