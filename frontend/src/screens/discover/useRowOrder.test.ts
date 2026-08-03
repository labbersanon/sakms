// useRowOrder tests — the per-screen state machine's reorder/hidden logic,
// asserted directly (the drag gesture that drives persistOrder in the real UI
// isn't simulable in jsdom, so the resulting state-update logic is tested at the
// hook level instead). The api/rowOrder module is mocked so no real fetch runs;
// mergeRowOrder is kept REAL (via importOriginal) since orderedKeys depends on
// its reconciliation.

import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook } from "@solidjs/testing-library";
import * as rowOrderApi from "../../api/rowOrder";
import { useRowOrder } from "./useRowOrder";

vi.mock("../../api/rowOrder", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../api/rowOrder")>();
  return {
    ...actual, // keep the real mergeRowOrder + DiscoverScreen type
    fetchRowOrder: vi.fn(),
    fetchRowHidden: vi.fn(),
    saveRowOrder: vi.fn(),
    saveRowHidden: vi.fn(),
  };
});

const mocked = vi.mocked(rowOrderApi);
const flush = () => new Promise((r) => setTimeout(r, 0));

beforeEach(() => {
  vi.clearAllMocks();
  mocked.fetchRowOrder.mockResolvedValue([]);
  mocked.fetchRowHidden.mockResolvedValue([]);
  mocked.saveRowOrder.mockResolvedValue(undefined);
  mocked.saveRowHidden.mockResolvedValue(undefined);
});

describe("useRowOrder — order", () => {
  it("persistOrder saves the new order and reflects it in orderedKeys", async () => {
    const { result } = renderHook(() =>
      useRowOrder("mainstream", () => ["a", "b", "c"]),
    );
    await flush();
    // Empty stored order merges to knownKeys' own order.
    expect(result.orderedKeys()).toEqual(["a", "b", "c"]);

    result.persistOrder(["c", "b", "a"]);
    expect(mocked.saveRowOrder).toHaveBeenCalledWith("mainstream", [
      "c",
      "b",
      "a",
    ]);
    expect(result.orderedKeys()).toEqual(["c", "b", "a"]);
  });
});

// The hidden-block cases run against "mainstream", not "adult": Adult retired
// the per-screen KV order/hidden store entirely (its row order is adultnewest's
// sort_order column now, and it has no structural rows left to hide). The logic
// under test is unchanged and very much still live — Mainstream is the
// surviving consumer, which is exactly why useRowOrder itself was kept.
describe("useRowOrder — hidden", () => {
  it("isHidden defaults to false while the hidden set is still loading", () => {
    // A never-resolving fetch leaves hiddenKeys() null → visible by default, so
    // rows don't flash hidden during the initial load.
    mocked.fetchRowHidden.mockReturnValue(new Promise<string[]>(() => {}));
    const { result } = renderHook(() =>
      useRowOrder("mainstream", () => ["studios"]),
    );
    expect(result.isHidden("studios")).toBe(false);
  });

  it("isHidden reflects the loaded hidden set", async () => {
    mocked.fetchRowHidden.mockResolvedValue(["studios"]);
    const { result } = renderHook(() =>
      useRowOrder("mainstream", () => ["studios", "performers"]),
    );
    await flush();
    expect(result.isHidden("studios")).toBe(true);
    expect(result.isHidden("performers")).toBe(false);
  });

  it("toggleHidden flips membership and persists each change", async () => {
    const { result } = renderHook(() =>
      useRowOrder("mainstream", () => ["studios"]),
    );
    await flush();
    expect(result.isHidden("studios")).toBe(false);

    result.toggleHidden("studios");
    expect(result.isHidden("studios")).toBe(true);
    expect(mocked.saveRowHidden).toHaveBeenLastCalledWith("mainstream", ["studios"]);

    result.toggleHidden("studios");
    expect(result.isHidden("studios")).toBe(false);
    expect(mocked.saveRowHidden).toHaveBeenLastCalledWith("mainstream", []);
  });
});
