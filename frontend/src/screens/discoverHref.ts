// Discover URL helpers for the Library merge.
//
// Claude 2026-09-24: Library is no longer a sidebar page. Owned catalog is
//   Discover ?view=library. /library* bookmarks redirect here.
// Reason: one page, In library filter, Dashboard/View-all deep links.
// Troubleshooting: a leftover /library href still opens a dedicated page.
// Review if: nested Discover paths replace query params.
// Related files: AppShell.tsx, Dashboard.tsx, discover/index.tsx

import type { DiscoverItem } from "../api/discover";
import type { TrackedItem } from "../api/tag";
import type { AdultMediaTab, MainstreamMediaTab, MediaSection } from "./mediaNav";

export type DiscoverSearchHit =
  | { kind: "catalog"; mode: "movies" | "series"; item: DiscoverItem }
  | { kind: "owned"; mode: "movies" | "series"; item: TrackedItem };

// mergeOwnedCatalogSearch: one Discover box, owned wins on tmdbId, leftover
// title matches stay. Catalog rows that share a tmdbId collapse to one owned
// card so the operator never sees grab + play for the same title.
export function mergeOwnedCatalogSearch(
  q: string,
  catalog: { mode: "movies" | "series"; item: DiscoverItem }[],
  tracked: TrackedItem[],
  trackedMode: "movies" | "series",
): DiscoverSearchHit[] {
  const needle = q.trim().toLowerCase();
  const ownedByTmdb = new Map<number, TrackedItem>();
  for (const item of tracked) {
    const id = item.tmdbId ?? 0;
    if (id > 0) ownedByTmdb.set(id, item);
  }
  const seen = new Set<number>();
  const out: DiscoverSearchHit[] = [];
  for (const row of catalog) {
    const owned = ownedByTmdb.get(row.item.id);
    if (owned) {
      if (seen.has(owned.id)) continue;
      out.push({ kind: "owned", mode: row.mode, item: owned });
      seen.add(owned.id);
      continue;
    }
    out.push({ kind: "catalog", mode: row.mode, item: row.item });
  }
  for (const item of tracked) {
    if (seen.has(item.id)) continue;
    if (!item.title.toLowerCase().includes(needle)) continue;
    out.push({ kind: "owned", mode: trackedMode, item });
  }
  return out;
}

// adultOwnedIdentityKey is box:sceneId. Empty when either half is missing so
// a title-only owned match cannot hide an unrelated catalog card.
export function adultOwnedIdentityKey(
  box: string | undefined,
  id: string | undefined,
): string {
  const b = (box ?? "").trim();
  const scene = (id ?? "").trim();
  return b && scene ? `${b}:${scene}` : "";
}

export const DISCOVER_VIEW_LIBRARY = "library";

export function discoverOwnedHref(
  section: MediaSection,
  opts: { tab?: string; tier?: string } = {},
): string {
  const q = new URLSearchParams();
  q.set("view", DISCOVER_VIEW_LIBRARY);
  const tab = (opts.tab ?? "").trim();
  if (tab) q.set("tab", tab);
  const tier = (opts.tier ?? "").trim();
  if (tier) q.set("tier", tier);
  return `/discover/${section}?${q.toString()}`;
}

// libraryPathToDiscover maps retired /library* URLs onto Discover owned view.
// tab/mode and tier query keys are preserved so Dashboard storage cells and
// old bookmarks still land on the same grid.
export function libraryPathToDiscover(pathname: string, search: string): string {
  const raw = search.startsWith("?") ? search.slice(1) : search;
  const params = new URLSearchParams(raw);
  const section: MediaSection = pathname.includes("/adult") ? "adult" : "mainstream";
  const tab = params.get("tab") || params.get("mode") || "";
  const tier = params.get("tier") || "";
  return discoverOwnedHref(section, { tab, tier });
}

export function isDiscoverOwnedView(raw: string | undefined): boolean {
  return raw === DISCOVER_VIEW_LIBRARY;
}

export function sanitizeMainstreamTab(
  raw: string | undefined,
): MainstreamMediaTab {
  return raw === "movies" ? "movies" : "series";
}

export function sanitizeAdultTab(raw: string | undefined): AdultMediaTab {
  return raw === "movies" ? "movies" : "scenes";
}
