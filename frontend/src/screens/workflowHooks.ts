// workflowHooks — shared reactive patterns extracted from the four workflow
// screens (Rename, Purge, Dedup, Tag). Three patterns are extracted:
//
//   Pattern A — mode-change cleanup: a `createEffect + on` block that clears
//   the shared actionError and any screen-specific state on mode switch.
//
//   Pattern B — scan and act async wrappers: error capture, busy signal
//   management, and post-success callbacks parameterized per screen.
//
//   Pattern C — bulk selection (useBulkSelection): a reactive Set<K> of selected
//   keys backing the opt-in "Apply/Remove Selected" multi-select. Rename, Purge,
//   and Dedup key on the numeric proposal id (K = number, the default); the
//   Downloads screen keys on the string gid and Requests on a synthetic
//   mode:tmdbId:title string. Genuinely identical across all of them (a set of
//   keys with toggle/select-all/clear), which is why it belongs here rather than
//   copied inline. Tag has no bulk-apply surface and does not use it.
//
// Only these three patterns are here. Screen-specific logic (Clean-up's rules
// card, Dedup keepSel indexing, Rename re-pick panel, Tag draft map) is
// NOT here — it only qualifies if it is genuinely identical or trivially
// parameterizable across all four screens.

import { createEffect, createSignal, on, type Accessor } from "solid-js";
import type { Mode } from "../api/discover";

export interface WorkflowActionsOptions {
  /** Called on every mode change (after actionError is cleared) — resets
   * screen-specific per-mode state (e.g. repickFor, newTag, keepSel, draft). */
  resetOnModeChange?: () => void;

  /** Scan API caller — omit for screens with no scan button (Tag). When
   * provided, `useWorkflowActions` also returns a `scanning` accessor and a
   * `scan` wrapper that manages the busy signal around it. */
  scanFn?: (mode: Mode) => Promise<unknown>;

  /** Called after a successful scan, before refetch — for extra resets that
   * scan (but not act) needs (e.g. Dedup clears keepSel after scan too). */
  resetAfterScan?: () => void;

  /** Called after a successful act, before refetch — for extra resets that
   * act (but not scan) needs (e.g. Rename closes the re-pick panel on act,
   * Dedup clears keepSel after act). Each screen passes its own callback;
   * Purge and Tag pass nothing here. */
  resetAfterAct?: () => void;

  /** The refetch function that runs after scan or act succeeds. For screens
   * with multiple resources (Purge, Tag) this is a wrapper that refetches all
   * of them — the hook never touches resources directly. */
  refetch: () => unknown | Promise<unknown>;
}

export interface WorkflowActions {
  /** Reactive accessor for the current action error string (empty = none). */
  actionError: Accessor<string>;
  /** Setter — screens that clear the error from outside act/scan (e.g. Rename
   * clearing it in the onDone callback of SearchTakeover) use this directly. */
  setActionError: (err: string) => void;
  /** Whether a scan is currently in flight. Always false for screens with no
   * scanFn (Tag). */
  scanning: Accessor<boolean>;
  /** Whether an act (apply/dismiss/batch) is currently in flight. */
  acting: Accessor<boolean>;
  /** Runs the scan: clears error, sets busy, calls scanFn(mode), calls
   * resetAfterScan if provided, then awaits refetch. No-op if scanFn was not
   * provided. */
  scan: (mode: Mode) => Promise<void>;
  /** Runs one proposal mutation fn, captures any error, calls resetAfterAct
   * if provided, then awaits refetch. */
  act: (fn: () => Promise<unknown>) => Promise<void>;
}

export function useWorkflowActions(
  mode: Accessor<Mode>,
  opts: WorkflowActionsOptions,
): WorkflowActions {
  const [actionError, setActionErrorSignal] = createSignal("");
  const [scanning, setScanning] = createSignal(false);
  // Claude 2026-08-06: expose acting so Apply all can disable during the job
  // Reason: Apply all stayed clickable while act()/apply-batch ran (only
  //   scanning/applyAllLoading gated it).
  // Troubleshooting: double Apply all / overlapping batches during long jobs.
  // Review if: every workflow screen wires acting into its bulk Apply control.
  const [acting, setActing] = createSignal(false);

  // Pattern A — mode-change cleanup. Clears shared error first, then calls the
  // screen's own extra reset. { defer: true } prevents firing on initial mount
  // (matches the original createEffect+on behavior in all four screens).
  createEffect(
    on(
      mode,
      () => {
        setActionErrorSignal("");
        opts.resetOnModeChange?.();
      },
      { defer: true },
    ),
  );

  // Pattern B (scan half) — manages busy signal, error, and post-success
  // callbacks. No-op when scanFn was not provided so Tag can call this safely.
  const scan = async (currentMode: Mode): Promise<void> => {
    if (!opts.scanFn) return;
    setActionErrorSignal("");
    setScanning(true);
    try {
      await opts.scanFn(currentMode);
      opts.resetAfterScan?.();
      await opts.refetch();
    } catch (e) {
      setActionErrorSignal((e as Error).message);
    } finally {
      setScanning(false);
    }
  };

  // Pattern B (act half) — error capture + post-success callbacks + refetch.
  const act = async (fn: () => Promise<unknown>): Promise<void> => {
    setActionErrorSignal("");
    setActing(true);
    try {
      await fn();
      opts.resetAfterAct?.();
      await opts.refetch();
    } catch (e) {
      setActionErrorSignal((e as Error).message);
    } finally {
      setActing(false);
    }
  };

  return {
    actionError,
    setActionError: setActionErrorSignal,
    scanning,
    acting,
    scan,
    act,
  };
}

// Pattern C — bulk selection. A reactive Set<K> of selected keys plus the three
// mutations the "Apply/Remove Selected" affordance needs. Every mutation assigns
// a NEW Set (never mutates in place) so SolidJS re-renders the checkbox column
// and action bar — same discipline as Purge's applyingIds guard. K defaults to
// `number` (Rename/Purge/Dedup key on the numeric proposal id, unchanged); the
// Downloads screen keys on the string gid and Requests on a synthetic
// mode:tmdbId:title string, which the generic parameter supports without a
// second hook.
export interface BulkSelection<K = number> {
  /** The current set of selected keys (read to track reactively). */
  selected: Accessor<ReadonlySet<K>>;
  /** True if key is currently selected — reactive when read in JSX. */
  has: (id: K) => boolean;
  /** How many keys are selected (0 = hide the action bar). */
  size: Accessor<number>;
  /** Add key if absent, remove it if present. */
  toggle: (id: K) => void;
  /** Replace the selection with exactly these keys (the select-all header). */
  selectAll: (ids: K[]) => void;
  /** Drop every selection — wired into mode-change, scan, and post-apply. */
  clear: () => void;
}

export function useBulkSelection<K = number>(): BulkSelection<K> {
  const [selected, setSelected] = createSignal<ReadonlySet<K>>(new Set());
  return {
    selected,
    has: (id) => selected().has(id),
    size: () => selected().size,
    toggle: (id) =>
      setSelected((prev) => {
        const next = new Set(prev);
        if (next.has(id)) next.delete(id);
        else next.add(id);
        return next;
      }),
    selectAll: (ids) => setSelected(new Set(ids)),
    clear: () => setSelected(new Set()),
  };
}
