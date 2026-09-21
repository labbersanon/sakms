import { describe, expect, it } from "vitest";
import {
  DISPLAY_MAX_FRAC_PER_SEC,
  EMA_ALPHA,
  ETA_FREEZE_MS,
  HARDWARE_REPAIR_BPS,
  HARDWARE_UNPACK_BPS,
  MIN_POSITIVE_SAMPLES,
  OVERALL_AVG_MIN_MS,
  type EtaInput,
  type EtaTracker,
  clampEtaJump,
  computeEtaSec,
  formatElapsed,
  formatEtaCountdown,
  hardwareEtaSec,
  priorRepairSec,
  priorUnpackSec,
  resolveEtaView,
  updateEtaTrackers,
} from "./downloadEta";

const item = (over: Partial<EtaInput> = {}): EtaInput => ({
  gid: "g1",
  status: "active",
  protocol: "torrent",
  totalLength: 1000,
  completedLength: 400,
  downloadSpeed: 100,
  ...over,
});

/** Feed MIN_POSITIVE_SAMPLES bootstrap speed frames spaced 1s apart. */
const sampleEnough = (
  d: EtaInput,
  now = 1_000,
): Map<string, EtaTracker> => {
  const trackers = new Map<string, EtaTracker>();
  for (let i = 0; i < MIN_POSITIVE_SAMPLES; i++) {
    updateEtaTrackers(
      trackers,
      [d],
      now - (MIN_POSITIVE_SAMPLES - 1 - i) * 1_000,
    );
  }
  return trackers;
};

describe("formatElapsed / formatEtaCountdown", () => {
  it("formats M:SS and H:MM:SS, with a ~ prefix on the countdown", () => {
    expect(formatElapsed(83_000)).toBe("1:23");
    expect(formatElapsed(3_661_000)).toBe("1:01:01");
    expect(formatEtaCountdown(83)).toBe("~1:23");
    expect(formatEtaCountdown(3661)).toBe("~1:01:01");
    expect(formatEtaCountdown(0)).toBe("~0:00");
  });
});

describe("EMA download speed", () => {
  it("seeds on the first positive sample and mixes with α on later samples", () => {
    const trackers = new Map<string, EtaTracker>();
    updateEtaTrackers(trackers, [item({ downloadSpeed: 100 })], 0);
    expect(trackers.get("g1")?.samples).toBe(1);
    expect(trackers.get("g1")?.smoothedBps).toBe(100);
    expect(trackers.get("g1")?.lastGoodEtaSec).not.toBeNull();

    updateEtaTrackers(trackers, [item({ downloadSpeed: 200 })], 1000);
    expect(trackers.get("g1")?.samples).toBe(2);
    expect(trackers.get("g1")?.smoothedBps).toBe(
      EMA_ALPHA * 200 + (1 - EMA_ALPHA) * 100,
    );
  });

  it("prefers completedLength byte-deltas once past bootstrap", () => {
    const trackers = sampleEnough(item({ downloadSpeed: 100, completedLength: 400 }), 2000);
    const before = trackers.get("g1")!.smoothedBps;
    // 400 bytes in 1s → 400 B/s instant; EMA pulls smoothed toward it.
    updateEtaTrackers(
      trackers,
      [item({ downloadSpeed: 0, completedLength: 800 })],
      3000,
    );
    expect(trackers.get("g1")!.smoothedBps).not.toBe(before);
    expect(trackers.get("g1")!.smoothedBps).toBeCloseTo(
      EMA_ALPHA * 400 + (1 - EMA_ALPHA) * before,
    );
  });

  it("ignores zero-speed frames so EMA does not collapse toward 0", () => {
    const trackers = sampleEnough(item({ downloadSpeed: 100 }), 2000);
    const smoothed = trackers.get("g1")!.smoothedBps;
    const samples = trackers.get("g1")!.samples;
    updateEtaTrackers(
      trackers,
      [item({ downloadSpeed: 0 })],
      3000,
    );
    expect(trackers.get("g1")?.samples).toBe(samples);
    expect(trackers.get("g1")?.smoothedBps).toBe(smoothed);
  });

  it("arms ETA from session-average when wire speed stays 0 but bytes advanced", () => {
    const trackers = new Map<string, EtaTracker>();
    updateEtaTrackers(
      trackers,
      [item({ downloadSpeed: 0, completedLength: 0, totalLength: 1_000_000 })],
      0,
    );
    expect(resolveEtaView(
      item({ downloadSpeed: 0, completedLength: 0, totalLength: 1_000_000 }),
      trackers.get("g1"),
      0,
      false,
    ).kind).toBe("calculating");

    updateEtaTrackers(
      trackers,
      [item({ downloadSpeed: 0, completedLength: 200_000, totalLength: 1_000_000 })],
      OVERALL_AVG_MIN_MS,
    );
    const t = trackers.get("g1")!;
    expect(t.samples).toBeGreaterThanOrEqual(MIN_POSITIVE_SAMPLES);
    expect(t.lastGoodEtaSec).not.toBeNull();
    expect(
      resolveEtaView(
        item({ downloadSpeed: 0, completedLength: 200_000, totalLength: 1_000_000 }),
        t,
        OVERALL_AVG_MIN_MS,
        false,
      ).kind,
    ).toBe("eta");
  });
});

describe("clampEtaJump", () => {
  it("limits estimate jumps to DISPLAY_MAX_FRAC_PER_SEC of the projected value", () => {
    const projected = 100;
    const dt = 1;
    const maxJump = projected * DISPLAY_MAX_FRAC_PER_SEC * dt;
    expect(clampEtaJump(200, projected, dt)).toBeCloseTo(projected + maxJump);
    expect(clampEtaJump(10, projected, dt)).toBeCloseTo(projected - maxJump);
    expect(clampEtaJump(105, projected, dt)).toBe(105);
  });
});

describe("hardware priors", () => {
  it("floors tiny payloads at 5s and uses totalLength / BPS otherwise", () => {
    expect(priorRepairSec(0)).toBe(0);
    expect(priorUnpackSec(0)).toBe(0);
    expect(priorRepairSec(100)).toBe(5);
    expect(priorUnpackSec(100)).toBe(5);
    expect(priorRepairSec(HARDWARE_REPAIR_BPS * 10)).toBe(10);
    expect(priorUnpackSec(HARDWARE_UNPACK_BPS * 8)).toBe(8);
  });

  it("adds full repair+unpack priors while a usenet job is still downloading", () => {
    const d = item({
      protocol: "usenet",
      phase: "downloading",
      totalLength: 1000,
      completedLength: 400,
      downloadSpeed: 50,
    });
    const trackers = sampleEnough(d, 2000);
    const eta = computeEtaSec(d, trackers.get("g1")!, 2000);
    expect(eta).toBeCloseTo(600 / trackers.get("g1")!.smoothedBps + 5 + 5);
  });

  it("does not add hardware priors for torrents", () => {
    const d = item({ protocol: "torrent", downloadSpeed: 100 });
    const trackers = sampleEnough(d, 2000);
    expect(hardwareEtaSec(d, 2000)).toBe(0);
    expect(computeEtaSec(d, trackers.get("g1")!, 2000)).toBeCloseTo(
      600 / trackers.get("g1")!.smoothedBps,
    );
  });
});

describe("postprocess remaining", () => {
  it("uses live phase rate when elapsed > 1s and phaseDone > 0", () => {
    const started = 0;
    const now = 10_000;
    const d = item({
      protocol: "usenet",
      phase: "repairing",
      downloadSpeed: 0,
      totalLength: 100,
      phaseDone: 2,
      phaseTotal: 4,
      phaseStartedAt: new Date(started).toISOString(),
    });
    const trackers = new Map<string, EtaTracker>();
    updateEtaTrackers(trackers, [d], now);
    expect(trackers.get("g1")?.lastGoodEtaSec).toBeCloseTo(15);
  });

  it("falls back to the prior fraction when elapsed ≤ 1s", () => {
    const now = 10_000;
    const d = item({
      protocol: "usenet",
      phase: "repairing",
      downloadSpeed: 0,
      totalLength: 100,
      phaseDone: 2,
      phaseTotal: 4,
      phaseStartedAt: new Date(now - 500).toISOString(),
    });
    expect(hardwareEtaSec(d, now)).toBeCloseTo(0.5 * 5 + 5);
  });

  it("falls back to the prior fraction when phaseDone is 0", () => {
    const now = 10_000;
    const d = item({
      protocol: "usenet",
      phase: "unpacking",
      downloadSpeed: 0,
      totalLength: HARDWARE_UNPACK_BPS * 10,
      phaseDone: 0,
      phaseTotal: 4,
      phaseStartedAt: new Date(now - 8_000).toISOString(),
    });
    expect(hardwareEtaSec(d, now)).toBeCloseTo(10);
  });

  it("drops repair and scales unpack remaining while unpacking", () => {
    const now = 10_000;
    const d = item({
      protocol: "usenet",
      phase: "unpacking",
      downloadSpeed: 0,
      totalLength: 100,
      phaseDone: 1,
      phaseTotal: 4,
      phaseStartedAt: new Date(now - 500).toISOString(),
    });
    expect(hardwareEtaSec(d, now)).toBeCloseTo(0.75 * 5);
  });
});

describe("freeze / calculating / hidden", () => {
  it("shows calculating until enough positive speed samples", () => {
    const trackers = new Map<string, EtaTracker>();
    const d = item({ downloadSpeed: 0 });
    updateEtaTrackers(trackers, [d], 0);
    expect(resolveEtaView(d, trackers.get("g1"), 0, false).kind).toBe(
      "calculating",
    );
  });

  it("arms ETA from byte deltas when SSE is faster than BYTE_DELTA_MIN_MS and wire speed is 0", () => {
    // Dual-engine fanout often delivers ~250ms frames; wire downloadSpeed can
    // stay 0 while the 60s ratesmooth window fills. Progress must still arm ETA.
    const trackers = new Map<string, EtaTracker>();
    let completed = 0;
    const start = 1_000;
    for (let i = 0; i < 12; i++) {
      completed += 25_000;
      updateEtaTrackers(
        trackers,
        [
          item({
            downloadSpeed: 0,
            completedLength: completed,
            totalLength: 1_000_000,
          }),
        ],
        start + i * 250,
      );
    }
    const t = trackers.get("g1")!;
    expect(t.samples).toBeGreaterThanOrEqual(MIN_POSITIVE_SAMPLES);
    expect(t.lastGoodEtaSec).not.toBeNull();
    expect(
      resolveEtaView(
        item({
          downloadSpeed: 0,
          completedLength: completed,
          totalLength: 1_000_000,
        }),
        t,
        start + 11 * 250,
        false,
      ).kind,
    ).toBe("eta");
  });

  it("freezes last good ETA for 30s of zero speed, then dashes", () => {
    const d = item({ downloadSpeed: 100 });
    const trackers = sampleEnough(d, 2000);
    const good = trackers.get("g1")!.lastGoodEtaSec;
    expect(good).toBeGreaterThan(0);

    const stalled = item({ downloadSpeed: 0 });
    updateEtaTrackers(trackers, [stalled], 3000);
    const frozen = resolveEtaView(stalled, trackers.get("g1"), 3000, true);
    expect(frozen).toMatchObject({
      kind: "eta",
      remainingSec: good,
      frozen: true,
    });

    const stillFrozen = resolveEtaView(
      stalled,
      trackers.get("g1"),
      2000 + ETA_FREEZE_MS - 1,
      true,
    );
    expect(stillFrozen.kind).toBe("eta");
    if (stillFrozen.kind === "eta") expect(stillFrozen.frozen).toBe(true);

    const dashed = resolveEtaView(
      stalled,
      trackers.get("g1"),
      2000 + ETA_FREEZE_MS,
      true,
    );
    expect(dashed.kind).toBe("dash");
  });

  it("keeps calculating when there was never a good ETA", () => {
    const trackers = new Map<string, EtaTracker>();
    const d = item({ downloadSpeed: 0 });
    updateEtaTrackers(trackers, [d], 0);
    updateEtaTrackers(trackers, [d], ETA_FREEZE_MS + 5_000);
    expect(
      resolveEtaView(d, trackers.get("g1"), ETA_FREEZE_MS + 5_000, true).kind,
    ).toBe("calculating");
  });

  it("hides the countdown when paused, queued, complete, or error", () => {
    const d = item({ downloadSpeed: 100 });
    const trackers = sampleEnough(d, 2000);
    const t = trackers.get("g1");
    expect(resolveEtaView({ ...d, status: "paused" }, t, 2000, false).kind).toBe(
      "hidden",
    );
    expect(resolveEtaView({ ...d, status: "waiting" }, t, 2000, false).kind).toBe(
      "hidden",
    );
    expect(
      resolveEtaView({ ...d, status: "complete" }, t, 2000, false).kind,
    ).toBe("hidden");
    expect(resolveEtaView({ ...d, status: "error" }, t, 2000, false).kind).toBe(
      "hidden",
    );
  });

  it("ticks the live countdown down between samples", () => {
    const d = item({ downloadSpeed: 100 });
    const trackers = sampleEnough(d, 2000);
    const live = resolveEtaView(d, trackers.get("g1"), 4000, false);
    expect(live.kind).toBe("eta");
    if (live.kind === "eta") {
      expect(live.frozen).toBe(false);
      expect(live.remainingSec).toBeCloseTo(
        trackers.get("g1")!.lastGoodEtaSec! - 2,
      );
    }
  });

  it("keeps a live countdown across zero-speed frames when not stalled", () => {
    const d = item({ downloadSpeed: 100 });
    const trackers = sampleEnough(d, 2000);
    const gap = item({ downloadSpeed: 0 });
    updateEtaTrackers(trackers, [gap], 3000);
    const live = resolveEtaView(gap, trackers.get("g1"), 4000, false);
    expect(live.kind).toBe("eta");
    if (live.kind === "eta") {
      expect(live.frozen).toBe(false);
      expect(live.remainingSec).toBeCloseTo(
        trackers.get("g1")!.lastGoodEtaSec! - 2,
      );
    }
  });

  it("uses addedAt for elapsed when present, otherwise first-seen", () => {
    const added = new Date(0).toISOString();
    const d = item({ addedAt: added, downloadSpeed: 100 });
    const trackers = sampleEnough(d, 2000);
    const view = resolveEtaView(d, trackers.get("g1"), 83_000, false);
    expect(view.kind).not.toBe("hidden");
    if (view.kind !== "hidden") expect(view.elapsedMs).toBe(83_000);

    const noAdded = item({ downloadSpeed: 0 });
    const t2 = new Map<string, EtaTracker>();
    updateEtaTrackers(t2, [noAdded], 5_000);
    const elapsed = resolveEtaView(noAdded, t2.get("g1"), 8_000, false);
    expect(elapsed.kind).toBe("calculating");
    if (elapsed.kind === "calculating") expect(elapsed.elapsedMs).toBe(3_000);
  });

  it("drops trackers for gids that left the queue", () => {
    const trackers = sampleEnough(item(), 2000);
    updateEtaTrackers(trackers, [], 3000);
    expect(trackers.size).toBe(0);
  });
});
