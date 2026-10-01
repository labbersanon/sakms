// Mirrors internal/searchterm and Adult CleanReleaseTitleForSearch so Rename
// Search can prefill the query Scan would send, not the raw filename or the
// (possibly wrong) catalog title.

import type { Mode } from "./api/discover";

const VIDEO_EXTS = new Set([
  ".3gp",
  ".asf",
  ".avi",
  ".divx",
  ".dvr-ms",
  ".f4v",
  ".flv",
  ".img",
  ".iso",
  ".m2t",
  ".m2ts",
  ".m2v",
  ".m4v",
  ".mk3d",
  ".mkv",
  ".mov",
  ".mp4",
  ".mpg",
  ".mpeg",
  ".mts",
  ".ogg",
  ".ogm",
  ".ogv",
  ".rec",
  ".ts",
  ".rmvb",
  ".vob",
  ".webm",
  ".wmv",
  ".wtv",
]);

const NOISE_TOKENS = [
  "1080p",
  "720p",
  "2160p",
  "480p",
  "4k",
  "uhd",
  "hd",
  "web-dl",
  "webdl",
  "webrip",
  "web",
  "bluray",
  "blu-ray",
  "brrip",
  "bdrip",
  "hdtv",
  "dvdrip",
  "remux",
  "x264",
  "x265",
  "h264",
  "h265",
  "hevc",
  "avc",
  "av1",
  "aac",
  "dts",
  "dts-hd",
  "ddp5",
  "atmos",
  "truehd",
  "proper",
  "repack",
  "extended",
  "unrated",
  "theatrical",
  "limited",
  "multi",
  "hdr",
  "hdr10",
  "10bit",
  "internal",
  "uncut",
  "sample",
];

const noiseTokenRe = new RegExp(`\\b(${NOISE_TOKENS.join("|")})\\b`, "gi");
const noiseTokenOneRe = new RegExp(`\\b(${NOISE_TOKENS.join("|")})\\b`, "i");
const releaseGroupRe = /-[A-Za-z0-9]+$/;
const multiSpaceRe = /\s{2,}/g;
const bracketedRe = /[\[(][^[\]()]*[\])]/g;
const dottedCodecRe = /\b([xh])\.(26[45])\b/gi;
const yearBracketRe = /\[(\d{4})\]/g;
const leadingIndexRe = /^0\d\s+/i;
const creditBeforeYearRe = /^(.+?)\s*-\s+.+\((\d{4})\)\s*$/i;
const cqNoiseRe = /\bcq\d+\b/gi;
const trailingJunkRe = /[\s-]+$/;
const yearParenStripRe = /\s*\((19\d{2}|20\d{2})\)/g;
const yearBareEndRe = /\s+\b(19\d{2}|20\d{2})\b\s*$/i;
const commaPersonRe = /^(.+?)\s+([^,]+),\s*([^,]+)$/i;
const episodeMarkerRe = /S(\d{1,2})E(\d{1,3})/i;
const altEpisodeMarkerRe = /\b(\d{1,2})x(\d{1,3})\b/i;
const yearSeasonMarkerRe = /S(\d{4})E(\d{1,3})/i;
const yearAltSeasonMarkerRe = /\b(\d{4})x(\d{1,3})\b/i;
const adultBracketedRe = /\[.*?\]|\(.*?\)/g;
const adultMediaTagsRe =
  /\b(1080p|2160p|720p|4k|hd|sd|xxx|hevc|x265|x264|aac|h264|mp4|mkv|wmv)\b/gi;
const adultPathSepRe = /[-_.]/g;
const adultDateTokenRe =
  /\b(\d{2}|\d{4})\.(0[1-9]|1[0-2])\.(0[1-9]|[12]\d|3[01])\b/g;
const adultTechMarkerRe =
  /\b(xxx|\d{3,4}p|x26[45]|h\.?26[45]|hevc|xvid|av1|web[.\-_]?dl|web[.\-_]?rip|bluray|bdrip|brrip|hdtv|dvdrip|remux)\b/i;

function isVideoExt(ext: string): boolean {
  const e = ext.trim().toLowerCase();
  if (!e) return false;
  return VIDEO_EXTS.has(e.startsWith(".") ? e : `.${e}`);
}

function fileExt(name: string): string {
  const i = name.lastIndexOf(".");
  if (i <= 0) return "";
  return name.slice(i);
}

function basename(path: string): string {
  const trimmed = path.replace(/[/\\]+$/, "");
  const i = Math.max(trimmed.lastIndexOf("/"), trimmed.lastIndexOf("\\"));
  return i >= 0 ? trimmed.slice(i + 1) : trimmed;
}

function dirname(path: string): string {
  const trimmed = path.replace(/[/\\]+$/, "");
  const i = Math.max(trimmed.lastIndexOf("/"), trimmed.lastIndexOf("\\"));
  return i >= 0 ? trimmed.slice(0, i) : "";
}

function trimSeparators(s: string): string {
  return s.trim().replace(/[.\-_ ]+$/g, "");
}

function isYearSeason(n: number): boolean {
  return n >= 1928 && n <= 1999;
}

export function fromName(name: string): string {
  const ext = fileExt(name);
  if (isVideoExt(ext)) {
    name = name.slice(0, -ext.length);
  }
  name = name.replace(dottedCodecRe, "$1$2");
  name = name.replace(yearBracketRe, "($1)");
  let s = name.replaceAll(".", " ").replaceAll("_", " ");
  s = s.replace(leadingIndexRe, "");
  const credit = creditBeforeYearRe.exec(s);
  if (credit) {
    s = `${credit[1]} (${credit[2]})`;
  }
  s = s.replace(releaseGroupRe, "");
  s = s.replace(bracketedRe, (m) => (noiseTokenOneRe.test(m) ? " " : m));
  s = s.replace(noiseTokenRe, " ");
  s = s.replace(cqNoiseRe, " ");
  s = s.replace(multiSpaceRe, " ");
  s = s.replace(trailingJunkRe, "");
  return s.trim();
}

export function stripYear(term: string): string {
  let s = term.replace(yearParenStripRe, " ");
  s = s.replace(yearBareEndRe, "");
  s = s.replace(multiSpaceRe, " ");
  return s.trim();
}

export function searchQueries(name: string): string[] {
  const base = fromName(name);
  const out: string[] = [];
  const add = (raw: string) => {
    const s = raw.trim();
    if (!s) return;
    if (out.some((e) => e.toLowerCase() === s.toLowerCase())) return;
    out.push(s);
  };
  const stripped = stripYear(base);
  add(stripped);
  add(base);
  const person = commaPersonRe.exec(stripped);
  const personTitle = person?.[1];
  const personLast = person?.[2];
  const personFirst = person?.[3];
  if (personTitle && personLast && personFirst) {
    add(personTitle.trim());
    add(`${personTitle.trim()} ${personFirst.trim()} ${personLast.trim()}`);
  }
  const dash = stripped.lastIndexOf(" - ");
  if (dash > 0) {
    add(stripped.slice(0, dash).trim());
  }
  return out;
}

export function cleanReleaseTitleForSearch(title: string): string {
  let s = title.replace(adultBracketedRe, " ");
  s = s.replace(adultDateTokenRe, " ");
  const tech = adultTechMarkerRe.exec(s);
  if (tech && tech.index !== undefined) {
    s = s.slice(0, tech.index);
  }
  s = s.replace(adultMediaTagsRe, " ");
  s = s.replace(adultPathSepRe, " ");
  s = s.replace(multiSpaceRe, " ");
  return s.trim();
}

function stripEpisodeMarker(name: string): string {
  const sxx = episodeMarkerRe.exec(name);
  if (sxx && sxx.index !== undefined) {
    return trimSeparators(name.slice(0, sxx.index));
  }
  const alt = altEpisodeMarkerRe.exec(name);
  if (alt && alt.index !== undefined) {
    return trimSeparators(name.slice(0, alt.index));
  }
  return name;
}

function stripYearSeasonMarker(name: string): string {
  const split = (loc: number) => trimSeparators(name.slice(0, loc));
  const y = yearSeasonMarkerRe.exec(name);
  if (y && y.index !== undefined && isYearSeason(Number(y[1]))) {
    return split(y.index);
  }
  const alt = yearAltSeasonMarkerRe.exec(name);
  if (alt && alt.index !== undefined && isYearSeason(Number(alt[1]))) {
    return split(alt.index);
  }
  return name;
}

function seriesSeed(name: string, sourcePath?: string): string {
  let seed = stripYearSeasonMarker(name);
  if (seed === name) {
    seed = stripEpisodeMarker(name);
  }
  if (seed !== name) {
    return seed;
  }
  if (!sourcePath) {
    return name;
  }
  const parent = basename(dirname(sourcePath));
  if (!parent) {
    return name;
  }
  const fromYear = stripYearSeasonMarker(parent);
  if (fromYear !== parent) {
    return fromYear;
  }
  const fromEp = stripEpisodeMarker(parent);
  if (fromEp !== parent) {
    return fromEp;
  }
  return name;
}

// Claude 2026-10-01: Rename Search prefills Scan's filename query, not sourceName.
// Reason: autoSearch was sending the raw file (dots, tags, extension) or the
//   current catalog title, which is the match the operator is replacing.
// Troubleshooting: Re-pick box shows Some.Movie.2021.1080p.mkv instead of Some Movie.
// Review if: proposals grow a persisted searchTerm from Scan.
export function suggestedRenameQuery(
  mode: Mode,
  sourceName: string,
  sourcePath?: string,
): string {
  const file = basename(sourceName || sourcePath || "");
  if (!file) {
    return "";
  }
  if (mode === "adult") {
    return cleanReleaseTitleForSearch(file) || fromName(file);
  }
  const seed = mode === "series" ? seriesSeed(file, sourcePath) : file;
  return searchQueries(seed)[0] || fromName(seed) || fromName(file);
}
