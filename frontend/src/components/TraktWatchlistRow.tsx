// TraktWatchlistRow — Discover's "Trakt Watchlist" row: titles the operator
// marked "want to watch" on Trakt but doesn't own yet. Self-guards on Trakt's
// connection status so it renders nothing until an account is linked
// (Settings' Connections tab is where that happens) — no other Discover code
// needs to know whether Trakt is configured.
//
// Mounted as `<TraktWatchlistRow onGrab={setGrabTarget} />` inside
// MainstreamDiscover (discover/Mainstream.tsx), sharing its existing grab
// dialog. GrabButton/GrabTarget come from discover/Mainstream.tsx and
// discover/shared.tsx respectively (Discover.tsx is now just a thin
// re-export shim over discover/index.tsx) rather than reimplemented, so a
// watchlist card grabs through the identical auto-grab/season-episode-picker
// path every other Discover card does.

import { type Component, createResource, Show } from "solid-js";
import { Carousel } from "./Carousel";
import { ErrorText } from "./ui";
import { fetchTraktStatus, fetchTraktWatchlist, type TraktWatchlistItem } from "../api/trakt";
import { fetchTitlePoster, tmdbPoster, type DiscoverItem } from "../api/discover";
import { GrabButton } from "../screens/discover/Mainstream";
import { type GrabTarget } from "../screens/discover/shared";
import { MediaFallbackTile } from "./media";

// Claude 2026-08-13: local TextPoster copy retired; MediaFallbackTile is the
// shared missing-art tile. Reason: leftover cinematic duplication.
// Review if: Trakt cards need a full-title poster hole again.
// const TextPoster: Component<{ label: string }> = (props) => (
//   <div class="flex h-full w-full items-center justify-center bg-surface-2 p-2 text-center text-xs text-muted">
//     {props.label}
//   </div>
// );

// WatchlistCard maps one Trakt watchlist entry to sakms's card shape. Trakt
// gives only (type, title, year, tmdbId) — no poster — so art is resolved the
// same on-demand way LibraryCard resolves it for the existing-library row:
// one bounded fetchTitlePoster call per rendered card, by tmdbId.
const WatchlistCard: Component<{
  item: TraktWatchlistItem;
  onGrab: (t: GrabTarget) => void;
}> = (props) => {
  const mode = (): "movies" | "series" =>
    props.item.type === "show" ? "series" : "movies";
  const [poster] = createResource(
    () => props.item.tmdbId,
    (id) => (id ? fetchTitlePoster(mode(), id).catch(() => "") : Promise.resolve("")),
  );
  const src = () => tmdbPoster(poster() ?? "");
  const grabItem = (): DiscoverItem => ({
    id: props.item.tmdbId,
    title: props.item.title,
    posterPath: poster() ?? "",
    overview: "",
    releaseDate: props.item.year ? String(props.item.year) : "",
    voteAverage: 0,
    mediaType: mode() === "series" ? "tv" : "movie",
  });

  return (
    <div class="w-36 shrink-0" title={props.item.title}>
      <div class="aspect-[2/3] overflow-hidden rounded-lg border border-border bg-surface">
        <Show when={src()} fallback={<MediaFallbackTile title={props.item.title} />}>
          <img
            src={src()}
            alt={props.item.title}
            loading="lazy"
            class="h-full w-full object-cover"
          />
        </Show>
      </div>
      <div class="mt-1.5 truncate text-sm text-fg" title={props.item.title}>
        {props.item.title}
      </div>
      <div class="text-xs text-muted">{props.item.year || "—"}</div>
      <div class="mt-1.5">
        <GrabButton mode={mode()} item={grabItem()} onGrab={props.onGrab} />
      </div>
    </div>
  );
};

export const TraktWatchlistRow: Component<{
  contentType?: "movies" | "series";
  onGrab: (t: GrabTarget) => void;
}> = (props) => {
  const [status] = createResource(fetchTraktStatus);
  const [items] = createResource(
    () => (status()?.linked ? true : undefined),
    () => fetchTraktWatchlist(),
  );
  const visibleItems = () =>
    (items() ?? []).filter((item) => {
      if (!props.contentType) return true;
      return props.contentType === "series"
        ? item.type === "show"
        : item.type === "movie";
    });

  return (
    <Show when={status()?.linked}>
      <Show
        when={!items.error}
        fallback={<ErrorText>{(items.error as Error)?.message}</ErrorText>}
      >
        <Carousel
          title="Trakt Watchlist"
          items={visibleItems()}
          loading={items.loading}
          emptyText="Your Trakt watchlist is empty."
          renderItem={(item) => <WatchlistCard item={item} onGrab={props.onGrab} />}
        />
      </Show>
    </Show>
  );
};
