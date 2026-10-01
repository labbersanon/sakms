// Claude 2026-10-01: DELETE /api/modes/{mode}/tracked/{id}.
// Reason: owned DetailPopup remove-from-library; movies/adult ignore the body.
// Review if: series seasonal delete grows a dedicated route.

import { api } from "./client";
import type { RemoveTrackedRequest, RemoveTrackedResponse } from "@dto";

export function deleteTracked(
  mode: "movies" | "series" | "adult",
  id: number,
  body?: RemoveTrackedRequest,
): Promise<RemoveTrackedResponse> {
  return api<RemoveTrackedResponse>(`/api/modes/${mode}/tracked/${id}`, {
    method: "DELETE",
    body: body ? JSON.stringify(body) : undefined,
  });
}
