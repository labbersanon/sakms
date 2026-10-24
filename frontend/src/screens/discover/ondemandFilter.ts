// On Demand chip filters Discover to verified NZB titles. Same PosterCard /
// AdultCard + DetailPopup as the carousels — not a second screen.
//
// Claude 2026-10-23: one card per tmdbId (series episodes collapse).
// Reason: Discover is title-shaped; episode pick lives in DetailPopup.
// Troubleshooting: per-episode On Demand cards → this unique was skipped.
// Review if: Play starts-while-downloading from those quality cells.

import type { AdultDiscoverItem, DiscoverItem, OndemandTitle } from "@dto";

export function filterOndemandTitles(
  items: OndemandTitle[],
  query?: string,
): OndemandTitle[] {
  const needle = (query ?? "").trim().toLowerCase();
  const seen = new Set<number>();
  const out: OndemandTitle[] = [];
  for (const item of items) {
    const id = item.tmdbId;
    if (id == null || id <= 0 || seen.has(id)) continue;
    seen.add(id);
    if (needle) {
      const hay = `${item.title} ${item.year ?? ""} ${item.imdbId ?? ""}`.toLowerCase();
      if (!hay.includes(needle)) continue;
    }
    out.push(item);
  }
  return out;
}

export function ondemandToDiscoverItem(
  item: OndemandTitle,
  mode: "movies" | "series",
  card?: { posterPath?: string; overview?: string },
): DiscoverItem {
  return {
    id: item.tmdbId ?? 0,
    title: item.title,
    posterPath: card?.posterPath ?? "",
    overview: card?.overview ?? "",
    releaseDate: item.year ? `${item.year}-01-01` : "",
    voteAverage: 0,
    mediaType: mode === "series" ? "tv" : "movie",
  };
}

export function ondemandToAdultItem(item: OndemandTitle): AdultDiscoverItem {
  return {
    id: String(item.tmdbId),
    title: item.title,
    studio: "",
    date: item.year ? `${item.year}-01-01` : "",
    image: "",
    durationSeconds: 0,
    rating: 0,
    source: "tpdb",
    slug: "",
  };
}
