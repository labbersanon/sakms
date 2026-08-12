// Mirrors internal/naming/naming.go for Rename card previews only — the
// backend remains authoritative at Apply time.
import type { Mode } from "./api/discover";
import type { Proposal } from "@dto";

export type NamingPreset = "jellyfin" | "legacy";

type RenameProposal = Proposal & { tmdbId?: number };

export function safePathComponent(s: string): string {
  return s.replace(/\//g, "-").replace(/\\/g, "-").replace(/\0/g, "_");
}

function fileExt(p: Proposal): string {
  const name = p.sourcePath?.split("/").pop() || p.sourceName;
  const dot = name.lastIndexOf(".");
  return dot >= 0 ? name.slice(dot) : "";
}

function movieFolderName(
  preset: NamingPreset,
  title: string,
  year: number,
  tmdbId: number,
): string {
  let name = safePathComponent(title);
  if (year) name = `${name} (${year})`;
  if (preset === "jellyfin" && tmdbId > 0) {
    name = `${name} [tmdbid-${tmdbId}]`;
  }
  return name;
}

export function movieFileName(
  preset: NamingPreset,
  title: string,
  year: number,
  tmdbId: number,
  ext: string,
): string {
  return movieFolderName(preset, title, year, tmdbId) + ext;
}

export function episodeFileName(
  preset: NamingPreset,
  seriesTitle: string,
  seasonNumber: number,
  episodeNumber: number,
  episodeTitle: string,
  ext: string,
): string {
  const series = safePathComponent(seriesTitle);
  const epTitle = safePathComponent(episodeTitle);
  let base: string;
  if (preset === "legacy") {
    base = `${series} - S${String(seasonNumber).padStart(2, "0")}E${String(episodeNumber).padStart(2, "0")}`;
    if (epTitle) base = `${base} - ${epTitle}`;
  } else {
    base = `${series} S${String(seasonNumber).padStart(2, "0")}E${String(episodeNumber).padStart(2, "0")}`;
    if (epTitle) base = `${base} ${epTitle}`;
  }
  return base + ext;
}

export function episodeRangeFileName(
  preset: NamingPreset,
  seriesTitle: string,
  seasonNumber: number,
  episodeNumbers: number[],
  episodeTitle: string,
  ext: string,
): string {
  if (episodeNumbers.length < 2) {
    const episodeNumber = episodeNumbers.length === 1 ? episodeNumbers[0] : 0;
    return episodeFileName(
      preset,
      seriesTitle,
      seasonNumber,
      episodeNumber,
      episodeTitle,
      ext,
    );
  }
  const first = episodeNumbers[0];
  const last = episodeNumbers[episodeNumbers.length - 1];
  const series = safePathComponent(seriesTitle);
  const epTitle = safePathComponent(episodeTitle);
  let base: string;
  if (preset === "legacy") {
    base = `${series} - S${String(seasonNumber).padStart(2, "0")}E${String(first).padStart(2, "0")}-E${String(last).padStart(2, "0")}`;
    if (epTitle) base = `${base} - ${epTitle}`;
  } else {
    base = `${series} S${String(seasonNumber).padStart(2, "0")}E${String(first).padStart(2, "0")}-E${String(last).padStart(2, "0")}`;
    if (epTitle) base = `${base} ${epTitle}`;
  }
  return base + ext;
}

export function adultFileName(
  studio: string,
  title: string,
  date: string,
  phash: string,
  ext: string,
): string {
  let name = safePathComponent(title);
  if (studio) name = `${safePathComponent(studio)} - ${name}`;
  if (date) name = `${name} (${date})`;
  if (phash) name = `${name} [phash-${phash}]`;
  return name + ext;
}

export function proposedFileName(
  mode: Mode,
  preset: NamingPreset,
  p: Proposal,
): string {
  if (p.status !== "pending" || !p.title) return "";
  const ext = fileExt(p);
  const wire = p as RenameProposal;
  if (mode === "movies") {
    return movieFileName(preset, p.title, p.year ?? 0, wire.tmdbId ?? 0, ext);
  }
  if (mode === "series") {
    if (p.seasonNumber == null || p.episodeNumber == null) return "";
    const nums = [p.episodeNumber, ...(p.extraEpisodeNumbers ?? [])];
    return episodeRangeFileName(
      preset,
      p.title,
      p.seasonNumber,
      nums,
      "",
      ext,
    );
  }
  return adultFileName(p.studio ?? "", p.title, p.date ?? "", p.phash ?? "", ext);
}
