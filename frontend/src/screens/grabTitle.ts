import type { Grab } from "@dto";

// Claude 2026-10-22: Activity/History label matches catalogTitleFromGrab.
// Reason: Grab.title is the show name; season/episode live in sibling fields
//   so Furious S01E05 rendered as "Furious" after Newznab grabs.
// Troubleshooting: no SxxExx → seasonSpecified false. Missing · name →
//   episodeTitle empty on the grab JSON. Review if: Title stores the label.
export function grabDisplayTitle(g: Grab): string {
  const title = (g.title ?? "").trim();
  if (!title) return "";
  let out = title;
  if (g.seasonSpecified) {
    const season = String(g.seasonNumber ?? 0).padStart(2, "0");
    const episode = g.episodeNumber ?? 0;
    out =
      episode > 0
        ? `${title} S${season}E${String(episode).padStart(2, "0")}`
        : `${title} S${season}`;
  }
  const ep = (g.episodeTitle ?? "").trim();
  if (ep && ep.toLowerCase() !== title.toLowerCase()) {
    return `${out} · ${ep}`;
  }
  return out;
}
