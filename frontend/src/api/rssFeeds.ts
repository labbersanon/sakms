// Admin-defined raw RSS 2.0 feed row data access — CRUD + reorder for
// operator-added feed rows (NZBGeek saved-search style URLs), plus
// resolving a feed's actual items for rendering on the Discover page.
// Mirrors src/api/discoverSliders.ts almost exactly — same CRUD+reorder
// shape, a different backend resource. Every call goes through api()
// (src/api/client.ts) so it inherits the session cookie and the global 401
// → re-boot session-expiry fallback. Request/response shapes are the
// generated DTOs (@dto), never hand-duplicated.

import { api } from "./client";
import type {
  RssFeed,
  RssFeedItem,
  RssFeedReorderRequest,
  RssFeedUpsertRequest,
} from "@dto";

export type { RssFeed, RssFeedItem, RssFeedUpsertRequest };

// RssFeedUpdateBody widens the generated RssFeedUpsertRequest's feedUrl to
// `string | null`. The feed URL is now a masked secret (feed_url_encrypted;
// toDTORssFeed always emits ""), so an update that only changes enabled/title
// MUST send `feedUrl: null` to preserve the stored URL — the backend's
// three-state *string reads null (or absent) as "preserve", "" as
// ErrFeedURLRequired, non-empty as "replace". tygo maps Go *string to a plain
// `string | undefined`, so it can't express the null the plan (and the
// toDTORssFeed doc comment) call for — this widens it at the boundary. Create
// never preserves (there's no stored value yet), so createRssFeed keeps the
// unwidened RssFeedUpsertRequest.
export type RssFeedUpdateBody = Omit<RssFeedUpsertRequest, "feedUrl"> & {
  feedUrl?: string | null;
};

// TARGETS mirrors internal/rssfeeds.Target's fixed enum — a feed belongs to
// exactly one mode, no "mixed" (unlike Slider's target, which allows mixed).
export const TARGETS = ["movie", "tv", "adult"] as const;
export type RssFeedTarget = (typeof TARGETS)[number];

// PROTOCOLS mirrors internal/rssfeeds.Protocol's fixed enum — admin-set at
// creation, not sniffed from the feed's XML.
export const PROTOCOLS = ["torrent", "usenet"] as const;
export type RssFeedProtocol = (typeof PROTOCOLS)[number];

// fetchRssFeeds lists every admin-defined feed, already ordered by
// sortOrder (rssfeeds.Store.List's own ordering) — GET
// /api/discover/rss-feeds.
export function fetchRssFeeds(): Promise<RssFeed[]> {
  return api<RssFeed[]>("/api/discover/rss-feeds");
}

export function createRssFeed(body: RssFeedUpsertRequest): Promise<RssFeed> {
  return api<RssFeed>("/api/discover/rss-feeds", {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export function updateRssFeed(
  id: number,
  body: RssFeedUpdateBody,
): Promise<RssFeed> {
  return api<RssFeed>(`/api/discover/rss-feeds/${id}`, {
    method: "PUT",
    body: JSON.stringify(body),
  });
}

export function deleteRssFeed(id: number): Promise<void> {
  return api<void>(`/api/discover/rss-feeds/${id}`, { method: "DELETE" });
}

// reorderRssFeeds sends the FULL new display order in one call — ids must
// cover every existing feed exactly once (Store.Reorder's requirement),
// never a partial/per-item bulk mutation.
export function reorderRssFeeds(ids: number[]): Promise<void> {
  const body: RssFeedReorderRequest = { ids };
  return api<void>("/api/discover/rss-feeds/reorder", {
    method: "POST",
    body: JSON.stringify(body),
  });
}

// fetchRssFeedItems resolves one feed's actual live items — GET
// /api/discover/rss-feeds/{id}/resolve. Each item is already a
// fully-resolved release (a real downloadUrl+protocol in hand), capped
// server-side at 50 items — no client pagination.
export function fetchRssFeedItems(feedId: number): Promise<RssFeedItem[]> {
  return api<RssFeedItem[]>(`/api/discover/rss-feeds/${feedId}/resolve`);
}
