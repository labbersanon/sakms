import { describe, expect, it } from "vitest";
import {
  EMA_ALPHA,
  ETA_FREEZE_MS,
  HARDWARE_REPAIR_BPS,
  HARDWARE_UNPACK_BPS,
  MIN_POSITIVE_SAMPLES,
  type EtaInput,
  type EtaTracker,
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

const sampleTwice = (
  d: EtaInput,
  now = 1_000,
): Map<string, EtaTracker> => {
  const trackers = new Map<string, EtaTracker>();
  updateEtaTrackers(trackers, [d], now - 1_000);
  updateEtaTrackers(trackers, [d], now);
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
  it("seeds on the first positive sample and mixes with α on the next", () => {
    const trackers = new Map<string, EtaTracker>();
    updateEtaTrackers(trackers, [item({ downloadSpeed: 100 })], 0);
    expect(trackers.get("g1")?.samples).toBe(1);
    expect(trackers.get("g1")?.smoothedBps).toBe(100);
    expect(trackers.get("g1")?.lastGoodEtaSec).toBeNull();

    updateEtaTrackers(trackers, [item({ downloadSpeed: 200 })], 1000);
    expect(trackers.get("g1")?.samples).toBe(MIN_POSITIVE_SAMPLES);
    expect(trackers.get("g1")?.smoothedBps).toBe(
      EMA_ALPHA * 200 + (1 - EMA_ALPHA) * 100,
    );
    expect(trackers.get("g1")?.lastGoodEtaSec).toBeCloseTo(600 / 130);
  });

  it("ignores zero-speed frames so EMA does not collapse toward 0", () => {
    const trackers = sampleTwice(item({ downloadSpeed: 100 }), 1000);
    const smoothed = trackers.get("g1")!.smoothedBps;
    updateEtaTrackers(
      trackers,
      [item({ downloadSpeed: 0 })],
      2000,
    );
    expect(trackers.get("g1")?.samples).toBe(2);
    expect(trackers.get("g1")?.smoothedBps).toBe(smoothed);
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
    const trackers = sampleTwice(d, 1000);
    const eta = computeEtaSec(d, trackers.get("g1")!, 1000);
    expect(eta).toBeCloseTo(600 / 50 + 5 + 5);
  });

  it("does not add hardware priors for torrents", () => {
    const d = item({ protocol: "torrent", downloadSpeed: 100 });
    const trackers = sampleTwice(d, 1000);
    expect(hardwareEtaSec(d, 1000)).toBe(0);
    expect(computeEtaSec(d, trackers.get("g1")!, 1000)).toBeCloseTo(6);
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
    // rate = 2/10 units/s, remain units = 2 → 10s repair + 5s unpack prior
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
  it("shows calculating until two positive speed samples", () => {
    const trackers = new Map<string, EtaTracker>();
    const d = item({ downloadSpeed: 100 });
    updateEtaTrackers(trackers, [d], 0);
    expect(resolveEtaView(d, trackers.get("g1"), 0, false).kind).toBe(
      "calculating",
    );
  });

  it("freezes last good ETA for 30s of zero speed, then dashes", () => {
    const d = item({ downloadSpeed: 100 });
    const trackers = sampleTwice(d, 1000);
    const good = trackers.get("g1")!.lastGoodEtaSec;
    expect(good).toBeCloseTo(6);

    const stalled = item({ downloadSpeed: 0 });
    updateEtaTrackers(trackers, [stalled], 2000);
    const frozen = resolveEtaView(stalled, trackers.get("g1"), 2000, true);
    expect(frozen).toMatchObject({
      kind: "eta",
      remainingSec: good,
      frozen: true,
    });

    const stillFrozen = resolveEtaView(
      stalled,
      trackers.get("g1"),
      1000 + ETA_FREEZE_MS - 1,
      true,
    );
    expect(stillFrozen.kind).toBe("eta");
    if (stillFrozen.kind === "eta") expect(stillFrozen.frozen).toBe(true);

    const dashed = resolveEtaView(
      stalled,
      trackers.get("g1"),
      1000 + ETA_FREEZE_MS,
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
    const trackers = sampleTwice(d, 1000);
    const t = trackers.get("g1");
    expect(resolveEtaView({ ...d, status: "paused" }, t, 1000, false).kind).toBe(
      "hidden",
    );
    expect(resolveEtaView({ ...d, status: "waiting" }, t, 1000, false).kind).toBe(
      "hidden",
    );
    expect(
      resolveEtaView({ ...d, status: "complete" }, t, 1000, false).kind,
    ).toBe("hidden");
    expect(resolveEtaView({ ...d, status: "error" }, t, 1000, false).kind).toBe(
      "hidden",
    );
  });

  it("ticks the live countdown down between samples", () => {
    const d = item({ downloadSpeed: 100 });
    const trackers = sampleTwice(d, 1000);
    const live = resolveEtaView(d, trackers.get("g1"), 3000, false);
    expect(live.kind).toBe("eta");
    if (live.kind === "eta") {
      expect(live.frozen).toBe(false);
      expect(live.remainingSec).toBeCloseTo(4);
    }
  });

  it("uses addedAt for elapsed when present, otherwise first-seen", () => {
    const added = new Date(0).toISOString();
    const d = item({ addedAt: added, downloadSpeed: 100 });
    const trackers = sampleTwice(d, 1000);
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
    const trackers = sampleTwice(item(), 1000);
    updateEtaTrackers(trackers, [], 2000);
    expect(trackers.size).toBe(0);
  });
});
