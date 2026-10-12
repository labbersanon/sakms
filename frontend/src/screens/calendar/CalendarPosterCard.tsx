// CalendarPosterCard — Discover PosterCard's visual shell for Queue calendars
// (History grabs, Upcoming movies/episodes). Same 220px tile, 2:3 poster,
// title + muted meta. Actions stay caller-owned: Discover still uses
// PosterCard (DetailPopup / Grab); History is read-only; Upcoming movies
// click-to-request; Upcoming series stays non-requestable.
//
// Claude 2026-10-12: Queue calendar cells were text chips / cell-width thumbs.
// Reason: standardize every month grid to Discover's poster calendar.
// Troubleshooting: Queue → Calendar did not look like Discover → Calendar.
// Review if: MEDIA_CAROUSEL_POSTER_CLASS changes — MonthGrid min-w tracks it.
// Related files: MonthGrid.tsx, discover/Mainstream.tsx PosterCard

import { type Component, type JSX, Show } from "solid-js";
import {
  MediaCardShell,
  MediaFallbackTile,
  MEDIA_CAROUSEL_POSTER_CLASS,
} from "../../components/media";

export const CalendarPosterCard: Component<{
  title: string;
  posterSrc?: string;
  meta?: string;
  hover?: string;
  badge?: string;
  badgeWarn?: boolean;
  // When set, the card is a button (Upcoming movie request). When omitted the
  // tile is inert (History, Upcoming series) so it cannot be mistaken for a
  // request control.
  onClick?: () => void;
  disabled?: boolean;
  ariaLabel?: string;
  layout?: "grid" | "list";
  children?: JSX.Element;
}> = (props) => {
  const poster = () => (
    <div
      classList={{
        "relative aspect-[2/3] overflow-hidden rounded-lg border border-border bg-surface":
          props.layout !== "list",
        "relative h-16 w-11 shrink-0 overflow-hidden rounded border border-border bg-surface":
          props.layout === "list",
      }}
    >
      <Show
        when={props.posterSrc}
        fallback={<MediaFallbackTile title={props.title} />}
      >
        <img
          src={props.posterSrc}
          alt={props.layout === "list" ? "" : props.title}
          loading="lazy"
          class="h-full w-full object-cover"
        />
      </Show>
      <Show when={props.layout !== "list" && props.hover}>
        <div class="absolute inset-0 flex items-end bg-black/70 p-2 opacity-0 transition-opacity group-hover:opacity-100">
          <p class="line-clamp-5 text-xs text-white">{props.hover}</p>
        </div>
      </Show>
    </div>
  );

  const copy = () => (
    <div class="min-w-0">
      <div
        class="truncate text-sm text-fg"
        classList={{ "mt-1.5": props.layout !== "list" }}
        title={props.title}
      >
        {props.title}
      </div>
      <Show when={props.meta}>
        <div class="flex items-center gap-2 text-xs text-muted">{props.meta}</div>
      </Show>
      <Show when={props.badge}>
        <span
          class="mt-0.5 inline-block rounded border px-1 text-[10px] font-medium"
          classList={{
            "border-dashed border-warn text-warn": !!props.badgeWarn,
            "border-border text-muted": !props.badgeWarn,
          }}
        >
          {props.badge}
        </span>
      </Show>
      {props.children}
    </div>
  );

  const inner = () => (
    <Show
      when={props.layout === "list"}
      fallback={
        <>
          {poster()}
          {copy()}
        </>
      }
    >
      <div class="flex items-center gap-3 text-left">
        {poster()}
        {copy()}
      </div>
    </Show>
  );

  return (
    <Show
      when={props.onClick}
      fallback={
        <div
          class={
            props.layout === "list"
              ? "w-full rounded-lg border-2 border-transparent bg-surface p-1.5"
              : `${MEDIA_CAROUSEL_POSTER_CLASS} group`
          }
        >
          <div class="flex w-full flex-col overflow-hidden rounded-lg border-2 border-transparent bg-surface text-left">
            {inner()}
          </div>
        </div>
      }
    >
      <div
        class={
          props.layout === "list" ? "w-full" : MEDIA_CAROUSEL_POSTER_CLASS
        }
      >
        <MediaCardShell
          class="group w-full"
          label={props.ariaLabel ?? props.title}
          disabled={props.disabled}
          onClick={() => props.onClick?.()}
        >
          {inner()}
        </MediaCardShell>
      </div>
    </Show>
  );
};
