// ViewAllLink — Discover row "View all" control. A real <a> so getByRole("link")
// works in tests that do not mount a Router; when a router is present, click
// is intercepted into client navigation so the SPA does not reload.
//
// Claude 2026-08-13: try/catch useNavigate matches DiscoverAdult's useLocation
// pattern. Reason: Discover unit tests mount DiscoverMainstream without a
// Router; A from @solidjs/router throws outside one.
// Review if: those tests wrap a Router and this can become a plain <A>.
//
// Claude 2026-08-13: DISCOVER_NAV_LINK_CLASS is a surface chip, not text-accent.
// Reason: gold #f2b705 on cream wallpaper + diagonal watermarks is unreadable
// on mobile (live /discover/row). Same class as RowView BackLink.
// Troubleshooting: if links look like bare gold underline, this class was lost.
// Review if: wallpaper is replaced with a solid dark/light surface.

import { type Component } from "solid-js";
import { useNavigate } from "@solidjs/router";

export const DISCOVER_NAV_LINK_CLASS =
  "inline-block shrink-0 rounded-md border border-border bg-surface/95 px-2.5 py-1 text-sm font-medium text-fg shadow-sm backdrop-blur-md hover:bg-surface-2";

export const ViewAllLink: Component<{ href: string; title: string }> = (
  props,
) => {
  let navigate: ReturnType<typeof useNavigate> | undefined;
  try {
    navigate = useNavigate();
  } catch {
    navigate = undefined;
  }
  return (
    <a
      href={props.href}
      class={DISCOVER_NAV_LINK_CLASS}
      aria-label={`View all ${props.title}`}
      onClick={(e) => {
        if (!navigate) return;
        e.preventDefault();
        void navigate(props.href);
      }}
    >
      View all
    </a>
  );
};
