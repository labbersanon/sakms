// NNTP crawler snapshot for the Dashboard card. Goes through api() so it
// inherits the session cookie and 401 → re-boot fallback.

import { api } from "./client";
import type { NNTPCrawlerStatus } from "@dto";

export type { NNTPCrawlerStatus };

export function fetchNNTPCrawlerStatus(): Promise<NNTPCrawlerStatus> {
  return api<NNTPCrawlerStatus>("/api/admin/nntp-crawler");
}
