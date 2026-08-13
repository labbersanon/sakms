// ViewAllLink — Discover row "View all" control. A real <a> so getByRole("link")
// works in tests that do not mount a Router; when a router is present, click
// is intercepted into client navigation so the SPA does not reload.
//
// Claude 2026-08-13: try/catch useNavigate matches DiscoverAdult's useLocation
// pattern. Reason: Discover unit tests mount DiscoverMainstream without a
// Router; A from @solidjs/router throws outside one.
// Review if: those tests wrap a Router and this can become a plain <A>.

import { type Component } from "solid-js";
import { useNavigate } from "@solidjs/router";

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
      class="shrink-0 text-sm font-medium text-accent hover:underline"
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
