// Per-title quality prefs (series monitor + movie track).

import { api } from "./client";
import type { Mode } from "./discover";

export type TitleQualityPrefsResponse = {
  tiers: string[];
  maxResolution: number;
  inherited: boolean;
  upgradeQueued?: number;
};

export type TitleQualityPrefsRequest = {
  tiers: string[];
  maxResolution: number;
  clear?: boolean;
};

export type TitleQualityKey =
  | { seriesID: number; tmdbId?: undefined }
  | { tmdbId: number; seriesID?: undefined };

function prefsPath(mode: Mode, key: TitleQualityKey): string {
  if (mode === "series") {
    if (key.seriesID != null) {
      return `/api/modes/series/library/${key.seriesID}/quality-prefs`;
    }
    return `/api/modes/series/library/tmdb/${key.tmdbId}/quality-prefs`;
  }
  return `/api/modes/movies/library/tmdb/${key.tmdbId}/quality-prefs`;
}

export function fetchTitleQualityPrefs(
  mode: Mode,
  key: TitleQualityKey,
): Promise<TitleQualityPrefsResponse> {
  return api<TitleQualityPrefsResponse>(prefsPath(mode, key));
}

export function putTitleQualityPrefs(
  mode: Mode,
  key: TitleQualityKey,
  body: TitleQualityPrefsRequest,
): Promise<TitleQualityPrefsResponse> {
  return api<TitleQualityPrefsResponse>(prefsPath(mode, key), {
    method: "PUT",
    body: JSON.stringify(body),
  });
}
