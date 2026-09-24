// Claude 2026-09-24: SearchTakeover fourth caller. PUT /tracked/{id}/identity.
// Reason: wrong catalog match stays a library row with a new tmdb/box key.
// Review if: Rematch starts rewriting file names on disk.

import { api } from "./client";
import type { TrackedIdentityRequest, TrackedItem } from "@dto";
import type { TakeoverPick } from "../screens/SearchTakeover";

export function identityFromPick(pick: TakeoverPick): TrackedIdentityRequest {
  if (pick.kind === "adult") {
    return {
      title: pick.title,
      box: pick.box,
      sceneId: pick.sceneId,
      studio: pick.studio,
      date: pick.date,
    };
  }
  return { tmdbId: pick.tmdbId, title: pick.title, year: pick.year };
}

export function applyPickToTracked(
  item: TrackedItem,
  pick: TakeoverPick,
): TrackedItem {
  const identity = identityFromPick(pick);
  return { ...item, ...identity, year: identity.year ?? item.year };
}

export function replaceSlotFromPick(
  pick: TakeoverPick,
): { season: number; episode: number } | null {
  if (
    pick.kind !== "catalog" ||
    pick.seasonNumber == null ||
    pick.episodeNumber == null
  ) {
    return null;
  }
  return { season: pick.seasonNumber, episode: pick.episodeNumber };
}

export function putTrackedIdentity(
  mode: "movies" | "series" | "adult",
  id: number,
  body: TrackedIdentityRequest,
): Promise<void> {
  return api<void>(`/api/modes/${mode}/tracked/${id}/identity`, {
    method: "PUT",
    body: JSON.stringify(body),
  });
}
