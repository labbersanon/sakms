// Claude 2026-09-28: Organize Import client.
// Reason: scan/apply stay off the proposals queue; confirm-then-MOVE.
// Troubleshooting: 400 path must be a mounted root; Adult is refused.
// Review if: Adult import is added.

import { api } from "./client";
import type { Mode } from "./discover";

export type ImportMode = Exclude<Mode, "adult">;

export type ManualImportItem = {
  sourcePath: string;
  sourceName: string;
  destPath?: string;
  destRoot?: string;
  title?: string;
  year?: number;
  tmdbId?: number;
  seasonNumber?: number;
  episodeNumber?: number;
  extraEpisodeNumbers?: number[];
  status: string;
  reason?: string;
  mode: string;
};

export type ManualImportScanResponse = {
  path: string;
  destRoot: string;
  items: ManualImportItem[];
};

export type ManualImportApplyResult = {
  sourcePath: string;
  destPath?: string;
  ok: boolean;
  error?: string;
};

export type ManualImportApplyResponse = {
  results: ManualImportApplyResult[];
};

export function scanManualImport(
  mode: ImportMode,
  path: string,
): Promise<ManualImportScanResponse> {
  return api<ManualImportScanResponse>("/api/organize/import/scan", {
    method: "POST",
    body: JSON.stringify({ mode, path }),
  });
}

export function applyManualImport(
  mode: ImportMode,
  items: ManualImportItem[],
): Promise<ManualImportApplyResponse> {
  return api<ManualImportApplyResponse>("/api/organize/import/apply", {
    method: "POST",
    body: JSON.stringify({ mode, items }),
  });
}
