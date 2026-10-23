// GET /api/modes/{mode}/ondemand. Verified NZBs only; nzbId is a hash, never a URL.

import { api } from "./client";
import type { OndemandList } from "@dto";
import type { Mode } from "./discover";

export function fetchOndemand(mode: Mode): Promise<OndemandList> {
  return api<OndemandList>(`/api/modes/${mode}/ondemand`);
}
