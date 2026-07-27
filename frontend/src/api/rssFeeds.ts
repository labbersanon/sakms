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
  ProtocolUndetectedResponse,
  RssFeed,
  RssFeedCreateRequest,
  RssFeedItem,
  RssFeedReorderRequest,
  RssFeedUpdateRequest,
} from "@dto";

export type {
  RssFeed,
  RssFeedItem,
  RssFeedCreateRequest,
  ProtocolUndetectedResponse,
};

// RssFeedUpdateBody widens the generated RssFeedUpdateRequest's feedUrl to
// `string | null`. The feed URL is now a masked secret (feed_url_encrypted;
// toDTORssFeed always emits ""), so an update that only changes enabled/title
// MUST send `feedUrl: null` to preserve the stored URL — the backend's
// three-state *string reads null (or absent) as "preserve", "" as
// ErrFeedURLRequired, non-empty as "replace". tygo maps Go *string to a plain
// `string | undefined`, so it can't express the null the plan (and the
// toDTORssFeed doc comment) call for — this widens it at the boundary. Create
// has its own RssFeedCreateRequest type (protocol optional, feedUrl a required
// plain string), so createRssFeed doesn't use this.
export type RssFeedUpdateBody = Omit<RssFeedUpdateRequest, "feedUrl"> & {
  feedUrl?: string | null;
};

// PROTOCOL_UNDETECTED is the fixed sentinel the backend returns (as a 422 body
// {"error":"protocol_undetected"}) from both create (protocol omitted) and
// rescan when a feed's enclosures don't yield a confident torrent/usenet
// determination — see apidto.ProtocolUndetectedResponse. api() surfaces a
// non-ok JSON body's `error` field as the thrown Error's message, so the create
// path detects this by catching an Error with exactly this message; the rescan
// path returns it as a union member instead (see rescanRssFeed).
export const PROTOCOL_UNDETECTED = "protocol_undetected";

// isProtocolUndetected narrows a rescanRssFeed result (or any value) to the
// inconclusive-detection response, so a caller can branch to the manual-pick
// fallback pop-up instead of treating it as a resolved feed.
export function isProtocolUndetected(
  v: unknown,
): v is ProtocolUndetectedResponse {
  return (
    typeof v === "object" &&
    v !== null &&
    (v as ProtocolUndetectedResponse).error === PROTOCOL_UNDETECTED
  );
}

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

// createRssFeed posts a new feed — POST /api/discover/rss-feeds. Protocol is
// optional on RssFeedCreateRequest: Mainstream's AddRssFeedModal and the Adult
// fallback pop-up's retry send an explicit protocol; the Adult Add flow omits
// it and the backend auto-detects. When detection is inconclusive the backend
// returns 422 protocol_undetected, which api() throws as Error(PROTOCOL_UNDETECTED)
// — the Adult Add flow catches that and opens the manual-pick pop-up.
export function createRssFeed(body: RssFeedCreateRequest): Promise<RssFeed> {
  return api<RssFeed>("/api/discover/rss-feeds", {
    method: "POST",
    body: JSON.stringify(body),
  });
}

// rescanRssFeed re-runs backend protocol auto-detection against an existing
// feed — POST /api/discover/rss-feeds/{id}/rescan. A confident result overwrites
// the stored protocol and returns the updated feed; an inconclusive result
// returns the ProtocolUndetectedResponse union member (converted from api()'s
// thrown 422) so the caller can open the manual-pick pop-up instead of surfacing
// an error. The pop-up's manual pick is then applied via updateRssFeed (a PUT
// with the chosen protocol), NOT a second rescan — the rescan endpoint has no
// manual-protocol input.
export async function rescanRssFeed(
  id: number,
): Promise<RssFeed | ProtocolUndetectedResponse> {
  try {
    return await api<RssFeed>(`/api/discover/rss-feeds/${id}/rescan`, {
      method: "POST",
    });
  } catch (e) {
    if (e instanceof Error && e.message === PROTOCOL_UNDETECTED) {
      return { error: PROTOCOL_UNDETECTED };
    }
    throw e;
  }
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
