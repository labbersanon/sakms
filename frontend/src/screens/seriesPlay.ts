// headerPlayFromSeasons picks Play Show / Resume Show from GET .../seasons.
// Claude 2026-09-24: skip Specials unless that is all that exists; in-progress wins (Resume).
// Reason: Play Show should start at S01E01, not S00 extras.
// Troubleshooting: GET /tracked has no series videoUrl — a show is not one file.
// Review if: Next Up uses a separate rule from first unwatched.

import type { SeasonEpisode, SeasonState } from "@dto";

export type HeaderPlay = {
  src: string;
  resume: boolean;
  episodeId: number;
};

export const episodePlaySrc = (ep: SeasonEpisode): string => {
  if ((ep.videoUrl ?? "").trim()) return ep.videoUrl ?? "";
  const file = (ep.files ?? []).find((f) => (f.videoUrl ?? "").trim());
  return file?.videoUrl ?? "";
};

export const seasonHasPlayable = (season: SeasonState): boolean =>
  (season.episodes ?? []).some((ep) => episodePlaySrc(ep) !== "");

export const isInProgress = (ep: SeasonEpisode): boolean =>
  !ep.watched && (ep.positionSeconds ?? 0) > 0 && episodePlaySrc(ep) !== "";

const walkEpisodes = (
  seasons: SeasonState[],
  pred: (ep: SeasonEpisode) => boolean,
): SeasonEpisode | undefined => {
  const sorted = [...seasons].sort((a, b) => a.seasonNumber - b.seasonNumber);
  for (const season of sorted) {
    const eps = [...(season.episodes ?? [])].sort(
      (a, b) => a.episodeNumber - b.episodeNumber,
    );
    for (const ep of eps) {
      if (pred(ep)) return ep;
    }
  }
  return undefined;
};

const pickAcrossSpecials = (
  seasons: SeasonState[],
  pred: (ep: SeasonEpisode) => boolean,
): SeasonEpisode | undefined => {
  const regular = seasons.filter((s) => s.seasonNumber > 0);
  return walkEpisodes(regular, pred) ?? walkEpisodes(seasons, pred);
};

export const headerPlayFromSeasons = (
  seasons: SeasonState[],
): HeaderPlay | null => {
  const toPlay = (ep: SeasonEpisode, resume: boolean): HeaderPlay => ({
    src: episodePlaySrc(ep),
    resume,
    episodeId: ep.id ?? 0,
  });
  const inProgress = pickAcrossSpecials(seasons, isInProgress);
  if (inProgress) return toPlay(inProgress, true);
  const unwatched = pickAcrossSpecials(
    seasons,
    (ep) => !ep.watched && episodePlaySrc(ep) !== "",
  );
  if (unwatched) return toPlay(unwatched, false);
  const any = pickAcrossSpecials(seasons, (ep) => episodePlaySrc(ep) !== "");
  if (any) return toPlay(any, false);
  return null;
};

export const firstPlayableEpisodeSrc = (seasons: SeasonState[]): string =>
  headerPlayFromSeasons(seasons)?.src ?? "";

// defaultSeasonNumber is the chip that opens first: first regular season
// with a playable file, else first regular season, else Specials / first row.
export const defaultSeasonNumber = (seasons: SeasonState[]): number => {
  if (seasons.length === 0) return 1;
  const regular = seasons.filter((s) => s.seasonNumber > 0);
  const playable = regular.find(seasonHasPlayable);
  if (playable) return playable.seasonNumber;
  if (regular.length > 0) {
    return [...regular].sort((a, b) => a.seasonNumber - b.seasonNumber)[0]!
      .seasonNumber;
  }
  return [...seasons].sort((a, b) => a.seasonNumber - b.seasonNumber)[0]!
    .seasonNumber;
};

export const seasonChipLabel = (n: number): string =>
  n === 0 ? "Specials" : `Season ${n}`;

export const episodeCode = (seasonNumber: number, episodeNumber: number): string =>
  `S${String(seasonNumber).padStart(2, "0")}E${String(episodeNumber).padStart(2, "0")}`;
