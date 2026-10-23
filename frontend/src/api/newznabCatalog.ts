// Newznab catalog overlap for the Dashboard Dock card. Polls
// GET /api/admin/newznab-catalog. Enclosure URLs are never in this payload.

import { api } from "./client";
import type { NewznabCatalogReport } from "@dto";

export type { NewznabCatalogReport };

export function fetchNewznabCatalog(): Promise<NewznabCatalogReport> {
  return api<NewznabCatalogReport>("/api/admin/newznab-catalog");
}
