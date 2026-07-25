// Discover row display-order data access — a best-effort hint over the FULL
// merged row set (built-in rows plus every dynamic row type: sliders, adult
// newest rows, rss feeds) for one screen ("mainstream" | "adult"). See
// internal/api/discover_row_order.go's doc comment: this is deliberately
// NOT validated against a fixed known-key set the way reorderRssFeeds/
// reorderSliders are — the caller is responsible for appending any key it
// knows about but doesn't find in the stored order, and skipping any stored
// key that no longer resolves to anything live.

import { api } from "./client";
import type {
  RowHiddenRequest,
  RowHiddenResponse,
  RowOrderRequest,
  RowOrderResponse,
} from "@dto";

export type DiscoverScreen = "mainstream" | "adult";

export function fetchRowOrder(screen: DiscoverScreen): Promise<string[]> {
  return api<RowOrderResponse>(`/api/discover/row-order/${screen}`).then(
    (r) => r.keys,
  );
}

export function saveRowOrder(
  screen: DiscoverScreen,
  keys: string[],
): Promise<void> {
  const body: RowOrderRequest = { keys };
  return api<void>(`/api/discover/row-order/${screen}`, {
    method: "PUT",
    body: JSON.stringify(body),
  });
}

// fetchRowHidden / saveRowHidden are the row-order siblings for the per-screen
// hidden-structural-row set (GET/PUT /api/discover/row-hidden/{screen}). Unlike
// row-order there is NO mergeRowOrder-style reconciliation: absent-from-list
// means visible, so the raw stored key set is used as-is (see
// internal/api/discover_row_hidden.go / RowHiddenResponse's doc comment).
export function fetchRowHidden(screen: DiscoverScreen): Promise<string[]> {
  return api<RowHiddenResponse>(`/api/discover/row-hidden/${screen}`).then(
    (r) => r.keys,
  );
}

export function saveRowHidden(
  screen: DiscoverScreen,
  keys: string[],
): Promise<void> {
  const body: RowHiddenRequest = { keys };
  return api<void>(`/api/discover/row-hidden/${screen}`, {
    method: "PUT",
    body: JSON.stringify(body),
  });
}

// mergeRowOrder combines a stored key order with the current full set of
// known row keys (built-in + every live dynamic row) into one ordered list:
// stored keys that are still known keep their relative order; any stored key
// that no longer resolves to anything live (a deleted slider/feed) is
// dropped; any known key absent from the stored order (new/never-ordered —
// including a fresh install with no stored order at all) is appended at the
// end, in knownKeys' own order. Pure client-side logic — the backend
// deliberately stores the order as-is without this reconciliation (see
// internal/api/discover_row_order.go's doc comment), so every reader applies
// it the same way.
export function mergeRowOrder(
  storedKeys: string[],
  knownKeys: string[],
): string[] {
  const known = new Set(knownKeys);
  const ordered = storedKeys.filter((k) => known.has(k));
  const seen = new Set(ordered);
  for (const k of knownKeys) {
    if (!seen.has(k)) ordered.push(k);
  }
  return ordered;
}
