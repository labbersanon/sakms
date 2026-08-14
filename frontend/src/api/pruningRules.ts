// Claude 2026-08-03: new file — data access for propose-only pruning rules
// (F1, plan .omc/plans/autopilot-impl-pruning-rules.md §6.4 + §13.1).
// Reason: mirrors src/api/discoverSliders.ts's CRUD shape (re-exports the
// generated DTOs from @dto rather than hand-duplicating them) plus the
// preview endpoint (§13.1) and the purge-scan-interval GET/PUT, which has no
// generated DTO — same pattern as fetchAdultNewestScanInterval in
// src/api/settings.ts. Every call goes through api() (src/api/client.ts) so
// it inherits the session cookie and the global 401 -> re-boot fallback.
// Troubleshooting: none yet.

import { api } from "./client";
import type {
  PruningCriterion,
  PruningRule,
  PruningRulePreviewResponse,
  PruningRuleUpsertRequest,
} from "@dto";

export type {
  PruningCriterion,
  PruningRule,
  PruningRulePreviewResponse,
  PruningRuleUpsertRequest,
};

// PRUNING_TIER_FLOORS mirrors internal/pruning's tierRank ladder (low ->
// lossless) — the only four values the backend's ErrInvalidTierFloor accepts
// as a non-empty QualityTierFloor. "unknown" (the backfill sentinel) is
// deliberately absent: it is never a valid floor.
export const PRUNING_TIER_FLOORS = ["low", "medium", "high", "lossless"] as const;
export type PruningTierFloor = (typeof PRUNING_TIER_FLOORS)[number];

export const PRUNING_TIER_FLOOR_LABELS: Record<PruningTierFloor, string> = {
  low: "Low",
  medium: "Medium",
  high: "High",
  lossless: "Lossless",
};

// GB_BYTES is the 1024-based conversion the size condition's GB input uses,
// matching internal/pruning's humanBytes convention (1024 == 1KB, not 1000).
export const GB_BYTES = 1024 ** 3;

export function fetchPruningRules(): Promise<PruningRule[]> {
  return api<PruningRule[]>("/api/pruning-rules");
}

export function createPruningRule(
  body: PruningRuleUpsertRequest,
): Promise<PruningRule> {
  return api<PruningRule>("/api/pruning-rules", {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export function updatePruningRule(
  id: number,
  body: PruningRuleUpsertRequest,
): Promise<PruningRule> {
  return api<PruningRule>(`/api/pruning-rules/${id}`, {
    method: "PUT",
    body: JSON.stringify(body),
  });
}

export function deletePruningRule(id: number): Promise<void> {
  return api<void>(`/api/pruning-rules/${id}`, { method: "DELETE" });
}

// previewPruningRule is the §13.1 soft-warning count: how many library
// subjects for body.mode would match this rule alone, right now. Never
// stages proposals and never requires the rule to exist yet — body is the
// same upsert shape create/update use, id-less.
export function previewPruningRule(
  body: PruningRuleUpsertRequest,
): Promise<number> {
  return api<PruningRulePreviewResponse>("/api/pruning-rules/preview", {
    method: "POST",
    body: JSON.stringify(body),
  }).then((r) => r.matchCount);
}

// Purge's background-scan cadence in whole seconds (0 = off, opt-in — the
// same convention every scan-scheduler interval in this app uses). No
// generated DTO for this endpoint (the Go handler uses a local
// request/response struct, internal/api/scanschedule_settings.go), so the
// wire shape is inlined here — same pattern as
// fetchAdultNewestScanInterval/putAdultNewestScanInterval in
// src/api/settings.ts.
export function fetchPurgeScanInterval(): Promise<number> {
  return api<{ intervalSeconds: number }>(
    "/api/settings/purge-scan-interval",
  ).then((r) => r.intervalSeconds);
}

export function putPurgeScanInterval(intervalSeconds: number): Promise<void> {
  return api<void>("/api/settings/purge-scan-interval", {
    method: "PUT",
    body: JSON.stringify({ intervalSeconds }),
  });
}
