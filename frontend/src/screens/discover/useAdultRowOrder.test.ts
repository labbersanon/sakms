// useAdultRowOrder tests — the optimistic-override state machine both Adult
// surfaces (Discover's inline row editor and Settings' Adult Rows tab) share.
// Asserted directly at the hook level, BELOW the screens: what is proven here
// is the override state machine itself — ordering, the drop of an override id
// no longer in rows(), the clear-on-refetch, and the revert-plus-error on a
// rejected persist.
//
// Deliberately NOT proven here: that a screen derives the right ids from its
// `newestrow:{id}` keys. api/adultNewestRows is mocked, so "sends the FULL id
// set" below can only show persistOrder passes through whatever it was HANDED —
// tautological with respect to the extraction. Both call sites drive a real
// solid-dnd pointer drag and assert the actual outbound POST body instead: see
// Discover.test.tsx and AdultRowAdmin.test.tsx (a drag IS simulable in jsdom
// once the row <li>s are given non-degenerate getBoundingClientRects; the older
// "not simulable" posture in RowEditor.test.tsx was only ever true of a drag
// against jsdom's all-zero default rects).

import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import type { AdultNewestRow } from "../../api/adultNewestRows";
import * as rowsApi from "../../api/adultNewestRows";
import { useAdultRowOrder } from "./useAdultRowOrder";

vi.mock("../../api/adultNewestRows", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("../../api/adultNewestRows")>();
  return { ...actual, reorderAdultNewestRows: vi.fn() };
});

const mocked = vi.mocked(rowsApi);
const flush = () => new Promise((r) => setTimeout(r, 0));

const row = (id: number, title: string): AdultNewestRow => ({
  id,
  title,
  rowType: "scene",
  sortOrder: id,
  enabled: true,
  createdAt: "2026-08-02T00:00:00Z",
  updatedAt: "2026-08-02T00:00:00Z",
});

const a = row(1, "First");
const b = row(2, "Second");
const c = row(3, "Third");

beforeEach(() => {
  vi.clearAllMocks();
  mocked.reorderAdultNewestRows.mockResolvedValue(undefined);
});

describe("useAdultRowOrder — orderedRows", () => {
  it("passes rows() straight through while no override is set", () => {
    const { result } = renderHook(() =>
      useAdultRowOrder(() => [a, b, c], vi.fn()),
    );
    expect(result.orderedRows()).toEqual([a, b, c]);
  });

  it("tolerates an unresolved resource (undefined rows) as an empty list", () => {
    const { result } = renderHook(() =>
      useAdultRowOrder(() => undefined, vi.fn()),
    );
    expect(result.orderedRows()).toEqual([]);
  });

  it("emits override ids first, in override order", async () => {
    const { result } = renderHook(() =>
      useAdultRowOrder(() => [a, b, c], vi.fn()),
    );
    await result.persistOrder([3, 1, 2]);
    expect(result.orderedRows()).toEqual([c, a, b]);
  });

  it("appends rows the override doesn't mention, in rows() order", async () => {
    // A row created on the OTHER surface between this surface's fetch and its
    // drop is absent from the override; it must still render, after everything
    // the override does name, rather than vanishing.
    const { result } = renderHook(() =>
      useAdultRowOrder(() => [a, b, c], vi.fn()),
    );
    await result.persistOrder([2, 1]);
    expect(result.orderedRows()).toEqual([b, a, c]);
  });

  it("drops override ids no longer present in rows() (the post-delete window)", async () => {
    const { result } = renderHook(() =>
      useAdultRowOrder(() => [a, c], vi.fn()),
    );
    // id 2 was deleted on the other surface; it must not surface as a hole or
    // a crash.
    await result.persistOrder([3, 2, 1]);
    expect(result.orderedRows()).toEqual([c, a]);
  });

  it("clears the override once rows() itself changes", async () => {
    const [rows, setRows] = createSignal<AdultNewestRow[]>([a, b]);
    const { result } = renderHook(() => useAdultRowOrder(rows, vi.fn()));
    await result.persistOrder([2, 1]);
    expect(result.orderedRows()).toEqual([b, a]);

    // The refetch landed: the server list is now authoritative and the
    // override has nothing left to say. The new list is a SUPERSET, not the
    // same two rows — with the override still live this would emit
    // [b, a, c] (override order, unmentioned row appended), so the expected
    // [a, b, c] can only come from the override actually having been cleared.
    // Re-setting an equal-length list would pass either way.
    setRows([a, b, c]);
    await flush();
    expect(result.orderedRows()).toEqual([a, b, c]);
  });
});

describe("useAdultRowOrder — persistOrder", () => {
  it("sends the FULL id set to reorderAdultNewestRows and notifies onPersisted", async () => {
    const onPersisted = vi.fn();
    const { result } = renderHook(() =>
      useAdultRowOrder(() => [a, b, c], onPersisted),
    );
    await result.persistOrder([2, 3, 1]);
    // Store.Reorder rejects anything but the exact full current id set, each
    // exactly once — a partial list is a 400, so this assertion is the guard.
    expect(mocked.reorderAdultNewestRows).toHaveBeenCalledWith([2, 3, 1]);
    expect(onPersisted).toHaveBeenCalledTimes(1);
    expect(result.error()).toBe("");
  });

  it("a rejected persist reverts the override AND surfaces the message", async () => {
    mocked.reorderAdultNewestRows.mockRejectedValue(
      new Error("row set changed"),
    );
    const onPersisted = vi.fn();
    const { result } = renderHook(() =>
      useAdultRowOrder(() => [a, b, c], onPersisted),
    );
    await result.persistOrder([3, 2, 1]);
    expect(result.error()).toBe("row set changed");
    // Reverted: the server order is what's shown again, not the failed drag.
    expect(result.orderedRows()).toEqual([a, b, c]);
    expect(onPersisted).not.toHaveBeenCalled();
  });

  it("a later successful persist clears a previous error", async () => {
    mocked.reorderAdultNewestRows.mockRejectedValueOnce(new Error("boom"));
    const { result } = renderHook(() =>
      useAdultRowOrder(() => [a, b], vi.fn()),
    );
    await result.persistOrder([2, 1]);
    expect(result.error()).toBe("boom");

    await result.persistOrder([2, 1]);
    expect(result.error()).toBe("");
    expect(result.orderedRows()).toEqual([b, a]);
  });
});
