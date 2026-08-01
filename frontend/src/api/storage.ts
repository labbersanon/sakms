// Storage allocation data access — the tracked library's byte totals and item
// counts split by mode and quality tier, backing the Dashboard's Storage
// Allocation table. Every call goes through api() (src/api/client.ts) so it
// inherits the session cookie and the global 401 → re-boot session-expiry
// fallback. Request/response shapes are the generated DTOs (@dto), never
// hand-duplicated.

import { api } from "./client";
import type { StorageAllocation } from "@dto";

export type { StorageAllocation };

// fetchStorageAllocation returns the dense mode x tier grid (always 3 rows x 5
// tier cells, zero cells included). Server-side this is one grouped SQL query
// over columns captured at write time — no disk I/O, no external calls.
export function fetchStorageAllocation(): Promise<StorageAllocation> {
  return api<StorageAllocation>("/api/admin/storage-allocation");
}
