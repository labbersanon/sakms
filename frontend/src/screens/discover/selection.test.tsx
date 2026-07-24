// selection store tests — the pure reducer behind Discover select-mode's
// bulk-grab (F3). These are router-free and component-free: they exercise
// createSelection directly, which is exactly why the store uses only
// createSignal (no reactive-root requirement).
//
// The headline test is the pre-mortem #5 orphan-drop proof — see
// "buildBatch drops selected keys whose card is no longer registered" below.

import { describe, expect, it } from "vitest";
import { createSelection } from "./selection";
import { type GrabTarget } from "./shared";

const movieTarget = (tmdbId: number, title = `Movie ${tmdbId}`): GrabTarget => ({
  mode: "movies",
  label: title,
  request: { title, tmdbId },
});

const seasonTarget = (tmdbId: number, season: number): GrabTarget => ({
  mode: "series",
  label: `Show ${tmdbId} S${season}`,
  request: {
    title: `Show ${tmdbId}`,
    tmdbId,
    seasonNumber: season,
    seasonSpecified: true,
  },
});

const adultTarget = (id: string): GrabTarget => ({
  mode: "adult",
  label: `Scene ${id}`,
  request: { title: `Scene ${id}` },
});

describe("createSelection — toggle / clear / count", () => {
  it("toggles a key on and off and tracks count", () => {
    const s = createSelection();
    expect(s.count()).toBe(0);
    expect(s.has("movies:1")).toBe(false);

    s.toggle("movies:1");
    expect(s.has("movies:1")).toBe(true);
    expect(s.count()).toBe(1);

    s.toggle("movies:1");
    expect(s.has("movies:1")).toBe(false);
    expect(s.count()).toBe(0);
  });

  it("clear() removes every selection", () => {
    const s = createSelection();
    s.toggle("movies:1");
    s.toggle("movies:2");
    s.toggle("adult:s1");
    expect(s.count()).toBe(3);

    s.clear();
    expect(s.count()).toBe(0);
    expect(s.has("movies:1")).toBe(false);
  });

  it("keys are unique across mode / season variants (no collisions)", () => {
    const s = createSelection();
    // Same tmdbId, three genuinely different selectable things.
    s.toggle("movies:42");
    s.toggle("series:42:S1");
    s.toggle("series:42:S2");
    expect(s.count()).toBe(3);
    // Re-toggling one season does not touch the others.
    s.toggle("series:42:S1");
    expect(s.count()).toBe(2);
    expect(s.has("series:42:S2")).toBe(true);
    expect(s.has("movies:42")).toBe(true);
  });
});

describe("createSelection — selectMode", () => {
  it("defaults off and toggles", () => {
    const s = createSelection();
    expect(s.selectMode()).toBe(false);
    s.setSelectMode(true);
    expect(s.selectMode()).toBe(true);
    s.setSelectMode(false);
    expect(s.selectMode()).toBe(false);
  });
});

describe("createSelection — buildBatch (pre-mortem #5 orphan-drop safety)", () => {
  it("builds a batch item per selected + registered key, payload from the live registry", () => {
    const s = createSelection();
    s.register("movies:1", movieTarget(1));
    s.register("movies:2", movieTarget(2));
    s.toggle("movies:1");
    s.toggle("movies:2");

    const batch = s.buildBatch();
    expect(batch.selectedCount).toBe(2);
    expect(batch.submittedCount).toBe(2);
    expect(batch.items.map((i) => i.request.tmdbId)).toEqual([1, 2]);
    expect(batch.items.every((i) => i.mode === "movies")).toBe(true);
  });

  // THE pre-mortem #5 proof: a selected key whose card is no longer registered
  // (an orphan) is NEVER included in the built AutoGrabBatchRequest — so no
  // stale/wrong tmdbId can reach the endpoint — and the drop is visible via
  // selectedCount > submittedCount.
  it("drops selected keys whose card is no longer registered, and reports the drop", () => {
    const s = createSelection();
    s.register("movies:1", movieTarget(1));
    s.register("movies:2", movieTarget(2));
    // movies:3 is selected but was NEVER registered (its card is not on screen).
    s.toggle("movies:1");
    s.toggle("movies:2");
    s.toggle("movies:3");

    const batch = s.buildBatch();
    expect(batch.selectedCount).toBe(3);
    expect(batch.submittedCount).toBe(2);
    expect(batch.items.map((i) => i.request.tmdbId)).toEqual([1, 2]);
    // The orphan's id never appears in the built request.
    expect(batch.items.some((i) => i.request.tmdbId === 3)).toBe(false);
  });

  it("treats a key whose card unmounted (registration cleaned up) as an orphan", () => {
    const s = createSelection();
    const cleanup1 = s.register("movies:1", movieTarget(1));
    s.register("movies:2", movieTarget(2));
    s.toggle("movies:1");
    s.toggle("movies:2");

    // The card for movies:1 leaves the screen (filter change, drill-away, etc.).
    cleanup1();

    const batch = s.buildBatch();
    expect(batch.selectedCount).toBe(2);
    expect(batch.submittedCount).toBe(1);
    expect(batch.items.map((i) => i.request.tmdbId)).toEqual([2]);
  });

  it("ref-counts a key rendered by two rows so one unmount doesn't orphan it", () => {
    const s = createSelection();
    const cleanupA = s.register("movies:1", movieTarget(1));
    const cleanupB = s.register("movies:1", movieTarget(1)); // same title, second row
    s.toggle("movies:1");

    // One row unmounts; the other is still on screen.
    cleanupA();
    let batch = s.buildBatch();
    expect(batch.submittedCount).toBe(1);
    expect(batch.items).toHaveLength(1);

    // The last row unmounts too — now it is a genuine orphan.
    cleanupB();
    batch = s.buildBatch();
    expect(batch.selectedCount).toBe(1);
    expect(batch.submittedCount).toBe(0);
  });

  it("flattens multi-season selections of one series into one item per season", () => {
    const s = createSelection();
    s.register("series:7:S1", seasonTarget(7, 1));
    s.register("series:7:S2", seasonTarget(7, 2));
    s.toggle("series:7:S1");
    s.toggle("series:7:S2");

    const batch = s.buildBatch();
    expect(batch.submittedCount).toBe(2);
    expect(batch.items.map((i) => i.request.seasonNumber)).toEqual([1, 2]);
    expect(batch.items.every((i) => i.request.seasonSpecified === true)).toBe(
      true,
    );
  });

  it("mixes modes in one batch, each item carrying its own mode", () => {
    const s = createSelection();
    s.register("movies:1", movieTarget(1));
    s.register("adult:s1", adultTarget("s1"));
    s.toggle("movies:1");
    s.toggle("adult:s1");

    const batch = s.buildBatch();
    expect(batch.items.map((i) => i.mode).sort()).toEqual(["adult", "movies"]);
  });
});
