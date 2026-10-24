// GET /api/modes/{mode}/ondemand. Verified NZBs only; nzbId is a hash, never a URL.
// POST /api/modes/{mode}/ondemand/play starts a staging-only download.

import { api } from "./client";
import type { OndemandList, OndemandPlayStatus } from "@dto";
import type { Mode } from "./discover";

export function fetchOndemand(mode: Mode): Promise<OndemandList> {
  return api<OndemandList>(`/api/modes/${mode}/ondemand`);
}

export function startOndemandPlay(
  mode: Mode,
  indexer: string,
  nzbId: string,
): Promise<OndemandPlayStatus> {
  return api<OndemandPlayStatus>(`/api/modes/${mode}/ondemand/play`, {
    method: "POST",
    body: JSON.stringify({ indexer, nzbId }),
  });
}

export function fetchOndemandPlay(
  mode: Mode,
  gid: string,
): Promise<OndemandPlayStatus> {
  return api<OndemandPlayStatus>(`/api/modes/${mode}/ondemand/play/${gid}`);
}
