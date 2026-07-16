// Carousel — a reusable horizontal, arrow-navigated row (Seerr-style), replacing
// the independently-paginated "Show more" button rows Discover used before.
// Generic over the item type so any card component (PosterCard, LibraryCard,
// AdultCard, future genre/studio/network cards) can render inside it without
// this component knowing anything about Movies/Series/Adult.
//
// Scroll + arrows: the row itself is a native `overflow-x-auto` flex strip, so
// touch/trackpad scrolling always works, arrows or not — the arrow buttons are
// a convenience layered on top via scrollBy(), not the only way to move. Arrow
// disabled-state is derived from the container's actual scroll position
// (scrollLeft/scrollWidth/clientWidth), read on mount, on every scroll event,
// and after the item list changes (a rAF tick after render, since a newly
// appended batch changes scrollWidth only once the DOM reflects it).
//
// Lazy-load-more: the same scroll handler fires onLoadMore() once the
// container scrolls within LOAD_MORE_THRESHOLD_PX of its right edge, gated on
// hasMore/loading so it never double-fires while a fetch is in flight or past
// the last page.

import {
  type Accessor,
  type Component,
  type JSX,
  createEffect,
  createSignal,
  For,
  Show,
} from "solid-js";
import { Muted } from "./ui";

// LOAD_MORE_THRESHOLD_PX is how close (in px) to the trailing edge the
// container must scroll before onLoadMore fires — far enough ahead that the
// next batch has a chance to arrive before the user hits the true end.
const LOAD_MORE_THRESHOLD_PX = 400;

// SCROLL_STEP_RATIO is how much of the visible width one arrow click scrolls
// by — most of a "page" so consecutive cards aren't skipped entirely, but not
// the full width so the leading edge card stays a visual anchor.
const SCROLL_STEP_RATIO = 0.9;

const svgProps = {
  width: "20",
  height: "20",
  viewBox: "0 0 24 24",
  fill: "none",
  stroke: "currentColor",
  "stroke-width": "2",
  "stroke-linecap": "round" as const,
  "stroke-linejoin": "round" as const,
  "aria-hidden": true,
};

const IconChevronLeft: Component = () => (
  <svg {...svgProps}>
    <path d="m15 6-6 6 6 6" />
  </svg>
);
const IconChevronRight: Component = () => (
  <svg {...svgProps}>
    <path d="m9 6 6 6-6 6" />
  </svg>
);

// arrowButtonClass is shared by both arrows: a circular button overlaid at the
// row's vertical center, hidden entirely below `sm` (mobile relies on native
// touch-scroll, matching the task's responsive requirement) and hidden when
// scrolling further that direction isn't possible.
const arrowButtonClass =
  "hidden sm:flex h-9 w-9 shrink-0 items-center justify-center rounded-full " +
  "border border-border bg-surface text-fg shadow transition hover:bg-surface-2 " +
  "disabled:pointer-events-none disabled:opacity-0";

export type CarouselProps<T> = {
  title: string;
  items: T[];
  renderItem: (item: T, index: Accessor<number>) => JSX.Element;
  onLoadMore?: () => void;
  hasMore?: boolean;
  loading?: boolean;
  emptyText?: string;
  class?: string;
};

export function Carousel<T>(props: CarouselProps<T>): JSX.Element {
  let track: HTMLDivElement | undefined;
  const [canScrollLeft, setCanScrollLeft] = createSignal(false);
  const [canScrollRight, setCanScrollRight] = createSignal(false);

  const updateScrollState = () => {
    const el = track;
    if (!el) return;
    setCanScrollLeft(el.scrollLeft > 1);
    setCanScrollRight(el.scrollLeft + el.clientWidth < el.scrollWidth - 1);
  };

  const onScroll = () => {
    updateScrollState();
    const el = track;
    if (!el || !props.onLoadMore || props.loading || props.hasMore === false) {
      return;
    }
    const distanceFromEnd = el.scrollWidth - el.clientWidth - el.scrollLeft;
    if (distanceFromEnd <= LOAD_MORE_THRESHOLD_PX) {
      props.onLoadMore();
    }
  };

  const scrollByStep = (direction: 1 | -1) => {
    const el = track;
    if (!el) return;
    el.scrollBy({
      left: direction * el.clientWidth * SCROLL_STEP_RATIO,
      behavior: "smooth",
    });
  };

  // Recompute arrow state whenever the rendered item count changes — queued a
  // tick out so the DOM (and therefore scrollWidth) has already updated to
  // reflect the new items.
  createEffect(() => {
    // Tracked read: re-runs this effect whenever the item count changes.
    props.items.length;
    queueMicrotask(updateScrollState);
  });

  return (
    <section class={props.class ?? "mt-6"}>
      <h2 class="mb-2 text-sm font-semibold uppercase tracking-wide text-muted">
        {props.title}
      </h2>
      <Show
        when={props.items.length > 0}
        fallback={
          <Muted>
            {props.loading ? "Loading…" : props.emptyText ?? "Nothing here yet."}
          </Muted>
        }
      >
        <div class="flex items-center gap-2">
          <button
            type="button"
            aria-label="Scroll left"
            class={arrowButtonClass}
            disabled={!canScrollLeft()}
            onClick={() => scrollByStep(-1)}
          >
            <IconChevronLeft />
          </button>
          <div
            ref={track}
            onScroll={onScroll}
            class="flex flex-1 items-stretch gap-3 overflow-x-auto scroll-smooth pb-2"
          >
            <For each={props.items}>{props.renderItem}</For>
            <Show when={props.loading}>
              <div class="flex w-28 shrink-0 items-center justify-center">
                <Muted>Loading…</Muted>
              </div>
            </Show>
          </div>
          <button
            type="button"
            aria-label="Scroll right"
            class={arrowButtonClass}
            disabled={!canScrollRight()}
            onClick={() => scrollByStep(1)}
          >
            <IconChevronRight />
          </button>
        </div>
      </Show>
    </section>
  );
}
