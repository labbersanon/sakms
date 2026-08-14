// Operator star-rating writes for Library cards and the detail panel.
// Movies/Series use the generic item route; Adult uses the dedicated scene
// route (the generic /items/{id}/rating path 400s for Adult, same split as
// tags). GET /tracked already returns the current rating — there is no
// catalog lookup here.

import { api } from "./client";
import type { Mode } from "./discover";

export function setItemRating(
  mode: Mode,
  itemId: number,
  rating: number,
): Promise<void> {
  const path =
    mode === "adult"
      ? `/api/modes/adult/scenes/${itemId}/rating`
      : `/api/modes/${mode}/items/${itemId}/rating`;
  return api<void>(path, {
    method: "PUT",
    body: JSON.stringify({ rating }),
  });
}
