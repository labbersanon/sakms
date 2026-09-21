// Claude 2026-09-21: client-side lifecycle ETA for the Downloads queue.
// Reason: operators wanted remaining time (download + Usenet repair/unpack), not
//   phase-elapsed only; the engine does not emit an ETA field.
// Troubleshooting: Downloads showed NN% + elapsed during PAR2/unrar, or ↓ speed
//   while downloading, with no sense of time left.
// Review if: the engine starts emitting remaining-time estimates of its own.
//
// Claude 2026-09-21: balanced smoothness pass (byte-delta EMA + display clamp).
// Reason: wire downloadSpeed (even with a 10s engine window) still jitters; UI
//   also jerked when lastGoodEtaSec jumped. Prefer calm countdown with moderate
//   tracking of real speed changes.
// Troubleshooting: ~countdown leaping by large steps every SSE frame.
// Review if: engine emits a first-class remaining-time field.

/** Balanced: smoother than 0.3, still tracks real speed shifts within ~a few samples. */
export const EMA_ALPHA = 0.18;
export const MIN_POSITIVE_SAMPLES = 3;
export const ETA_FREEZE_MS = 30_000;
/** Ignore sub-frame byte deltas that would invent huge instantaneous rates. */
export const BYTE_DELTA_MIN_MS = 400;
/**
 * Max fractional jump of the ETA estimate per second of wall time when a new
 * sample arrives (display clamp). Natural countdown between samples is uncapped.
 */
export const DISPLAY_MAX_FRAC_PER_SEC = 0.2;

export const HARDWARE_REPAIR_BPS = 25 * 1024 * 1024;
export const HARDWARE_UNPACK_BPS = 40 * 1024 * 1024;
export const PRIOR_FLOOR_SEC = 5;
const PHASE_RATE_MIN_ELAPSED_SEC = 1;

export type EtaInput = {
  gid: string;
  status: string;
  protocol: string;
  phase?: string;
  totalLength: number;
  completedLength: number;
  downloadSpeed: number;
  phaseDone?: number;
  phaseTotal?: number;
  phaseStartedAt?: string;
  addedAt?: string;
};

export type EtaTracker = {
  lastGoodEtaSec: number | null;
  lastGoodAt: number;
  smoothedBps: number;
  samples: number;
  firstSeenAt: number;
  lastCompleted: number;
  lastCompletedAt: number;
};

export type EtaView =
  | { kind: "hidden" }
  | { kind: "calculating"; elapsedMs: number }
  | { kind: "dash"; elapsedMs: number }
  | { kind: "eta"; remainingSec: number; elapsedMs: number; frozen: boolean };

export function formatElapsed(ms: number): string {
  const totalSec = Math.max(0, Math.floor(ms / 1000));
  const h = Math.floor(totalSec / 3600);
  const m = Math.floor((totalSec % 3600) / 60);
  const s = totalSec % 60;
  const ss = String(s).padStart(2, "0");
  if (h > 0) return `${h}:${String(m).padStart(2, "0")}:${ss}`;
  return `${m}:${ss}`;
}

export function formatEtaCountdown(sec: number): string {
  return `~${formatElapsed(Math.max(0, sec) * 1000)}`;
}

export function priorRepairSec(totalLength: number): number {
  if (totalLength <= 0) return 0;
  return Math.max(PRIOR_FLOOR_SEC, totalLength / HARDWARE_REPAIR_BPS);
}

export function priorUnpackSec(totalLength: number): number {
  if (totalLength <= 0) return 0;
  return Math.max(PRIOR_FLOOR_SEC, totalLength / HARDWARE_UNPACK_BPS);
}

const TERMINAL_STATUSES = new Set([
  "paused",
  "waiting",
  "complete",
  "error",
  "removed",
]);

export function showsCountdown(d: EtaInput): boolean {
  if (TERMINAL_STATUSES.has(d.status)) return false;
  return (
    d.status === "active" ||
    d.phase === "downloading" ||
    d.phase === "repairing" ||
    d.phase === "unpacking"
  );
}

function isPostprocess(d: EtaInput): boolean {
  return d.phase === "repairing" || d.phase === "unpacking";
}

function remainingFromPhaseRate(
  phaseDone: number,
  phaseTotal: number,
  phaseStartedAt: string | undefined,
  now: number,
  priorSec: number,
): number {
  const fracRemain = phaseTotal > 0 ? Math.max(0, 1 - phaseDone / phaseTotal) : 1;
  const priorRemain = fracRemain * priorSec;
  if (!phaseStartedAt) return priorRemain;
  const started = Date.parse(phaseStartedAt);
  if (Number.isNaN(started)) return priorRemain;
  const elapsedSec = (now - started) / 1000;
  if (phaseDone > 0 && elapsedSec > PHASE_RATE_MIN_ELAPSED_SEC) {
    const rate = phaseDone / elapsedSec;
    if (rate > 0) return Math.max(0, phaseTotal - phaseDone) / rate;
  }
  return priorRemain;
}

export function hardwareEtaSec(d: EtaInput, now: number): number {
  if (d.protocol !== "usenet") return 0;
  const repairPrior = priorRepairSec(d.totalLength);
  const unpackPrior = priorUnpackSec(d.totalLength);
  if (d.phase === "repairing") {
    return (
      remainingFromPhaseRate(
        d.phaseDone ?? 0,
        d.phaseTotal ?? 0,
        d.phaseStartedAt,
        now,
        repairPrior,
      ) + unpackPrior
    );
  }
  if (d.phase === "unpacking") {
    return remainingFromPhaseRate(
      d.phaseDone ?? 0,
      d.phaseTotal ?? 0,
      d.phaseStartedAt,
      now,
      unpackPrior,
    );
  }
  return repairPrior + unpackPrior;
}

export function downloadEtaSec(d: EtaInput, tracker: EtaTracker): number | null {
  if (isPostprocess(d)) return 0;
  if (d.totalLength <= 0) return null;
  const remaining = d.totalLength - d.completedLength;
  if (remaining <= 0) return 0;
  if (tracker.samples < MIN_POSITIVE_SAMPLES || tracker.smoothedBps <= 0) {
    return null;
  }
  return remaining / tracker.smoothedBps;
}

export function computeEtaSec(
  d: EtaInput,
  tracker: EtaTracker,
  now: number,
): number | null {
  if (!showsCountdown(d)) return null;
  const hw = hardwareEtaSec(d, now);
  if (isPostprocess(d)) return hw;
  const dlEta = downloadEtaSec(d, tracker);
  if (dlEta == null) return null;
  return dlEta + hw;
}

/** Clamp a newly computed ETA toward the projected display value. */
export function clampEtaJump(
  rawSec: number,
  projectedSec: number,
  dtSec: number,
): number {
  if (!(dtSec > 0) || !Number.isFinite(projectedSec)) return rawSec;
  const maxJump = Math.max(1, Math.abs(projectedSec) * DISPLAY_MAX_FRAC_PER_SEC * dtSec);
  if (rawSec > projectedSec + maxJump) return projectedSec + maxJump;
  if (rawSec < projectedSec - maxJump) return projectedSec - maxJump;
  return rawSec;
}

function isEtaLive(
  d: EtaInput,
  tracker: EtaTracker,
  stalled: boolean,
): boolean {
  // Claude 2026-09-21: live countdown does not require the current frame's
  // downloadSpeed > 0. Reason: SSE frames can briefly report 0 between EMA
  // samples; requiring speed forced calculating…/freeze flicker. Stalled still
  // freezes then dashes via resolveEtaView.
  // Troubleshooting: calculating… while ↓ MB/s was intermittently zero.
  // Review if: engine emits a stable smoothed speed on the wire.
  if (!showsCountdown(d)) return false;
  if (isPostprocess(d)) return tracker.lastGoodEtaSec != null;
  if (stalled) return false;
  return (
    tracker.samples >= MIN_POSITIVE_SAMPLES &&
    tracker.smoothedBps > 0 &&
    tracker.lastGoodEtaSec != null
  );
}

export function elapsedMs(
  d: EtaInput,
  tracker: EtaTracker | undefined,
  now: number,
): number {
  if (d.addedAt) {
    const t = Date.parse(d.addedAt);
    if (!Number.isNaN(t)) return Math.max(0, now - t);
  }
  if (tracker) return Math.max(0, now - tracker.firstSeenAt);
  return 0;
}

function feedBps(t: EtaTracker, bps: number): void {
  if (!(bps > 0)) return;
  if (t.samples === 0) t.smoothedBps = bps;
  else t.smoothedBps = EMA_ALPHA * bps + (1 - EMA_ALPHA) * t.smoothedBps;
  t.samples += 1;
}

export function updateEtaTrackers(
  trackers: Map<string, EtaTracker>,
  list: readonly EtaInput[],
  now: number,
): void {
  const live = new Set(list.map((d) => d.gid));
  for (const gid of [...trackers.keys()]) {
    if (!live.has(gid)) trackers.delete(gid);
  }
  for (const d of list) {
    let t = trackers.get(d.gid);
    if (!t) {
      t = {
        lastGoodEtaSec: null,
        lastGoodAt: 0,
        smoothedBps: 0,
        samples: 0,
        firstSeenAt: now,
        lastCompleted: d.completedLength,
        lastCompletedAt: now,
      };
      trackers.set(d.gid, t);
    }
    if (!showsCountdown(d)) continue;

    let sampled = false;
    if (!isPostprocess(d)) {
      const dtMs = now - t.lastCompletedAt;
      const dBytes = d.completedLength - t.lastCompleted;
      if (dtMs >= BYTE_DELTA_MIN_MS && dBytes > 0) {
        feedBps(t, dBytes / (dtMs / 1000));
        sampled = true;
      } else if (d.downloadSpeed > 0 && t.samples < MIN_POSITIVE_SAMPLES) {
        // Bootstrap from the engine's rolling-window speed until byte deltas exist.
        feedBps(t, d.downloadSpeed);
        sampled = true;
      }
      t.lastCompleted = d.completedLength;
      t.lastCompletedAt = now;
    }

    const eta = computeEtaSec(d, t, now);
    if (eta == null) continue;
    // Only refresh lastGood on postprocess ticks or new rate samples — otherwise
    // every SSE frame would reset the freeze clock and stall the natural countdown.
    if (!isPostprocess(d) && !sampled && t.lastGoodEtaSec != null) continue;
    if (t.lastGoodEtaSec != null && t.lastGoodAt > 0) {
      const dtSec = (now - t.lastGoodAt) / 1000;
      const projected = Math.max(0, t.lastGoodEtaSec - dtSec);
      t.lastGoodEtaSec = clampEtaJump(eta, projected, dtSec);
    } else {
      t.lastGoodEtaSec = eta;
    }
    t.lastGoodAt = now;
  }
}

export function resolveEtaView(
  d: EtaInput,
  tracker: EtaTracker | undefined,
  now: number,
  stalled: boolean,
): EtaView {
  if (!showsCountdown(d)) return { kind: "hidden" };
  const elapsed = elapsedMs(d, tracker, now);
  if (!tracker) return { kind: "calculating", elapsedMs: elapsed };
  if (isEtaLive(d, tracker, stalled)) {
    const remainingSec = Math.max(
      0,
      tracker.lastGoodEtaSec! - (now - tracker.lastGoodAt) / 1000,
    );
    return { kind: "eta", remainingSec, elapsedMs: elapsed, frozen: false };
  }
  if (
    tracker.lastGoodEtaSec != null &&
    now - tracker.lastGoodAt < ETA_FREEZE_MS
  ) {
    return {
      kind: "eta",
      remainingSec: tracker.lastGoodEtaSec,
      elapsedMs: elapsed,
      frozen: true,
    };
  }
  if (tracker.lastGoodEtaSec != null) {
    return { kind: "dash", elapsedMs: elapsed };
  }
  return { kind: "calculating", elapsedMs: elapsed };
}
