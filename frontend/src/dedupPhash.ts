// Claude 2026-08-21: pair-level phash match for Dedup VMAF gating.
// Reason: group pHashSimilarity is the worst pair in a TMDB-identity group
// (crop vs open-matte can sit at ~48%), so it must not decide whether THIS
// candidate vs the VMAF reference is the same picture. Mirrors
// internal/vmaf.ShouldScoreVMAF / phash.SimilarityWithin using the factory
// mode defaults; the API re-checks with the operator's stored threshold.
// Troubleshooting: identity-only tiles still polling /vmaf means Dedup.tsx
// is not calling phashMatches, or candidate.phash is missing from the wire.
// Review if: internal/phash.Frames, DefaultMoviesThreshold, or
// DefaultThreshold change.

import type { Mode } from "./api/discover";

export const PHASH_FRAMES = 5;
export const PHASH_THRESHOLD_MOVIES = 64;
export const PHASH_THRESHOLD_DEFAULT = 40;

export function phashThresholdForMode(mode: Mode): number {
  return mode === "movies" ? PHASH_THRESHOLD_MOVIES : PHASH_THRESHOLD_DEFAULT;
}

export function phashMatches(
  a: string | undefined,
  b: string | undefined,
  mode: Mode,
): boolean {
  if (!a || !b) return false;
  const ia = a.indexOf(":");
  const ib = b.indexOf(":");
  if (ia < 0 || ib < 0) return false;
  if (a.slice(0, ia) !== b.slice(0, ib)) return false;
  const ha = a.slice(ia + 1);
  const hb = b.slice(ib + 1);
  if (ha.length !== hb.length || ha.length % 2 !== 0) return false;
  let bits = 0;
  for (let i = 0; i < ha.length; i += 2) {
    const x =
      parseInt(ha.slice(i, i + 2), 16) ^ parseInt(hb.slice(i, i + 2), 16);
    if (Number.isNaN(x)) return false;
    bits += popcount8(x);
  }
  return bits <= phashThresholdForMode(mode) * PHASH_FRAMES;
}

function popcount8(x: number): number {
  let n = 0;
  let v = x;
  while (v) {
    n += v & 1;
    v >>= 1;
  }
  return n;
}
