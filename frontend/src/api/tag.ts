// Tag workflow data access (Stage 3, last of the four). Ported verbatim from
// the vanilla-JS frontend (internal/web/static/index.html's renderTag). The Tag
// workflow is DELIBERATELY SIMPLER than Rename/Purge/Dedup: no staged
// scan→propose→apply queue, just direct CRUD on a tracked item's tags. Two GETs
// back the view — a tag vocabulary (autocomplete) and the tracked items each
// carrying their current tags — and add/remove act immediately on one item.
//
// CRITICAL per-mode endpoint split (confirmed against internal/api/tag.go, NOT
// a guess): Movies/Series use the GENERIC item-tag routes; Adult uses its OWN
// DEDICATED scene-tag routes. The generic routes 400 for Adult (Whisparr
// eliminated — Adult tags are scene-level now), so a single generic Tag UI
// pointing every mode at the same URL shape would break Adult outright. Only the
// URL differs per mode; the request/response wire shapes are identical, so the
// component is mode-agnostic and only these functions branch:
//
//   vocabulary  Movies/Series: GET /api/modes/{mode}/tags
//               Adult:         GET /api/modes/adult/scenes/tags
//   add         Movies/Series: POST /api/modes/{mode}/items/{itemId}/tags
//               Adult:         POST /api/modes/adult/scenes/{sceneId}/tags
//   remove      Movies/Series: DELETE /api/modes/{mode}/items/{itemId}/tags/{tag}
//               Adult:         DELETE /api/modes/adult/scenes/{sceneId}/tags/{tag}
//
// tracked items come from ONE shared route for every mode
// (GET /api/modes/{mode}/tracked) — for Adult that route returns library_scenes
// rows whose id IS the {sceneId} the scene-tag routes take, so no separate
// scene-listing call is needed.
//
// Every call goes through api() (src/api/client.ts) so it inherits the session
// cookie and the global 401 → re-boot session-expiry fallback. Response/request
// shapes are the generated DTOs (@dto), never hand-duplicated (plan Guardrail #4).

import { api } from "./client";
import type { TagEntry, TrackedItem } from "@dto";
import type { Mode } from "./discover";

export type { TagEntry, TrackedItem };

// fetchTagVocabulary lists a mode's existing tag labels for the add-tag
// autocomplete. Adult routes to its dedicated scene-tag vocabulary
// (/scenes/tags); the generic /tags route 400s for Adult (see tag.go).
export function fetchTagVocabulary(mode: Mode): Promise<TagEntry[]> {
  const path =
    mode === "adult"
      ? "/api/modes/adult/scenes/tags"
      : `/api/modes/${mode}/tags`;
  return api<TagEntry[]>(path);
}

// fetchTrackedItems lists what the mode currently tracks (items/series/scenes),
// each with its current tags — the Tag workflow's item picker. One shared route
// for every mode; for Adult the returned id is a library_scenes.id.
export function fetchTrackedItems(mode: Mode): Promise<TrackedItem[]> {
  return api<TrackedItem[]>(`/api/modes/${mode}/tracked`);
}

// addTag assigns one label to one tracked item — immediate, not staged. Adult
// routes to its dedicated scene-tag endpoint (/scenes/{sceneId}/tags); the
// generic /items/{itemId}/tags route 400s for Adult (see tag.go).
export function addTag(
  mode: Mode,
  itemId: number,
  label: string,
): Promise<void> {
  const path =
    mode === "adult"
      ? `/api/modes/adult/scenes/${itemId}/tags`
      : `/api/modes/${mode}/items/${itemId}/tags`;
  return api<void>(path, { method: "POST", body: JSON.stringify({ label }) });
}

// removeTag unassigns one label from one tracked item. The {tagId} path segment
// is the tag LABEL itself (a local tag has no numeric id — see tag.go's
// removeItemTagHandler/removeSceneTagHandler), encoded because a label is a free
// string that may contain slashes/spaces. Adult routes to its dedicated scene
// endpoint; the generic route 400s for Adult.
export function removeTag(
  mode: Mode,
  itemId: number,
  label: string,
): Promise<void> {
  const tag = encodeURIComponent(label);
  const path =
    mode === "adult"
      ? `/api/modes/adult/scenes/${itemId}/tags/${tag}`
      : `/api/modes/${mode}/items/${itemId}/tags/${tag}`;
  return api<void>(path, { method: "DELETE" });
}
