// Adult catalog-poster picker. GET lists stash-box/TPDB images for one
// library_scenes row; PUT persists the operator pick. Discover untracked
// cards have no persist target — only owned rows (library id) can edit.

import { api } from "./client";

const CATALOG_BOXES = ["tpdb", "stashdb", "fansdb"];

export function canEditAdultPoster(box?: string): boolean {
  return !!box && CATALOG_BOXES.includes(box);
}

export function fetchAdultCatalogPosters(sceneId: number): Promise<string[]> {
  return api<{ urls: string[] }>(
    `/api/modes/adult/scenes/${sceneId}/catalog-posters`,
  ).then((r) => r.urls ?? []);
}

export function setAdultScenePoster(sceneId: number, url: string): Promise<void> {
  return api<void>(`/api/modes/adult/scenes/${sceneId}/poster`, {
    method: "PUT",
    body: JSON.stringify({ url }),
  });
}
