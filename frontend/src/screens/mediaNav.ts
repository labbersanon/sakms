import type { TabDef } from "../components/ui";

export type MediaRoot = "library" | "discover";
export type MediaSection = "mainstream" | "adult";
export type MainstreamMediaTab = "series" | "movies";
export type AdultMediaTab = "scenes" | "movies";

export const MEDIA_NAV_EXPANDED_KEY: Record<MediaRoot, string> = {
  library: "sakms.nav.library.expanded",
  discover: "sakms.nav.discover.expanded",
};

export const MEDIA_SECTIONS: { id: MediaSection; label: string }[] = [
  { id: "mainstream", label: "Mainstream" },
  { id: "adult", label: "Adult" },
];

export const MAINSTREAM_MEDIA_TABS: TabDef[] = [
  { id: "series", label: "Series" },
  { id: "movies", label: "Movies" },
];

export const ADULT_MEDIA_TABS: TabDef[] = [
  { id: "scenes", label: "Scenes" },
  { id: "movies", label: "Movies" },
];

export function mediaSectionHref(root: MediaRoot, section: MediaSection): string {
  return `/${root}/${section}`;
}

export function isMediaSection(value: string | undefined): value is MediaSection {
  return value === "mainstream" || value === "adult";
}
