// Claude 2026-09-29: Organize Import client — Movies, Series, and Adult.
// Reason: scan/apply stay off the proposals queue; confirm-then-MOVE.
//   Adult reconstructs box/sceneId/phash so apply can RelocateAdultScene.
// Troubleshooting: 400 path must be a mounted root; Adult needs a library root.
// Review if: scan results are queued.

import { api } from "./client";
import type { Mode } from "./discover";

export type ImportMode = Mode;

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
  box?: string;
  sceneId?: string;
  studio?: string;
  date?: string;
  phash?: string;
  durationSeconds?: number;
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
