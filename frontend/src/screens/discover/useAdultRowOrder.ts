// useAdultRowOrder — the shared drag-reorder state machine for admin-defined
// Adult "newest" rows, driven by BOTH surfaces that render that one list:
// Discover's inline row editor (screens/discover/Adult.tsx) and Settings' Adult
// Rows tab (screens/AdultRowAdmin.tsx). Both now persist to the SAME place —
// adultnewest's `sort_order` column, via POST /api/modes/adult/newest-rows/
// reorder — so an order set on either surface is what the other shows. Adult's
// old per-screen KV order (useRowOrder("adult", …)) is retired; useRowOrder
// itself survives because Mainstream is still a live consumer.
//
// The reorder is OPTIMISTIC: Store.Reorder returns 204 with no body, so the new
// order is only observable after a refetch — without the local override a drag
// would visibly snap back to the old order and only then correct itself. A
// failed persist reverts the override and surfaces the message (Store.Reorder
// rejects an id set that isn't exactly the current full set, which is reachable
// when the two surfaces are open in two tabs and one deletes a row).
//
// clearError exists for the OTHER direction of that error surface. Both hosts
// render this error alongside their own toggle/delete failure signal, and
// persistOrder already clears its own on entry — clearError is what lets a
// toggle/delete clear THIS one on entry, so at most one of the two is ever set
// and a stale reorder error can never mask a later mutation failure (or the
// reverse). Both hosts call it; it is not speculative surface.

import { createEffect, createSignal, on } from "solid-js";
import {
  type AdultNewestRow,
  reorderAdultNewestRows,
} from "../../api/adultNewestRows";

export function useAdultRowOrder(
  // Takes the row RESOURCE accessor directly (hence the `| undefined`, which an
  // uninitialized createResource yields while loading) rather than a caller-side
  // `() => data() ?? []` wrapper, so the effect below reads as what it is: clear
  // the override whenever the server list changes.
  rows: () => AdultNewestRow[] | undefined,
  onPersisted: () => void,
) {
  const [override, setOverride] = createSignal<number[] | null>(null);
  const [error, setError] = createSignal("");

  // orderedRows: ids listed in the override come first, in override order; any
  // row the override doesn't mention is appended in rows() order (a row created
  // on the other surface since the drag); an override id that is no longer in
  // rows() is dropped (the post-delete, pre-effect-clear window).
  const orderedRows = (): AdultNewestRow[] => {
    const list = rows() ?? [];
    const ids = override();
    if (!ids) return list;
    const byId = new Map(list.map((r) => [r.id, r]));
    const ordered: AdultNewestRow[] = [];
    for (const id of ids) {
      const row = byId.get(id);
      if (row) {
        ordered.push(row);
        byId.delete(id);
      }
    }
    for (const row of list) {
      if (byId.has(row.id)) ordered.push(row);
    }
    return ordered;
  };

  const persistOrder = async (ids: number[]) => {
    setOverride(ids);
    setError("");
    try {
      await reorderAdultNewestRows(ids);
      onPersisted();
    } catch (e) {
      setOverride(null);
      setError((e as Error).message);
    }
  };

  // Once the server list itself changes the override has nothing left to say —
  // the refetch already reflects the persisted order. defer:true so mounting
  // doesn't clear an override that was never set.
  createEffect(on(rows, () => setOverride(null), { defer: true }));

  return { orderedRows, persistOrder, error, clearError: () => setError("") };
}
