// RssFeedRow — mirrors Mainstream.tsx's (module-local) SliderRow: renders
// one admin-defined RSS feed's live items as a Carousel, via
// fetchRssFeedItems. A single per-feed unit (not a block that fetches+
// filters the whole feed list itself) — the Discover row-order feature
// requires interleaving built-in and dynamic rows in one arbitrary operator-
// chosen order (see RowEditor.tsx), so the caller (Mainstream.tsx/Adult.tsx)
// owns fetching the feed list, filtering by target/enabled, and placing each
// feed's row at its position in the merged order; this component only knows
// how to render ONE feed once it's been placed. Unlike SliderRow/
// PaginatedRow, a feed's items are NOT client-paginated — the resolve
// endpoint caps at 50 items and fetches the live feed fresh per call, which
// has no stable page cursor to offer (see rss_feeds.go's
// resolveRssFeedHandler doc comment) — so this is a single fetch, no "Show
// more".

import { type Component, createEffect, createSignal, on } from "solid-js";
import type { RssFeed, RssFeedItem } from "@dto";
import { type RssFeedTarget, fetchRssFeedItems } from "../../api/rssFeeds";
import { Carousel } from "../../components/Carousel";
import { RssFeedCard } from "./RssFeedCard";

// rssFeedTargetMode maps a Feed's target to the movies|series|adult mode
// manualGrab needs — a feed belongs to exactly one target, no per-item
// ambiguity the way a "mixed" slider has (see rssfeeds.Target's doc comment:
// no multi-mode/"mixed" feeds).
function rssFeedTargetMode(
  target: RssFeedTarget,
): "movies" | "series" | "adult" {
  if (target === "movie") return "movies";
  if (target === "tv") return "series";
  return "adult";
}

export const RssFeedRow: Component<{
  feed: RssFeed;
  reloadToken: () => number;
  onError: (err: unknown) => void;
}> = (props) => {
  const [items, setItems] = createSignal<RssFeedItem[]>([]);
  const [loading, setLoading] = createSignal(false);

  const load = async () => {
    setLoading(true);
    try {
      setItems(await fetchRssFeedItems(props.feed.id));
    } catch (e) {
      props.onError(e);
    } finally {
      setLoading(false);
    }
  };

  createEffect(on(props.reloadToken, () => void load()));

  return (
    <Carousel
      title={props.feed.title}
      items={items()}
      renderItem={(item) => (
        <RssFeedCard
          item={item}
          mode={rssFeedTargetMode(props.feed.target as RssFeedTarget)}
        />
      )}
      loading={loading()}
    />
  );
};
