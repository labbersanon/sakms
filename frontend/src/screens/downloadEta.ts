// Claude 2026-09-21: client-side lifecycle ETA for the Downloads queue.
// Reason: operators wanted remaining time (download + Usenet repair/unpack), not
//   phase-elapsed only; the engine does not emit an ETA field.
// Troubleshooting: Downloads showed NN% + elapsed during PAR2/unrar, or ↓ speed
//   while downloading, with no sense of time left.
// Review if: the engine starts emitting remaining-time estimates of its own.

/** Ignore sub-frame byte deltas that would invent huge instantaneous rates. */
export const BYTE_DELTA_MIN_MS = 400;
/** One solid rate sample is enough to leave calculating… (Usenet is bursty). */
export const MIN_POSITIVE_SAMPLES = 1;
export const ETA_FREEZE_MS = 30_000;
/**
 * Rolling span for the client byte-rate window. Matches engine
 * ratesmooth.DefaultWindow so remaining time is leftover / ~60s average.
 */
export const RATE_WINDOW_MS = 60_000;
/** Session-average fallback when wire speed and the 60s window are both quiet. */
export const OVERALL_AVG_MIN_MS = 2_000;

export const HARDWARE_REPAIR_BPS = 25 * 1024 * 1024;
export const HARDWARE_UNPACK_BPS = 40 * 1024 * 1024;
export const PRIOR_FLOOR_SEC = 5;
const PHASE_RATE_MIN_ELAPSED_SEC = 1;

// Claude 2026-09-21: live repair/unpack BPS, seeded at HARDWARE_* constants.
// Reason: GET /api/downloads/eta-priors may raise these after observed phases;
//   tests keep the exported constants as the default seed.
// Troubleshooting: Downloads countdown still using 25/40 after samples exist.
// Review if: hardwareEtaSec takes an explicit Priors object instead of setters.
let liveRepairBps = HARDWARE_REPAIR_BPS;
let liveUnpackBps = HARDWARE_UNPACK_BPS;

export function setHardwarePriors(repairBps: number, unpackBps: number): void {
  if (repairBps > 0) liveRepairBps = repairBps;
  if (unpackBps > 0) liveUnpackBps = unpackBps;
}

export function resetHardwarePriors(): void {
  liveRepairBps = HARDWARE_REPAIR_BPS;
  liveUnpackBps = HARDWARE_UNPACK_BPS;
}

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

type RateSample = { at: number; n: number };

export type EtaTracker = {
  lastGoodEtaSec: number | null;
  lastGoodAt: number;
  smoothedBps: number;
  samples: number;
  firstSeenAt: number;
  /** completedLength when the tracker was created (session-average baseline). */
  originCompleted: number;
  lastCompletedAt: number;
  rateSamples: RateSample[];
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
  return Math.max(PRIOR_FLOOR_SEC, totalLength / liveRepairBps);
}

export function priorUnpackSec(totalLength: number): number {
  if (totalLength <= 0) return 0;
  return Math.max(PRIOR_FLOOR_SEC, totalLength / liveUnpackBps);
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
  if (d.phase === "precheck" || d.phase === "waiting") return false;
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

function isEtaLive(
  d: EtaInput,
  tracker: EtaTracker,
  stalled: boolean,
): boolean {
  // Claude 2026-10-21: live remaining does not require the current frame's
  //   downloadSpeed > 0. Reason: engine ratesmooth can briefly report 0
  //   between article flushes; requiring speed forced calculating… flicker.
  //   Display holds lastGoodEtaSec until the next 60s-average calc.
  // Troubleshooting: calculating… while ↓ MB/s was intermittently zero.
  // Review if: engine emits a stable smoothed speed on every frame.
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

// Claude 2026-10-21: 60s rolling byte window, same shape as ratesmooth.Window.
// Reason: remaining must be leftover / minute-scale average, not an EMA of
//   400ms deltas; display updates only when this window (or engine 60s speed)
//   produces a new rate.
// Troubleshooting: ~countdown ticked every 1s then jumped on the next SSE.
// Review if: the engine emits remaining-time estimates of its own.
function addRateSample(t: EtaTracker, at: number, n: number): number {
  t.rateSamples.push({ at, n });
  const cutoff = at - RATE_WINDOW_MS;
  let i = 0;
  while (i < t.rateSamples.length && t.rateSamples[i]!.at < cutoff) {
    i++;
  }
  if (i > 0 && t.rateSamples.length - i < 2 && t.rateSamples.length >= 2) {
    i = t.rateSamples.length - 2;
  }
  if (i > 0) t.rateSamples = t.rateSamples.slice(i);
  t.samples = t.rateSamples.length;
  if (t.rateSamples.length < 2) return 0;
  const first = t.rateSamples[0]!;
  const last = t.rateSamples[t.rateSamples.length - 1]!;
  const dtSec = (last.at - first.at) / 1000;
  if (!(dtSec > 0)) return 0;
  const delta = last.n - first.n;
  if (!(delta > 0)) return 0;
  return delta / dtSec;
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
        samples: 1,
        firstSeenAt: now,
        originCompleted: d.completedLength,
        lastCompletedAt: now,
        rateSamples: [{ at: now, n: d.completedLength }],
      };
      trackers.set(d.gid, t);
    }
    if (!showsCountdown(d)) continue;

    let sampled = false;
    if (!isPostprocess(d)) {
      // Claude 2026-10-21: rate is engine DownloadSpeed (already ~60s
      //   ratesmooth) or the client 60s completedLength window when wire
      //   speed is 0. Do not EMA short deltas.
      // Reason: dual-engine ~250ms SSE + sparse completedLength left ETA on
      //   calculating…; EMA then fought the engine's minute window.
      // Troubleshooting: progress and/or ↓ rate visible, ETA stuck calculating….
      // Review if: engine emits a stable smoothed DownloadSpeed on every frame.
      const dtMs = now - t.lastCompletedAt;
      if (dtMs >= BYTE_DELTA_MIN_MS) {
        const windowBps = addRateSample(t, now, d.completedLength);
        t.lastCompletedAt = now;
        if (d.downloadSpeed > 0) {
          t.smoothedBps = d.downloadSpeed;
          sampled = true;
        } else if (windowBps > 0) {
          t.smoothedBps = windowBps;
          sampled = true;
        }
      } else if (d.downloadSpeed > 0 && t.smoothedBps <= 0) {
        t.smoothedBps = d.downloadSpeed;
        sampled = true;
        t.lastCompletedAt = now;
      } else if (
        t.smoothedBps <= 0 &&
        now - t.firstSeenAt >= OVERALL_AVG_MIN_MS
      ) {
        const sessionBytes = d.completedLength - t.originCompleted;
        const elapsedSec = (now - t.firstSeenAt) / 1000;
        if (sessionBytes > 0 && elapsedSec > 0) {
          t.smoothedBps = sessionBytes / elapsedSec;
          sampled = true;
        }
      }
    }

    const eta = computeEtaSec(d, t, now);
    if (eta == null) continue;
    // Only refresh lastGood on postprocess ticks or new rate samples — otherwise
    // the 1s elapsed tick would look like a new remaining calculation.
    if (!isPostprocess(d) && !sampled && t.lastGoodEtaSec != null) continue;
    t.lastGoodEtaSec = eta;
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
    return {
      kind: "eta",
      remainingSec: tracker.lastGoodEtaSec!,
      elapsedMs: elapsed,
      frozen: false,
    };
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
