// Adult Discover performer/studio monitoring API — GET monitor state, PUT
// toggle, and resolve the monitored-row strip. Each call goes through api()
// (src/api/client.ts) so it inherits the session cookie and the global
// 401 → re-boot session-expiry fallback.

import type { AdultMonitorState, SetAdultMonitorRequest } from "@dto";
import { api } from "./client";
import type { AdultNewestReleaseItem } from "./adultNewestRows";

export type { AdultMonitorState, SetAdultMonitorRequest };

// fetchMonitorState fetches the current monitoring state for one performer or
// studio. Never throws on a "can't resolve" state — that is a 200 with
// resolved:false and a human-readable reason string, not an error response.
export function fetchMonitorState(
  kind: "performer" | "studio",
  name: string,
): Promise<AdultMonitorState> {
  return api<AdultMonitorState>(
    `/api/modes/adult/discover/monitor?kind=${encodeURIComponent(kind)}&name=${encodeURIComponent(name)}`,
  );
}

// setMonitored toggles monitoring for one performer or studio.
// Rejects with ApiError(409) when monitored:true but the entity can't resolve.
export function setMonitored(body: SetAdultMonitorRequest): Promise<void> {
  return api<void>("/api/modes/adult/discover/monitor", {
    method: "PUT",
    body: JSON.stringify(body),
  });
}

// fetchMonitoredRow resolves the paginated monitored-entities strip for the
// browse page — GET /api/modes/adult/newest-rows/monitored/resolve?page={n}.
// Returns the same AdultNewestReleaseItem shape every other newest-row strip
// uses, so it wires directly into PaginatedStrip via toAdultDiscoverItem.
export function fetchMonitoredRow(
  page = 1,
): Promise<AdultNewestReleaseItem[]> {
  return api<AdultNewestReleaseItem[]>(
    `/api/modes/adult/newest-rows/monitored/resolve?page=${page}`,
  );
}
