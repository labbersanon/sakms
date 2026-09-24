// Claude 2026-09-24: Series-only play-head write; Movies/Adult do not call this.
// Reason: TrackedPlayback timeupdate/pause/ended need a throttled PUT.
// Troubleshooting: GET lives on .../seasons (positionSeconds); this path is write-only.
// Review if: Movies/Adult start sharing this helper.

import { api } from "./client";
import type { EpisodeProgressRequest } from "@dto";

export function putEpisodeProgress(
  seriesID: number,
  episodeId: number,
  body: EpisodeProgressRequest,
): Promise<void> {
  return api<void>(
    `/api/modes/series/tracked/${seriesID}/episodes/${episodeId}/progress`,
    { method: "PUT", body: JSON.stringify(body) },
  );
}
