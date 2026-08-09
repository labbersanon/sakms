// Dedup — the staged scan→propose→apply DEDUPLICATION queue. Two view modes
// (AC1): a compact LIST (the original table-per-group structure) and a CARD view
// of candidate tiles with click-to-play video previews. The view mode is
// persisted per mode in localStorage, defaulting to "card".
//
// Structurally DIFFERENT from Rename and Purge (verified against the old
// frontend, do NOT "align" them). Rename/Purge render one flat row per proposal;
// Dedup renders a GROUP of candidate files per proposal, because a duplicate is
// inherently a set, not a single item.
//
// Keeper selection — the safety-critical part (AC5/AC6/AC16). Each group carries
// a PRIMARY keeper (the single tracked copy) plus an optional set of ADDITIONAL
// keepers (extra files left on disk untouched, not tracked). The primary is
// picked with a radio (`Keep {label}`), one per group — the same control the
// original single-keep flow used, so a plain re-pick still means "keep only this
// one, delete the rest" when no additional keepers are checked. Additional
// keepers are a SEPARATE per-candidate checkbox (`Also keep {label}`), shown only
// for non-primary candidates. The invariant the delete path depends on is
// enforced structurally here: the primary is ALWAYS a kept candidate and the
// kept set can never reach zero, because the primary is a radio that cannot be
// deselected (there is always exactly one). Re-picking the primary preserves the
// additional set when the operator is multi-keeping (so it doubles as the
// "change primary" control, moving the old primary into the additional set), and
// drops the old primary in the plain sole-keep case — see onPickPrimary.
//
// Apply sends `{keepIndex: primaryIndex}` plus, only when the additional set is
// non-empty, `additionalKeepIndices: [...]`. The field is OMITTED (never sent as
// []) when empty — required to keep the existing single-keep wire shape and its
// strict request-shape tests intact (AC9). "Keep All" (keep everything, track
// nothing, delete nothing) stays a DISTINCT escape hatch from checking every box
// (track the primary, delete nothing) — see AC15.
//
// Skip (AC7) is client-side only: the proposal's current id is appended to a
// per-mode localStorage set, filtered out of the Pending render, and pruned
// against the live Pending ids on every load — so it self-empties once a scan
// rotates proposal ids (ReplacePending deletes+reinserts).
//
// Bulk apply — the one bounded exception to the project's "one item at a time"
// rule (see ROADMAP.md / CLAUDE.md). Pending cards carry a selection checkbox
// plus a select-all-pending toggle; "Apply Selected (N)" posts one apply-batch
// covering exactly those groups. Per group the batch sends keepIndex when the
// operator overrode the primary radio OR the group has additional keepers (the
// backend rejects additionalKeepIndices with a nil keepIndex), and
// additionalKeepIndices whenever the group has any — threading the multi-keep set
// through the bulk path too (AC13).
//
// keepIndex/additionalKeepIndices are ARRAY INDICES into the proposal's
// `candidates` in received order. Candidates are rendered in exactly that order
// and NEVER sorted — the index sent must line up with proposals.Proposal.Candidates
// or Apply deletes the wrong file (dedup.ApplyLibrary indexes p.Candidates directly).

import {
  type Component,
  batch,
  createEffect,
  createResource,
  createSignal,
  For,
  on,
  onCleanup,
  Show,
} from "solid-js";
import type { ApplyBatchItem, ApplyBatchResponse } from "@dto";
import type { Mode } from "../api/discover";
import {
  type Candidate,
  type Proposal,
  type ProposalStatus,
  applyKeep,
  applyKeepAll,
  dismissProposal,
  fetchDedupProposals,
  fetchDedupVmaf,
  moveProposalMode,
} from "../api/dedup";
import {
  applyBatchStreaming,
  loadPageSize,
  loadShowHistory,
  proposalVideoUrl,
  savePageSize,
  saveShowHistory,
} from "../api/organize";
import {
  BatchResultSummary,
  Button,
  ErrorText,
  modeLabel,
  ModeTabs,
  Muted,
  StatusPill,
} from "../components/ui";
import { type LogLine, useDedupScanStream } from "./dedupScanStream";
import { otherModes } from "./moveTargets";
import { SearchTakeover, type TakeoverPick } from "./SearchTakeover";
import { useBulkSelection, useWorkflowActions } from "./workflowHooks";
import {
  ActivityLogPanel,
  PageSizeSelect,
  PaginationBar,
  ShowHistoryToggle,
} from "./OrganizeChrome";

// winnerIndex returns the index of the group's flagged keeper, defaulting to 0
// when none is flagged (mirrors the backend's own winnerIndex fallback).
const winnerIndex = (candidates: Candidate[]): number => {
  const i = candidates.findIndex((c) => c.winner);
  return i >= 0 ? i : 0;
};

// fmtBitrate renders bitRate as "N kbps" (verbatim from the old frontend's
// fmtBytes) — blank for a missing/zero bitrate.
const fmtBitrate = (bitRate: number | undefined): string =>
  bitRate ? `${Math.round(bitRate / 1000)} kbps` : "";

// fmtSimilarity renders a phash similarity score [0.0–1.0] as a percentage
// string, e.g. "95% similar".
const fmtSimilarity = (s: number): string => `${Math.round(s * 100)}% similar`;

// similarityLabel returns a short confidence descriptor for the given phash
// similarity score. Matches the thresholds from the phash-primary spec.
const similarityLabel = (s: number): string => {
  if (s >= 0.9) return "high confidence duplicate";
  if (s >= 0.7) return "likely duplicate";
  return "possible duplicate — review carefully";
};

// ViewMode toggles the group layout: "list" (compact table) or "card" (tiles
// with a video preview). Persisted per mode in localStorage, defaulting to
// "card" (AC1) — an absent/invalid stored value reads as "card" so tests without
// localStorage setup exercise the default card view.
type ViewMode = "list" | "card";

const viewModeKey = (mode: Mode): string => `sakms.dedup.viewmode.${mode}`;

const readViewMode = (mode: Mode): ViewMode => {
  try {
    return localStorage.getItem(viewModeKey(mode)) === "list" ? "list" : "card";
  } catch {
    return "card";
  }
};

const writeViewMode = (mode: Mode, vm: ViewMode): void => {
  try {
    localStorage.setItem(viewModeKey(mode), vm);
  } catch {
    // storage errors are silent — the in-memory signal still works
  }
};

const skipKey = (mode: Mode): string => `sakms.dedup.skipped.${mode}`;

// readSkipped loads the per-mode client-side "skipped proposal id" set. A
// malformed/absent value reads as an empty set.
const readSkipped = (mode: Mode): Set<number> => {
  try {
    const raw = localStorage.getItem(skipKey(mode));
    if (!raw) return new Set();
    const arr = JSON.parse(raw) as unknown;
    return Array.isArray(arr)
      ? new Set(arr.filter((n): n is number => typeof n === "number"))
      : new Set();
  } catch {
    return new Set();
  }
};

const writeSkipped = (mode: Mode, ids: Set<number>): void => {
  try {
    localStorage.setItem(skipKey(mode), JSON.stringify([...ids]));
  } catch {
    // storage errors are silent — the in-memory signal still works
  }
};

// VMAF_POLL_MS is the bounded re-poll interval while a tile's VMAF score is
// still "computing" (AC17). The backend already rate-limits re-triggering a
// failed pair independently (vmafFailRetryAfter, 60s), so this only paces how
// often the frontend checks for a now-ready score.
const VMAF_POLL_MS = 2500;

// VmafBadge shows one non-primary tile's VMAF score against the group's current
// primary (AC17). It fetches on render, re-polls every VMAF_POLL_MS while the
// backend reports "computing", and stops on "ready", "error", or when the tile
// unmounts / its params change (the createEffect+onCleanup pair). The primary
// tile never renders this (it is the reference); switching primary re-keys the
// effect via referenceIndex, which naturally re-fetches against the new
// reference. An "error" is a small non-blocking indicator — it never disables
// Apply/Skip/Dismiss.
const VmafBadge: Component<{
  mode: Mode;
  proposalId: number;
  candidateIndex: number;
  referenceIndex: number;
}> = (props) => {
  const [state, setState] = createSignal<{
    status: "computing" | "ready" | "error";
    score?: number;
    error?: string;
  }>({ status: "computing" });

  createEffect(
    on(
      () => [
        props.mode,
        props.proposalId,
        props.candidateIndex,
        props.referenceIndex,
      ] as const,
      ([mode, id, candidateIndex, referenceIndex]) => {
        let cancelled = false;
        let timer: ReturnType<typeof setTimeout> | undefined;
        setState({ status: "computing" });

        const poll = async (): Promise<void> => {
          try {
            const r = await fetchDedupVmaf(
              mode,
              id,
              candidateIndex,
              referenceIndex,
            );
            if (cancelled) return;
            if (r.status === "computing") {
              setState({ status: "computing" });
              timer = setTimeout(() => void poll(), VMAF_POLL_MS);
            } else if (r.status === "ready") {
              setState({ status: "ready", score: r.score });
            } else {
              setState({ status: "error", error: r.error });
            }
          } catch (e) {
            if (cancelled) return;
            setState({ status: "error", error: (e as Error).message });
          }
        };
        void poll();

        onCleanup(() => {
          cancelled = true;
          if (timer !== undefined) clearTimeout(timer);
        });
      },
    ),
  );

  return (
    <Show
      when={state().status !== "computing"}
      fallback={
        <span class="text-xs text-muted" aria-label="VMAF computing">
          VMAF…
        </span>
      }
    >
      <Show
        when={state().status === "ready"}
        fallback={
          <span class="text-xs text-warn" title={state().error} aria-label="VMAF unavailable">
            VMAF n/a
          </span>
        }
      >
        <span class="text-xs text-muted" aria-label="VMAF score">
          VMAF {state().score?.toFixed(1)}
        </span>
      </Show>
    </Show>
  );
};

// ScanLogBox is the live per-file scan log: a header showing the neutral
// "Starting scan…" state (before the first real progress) or a clamped ≤100%
// percentage, over a fixed-height, auto-scrolling list of one line per analyzed
// file. Rendered only while a scan is live (see DedupView).
const ScanLogBox: Component<{
  lines: LogLine[];
  progress: { current: number; total: number } | null;
}> = (props) => {
  let box: HTMLDivElement | undefined;
  createEffect(() => {
    props.lines.length;
    if (box) box.scrollTop = box.scrollHeight;
  });
  const pct = (): number => {
    const p = props.progress;
    if (!p || p.total <= 0) return 0;
    return Math.min(p.current / p.total, 1);
  };
  return (
    <div class="mt-4">
      <div class="mb-1 text-sm text-muted">
        <Show when={props.progress} fallback={<span>Starting scan…</span>}>
          Scanning… {Math.round(pct() * 100)}%
        </Show>
      </div>
      <div
        ref={box}
        class="max-h-60 overflow-y-auto rounded-xl border border-border bg-surface p-3 font-mono text-xs text-muted"
      >
        <For each={props.lines}>
          {(line) => (
            <div>
              {line.current}/{line.total} · {line.name}
              {line.phase ? ` · ${line.phase}` : ""}
            </div>
          )}
        </For>
      </div>
    </div>
  );
};

// CandidateMeta is the shared resolution/codec/bitrate badge (existing Candidate
// fields — AC3) rendered in both views.
const CandidateMeta: Component<{ c: Candidate }> = (props) => (
  <span class="text-xs text-muted">
    {props.c.resolution ? `${props.c.resolution}p` : "—"}
    {props.c.codec ? ` · ${props.c.codec}` : ""}
    {fmtBitrate(props.c.bitRate) ? ` · ${fmtBitrate(props.c.bitRate)}` : ""}
  </span>
);

// DedupView is one mode's duplicate-group review queue. Keyed on props.mode so
// the resource refetches when the shell switches tabs.
const DedupView: Component<{ mode: Mode }> = (props) => {
  const [pageSize, setPageSize] = createSignal(loadPageSize("dedup"));
  const [offset, setOffset] = createSignal(0);
  const [logKey, setLogKey] = createSignal(0);
  const [applyProgress, setApplyProgress] = createSignal("");
  const [showHistory, setShowHistory] = createSignal(loadShowHistory("dedup"));
  const listView = (): "live" | "history" =>
    showHistory() ? "history" : "live";
  const [page, { refetch }] = createResource(
    () => ({
      mode: props.mode,
      limit: pageSize(),
      offset: offset(),
      view: listView(),
    }),
    ({ mode, limit, offset, view }) =>
      fetchDedupProposals(mode, limit, offset, view),
  );
  const proposals = () => page()?.items ?? [];
  // keepSel maps a proposal id → the operator's chosen PRIMARY index. Absent
  // means "use the group's flagged winner" (the pre-selected radio). Its
  // presence also flags a batch override (AC13). Cleared on refetch/mode switch.
  const [keepSel, setKeepSel] = createSignal<Record<number, number>>({});
  // additionalKeep maps a proposal id → the set of ADDITIONAL keeper indices
  // (the multi-keep set; never contains the primary). Cleared alongside keepSel.
  const [additionalKeep, setAdditionalKeep] = createSignal<
    Record<number, ReadonlySet<number>>
  >({});
  // viewMode + skippedIds are per-mode, initialized from localStorage and
  // re-read on mode switch (see resetOnModeChange).
  const [viewMode, setViewMode] = createSignal<ViewMode>(readViewMode(props.mode));
  const [skippedIds, setSkippedIds] = createSignal<Set<number>>(
    readSkipped(props.mode),
  );
  const selection = useBulkSelection();
  const [batchResult, setBatchResult] = createSignal<ApplyBatchResponse | null>(
    null,
  );
  // moveFor tracks the one group whose "Move to…" full-page takeover is
  // currently open (AC6) — at most one at a time, across the whole mode's
  // queue. Non-null means the list tree below is swapped out for
  // <SearchTakeover>; it is not an overlay any more, nothing is layered.
  const [moveFor, setMoveFor] = createSignal<{
    proposal: Proposal;
    target: Mode;
  } | null>(null);

  // savedScrollTop preserves the list's scroll offset across a takeover. The
  // scroll host is AppShell's <main>, NOT the document: the shell root is
  // `h-screen overflow-hidden` and <main> is the app's only scroll region, so
  // window.scrollY is permanently 0 and window.scrollTo() does nothing. Reached
  // through a ref on this screen's own root rather than a global
  // document.querySelector, so it asks "what is MY scrolling ancestor" and
  // degrades to null (the isolated screen tests) instead of to a wrong element.
  let rootEl!: HTMLDivElement;
  const scrollHost = (): HTMLElement | null => rootEl?.closest("main") ?? null;
  const [savedScrollTop, setSavedScrollTop] = createSignal<number | null>(null);

  // batch() is REQUIRED here, and this is the one place Dedup's copy of the
  // shared scroll-restore mechanism genuinely differs from Rename's. Dedup's
  // only entry point is a <select onChange> (:965), and `change` is NOT in
  // Solid's delegated-event set — so, unbatched, each setter drives its own
  // synchronous update cycle. The restore effect below would then run in the
  // gap between the two writes, observe savedScrollTop = 400 with moveFor
  // still null and page.loading false, "restore" against the list that has
  // not gone anywhere yet, and CLEAR savedScrollTop — leaving nothing to
  // restore when the takeover actually closes. Rename escapes this only
  // because its entry point is a Button onClick, and `click` IS delegated,
  // so Solid auto-batches it. Caught by Dedup-N4/N4b in Dedup.test.tsx.
  const openTakeover = (t: { proposal: Proposal; target: Mode }): void => {
    batch(() => {
      setSavedScrollTop(scrollHost()?.scrollTop ?? null);
      setMoveFor(t);
    });
  };

  // resetQueueState drops every stale per-scan selection so it never survives a
  // queue refresh or mode switch: the primary overrides, the additional-keep
  // sets, the bulk selection, and the last batch summary. Skipped ids are NOT
  // cleared here — they persist in localStorage and self-prune (see below).
  const resetQueueState = (): void => {
    setKeepSel({});
    setAdditionalKeep({});
    selection.clear();
    setBatchResult(null);
  };

  const { actionError, act } = useWorkflowActions(() => props.mode, {
    resetOnModeChange: () => {
      resetQueueState();
      setViewMode(readViewMode(props.mode));
      setSkippedIds(readSkipped(props.mode));
      setOffset(0);
      setApplyProgress("");
      // ModeTabs lives in the Dedup shell, OUTSIDE this component, so it stays
      // visible and clickable above the takeover — switching Movies→Series
      // mid-takeover would otherwise leave it mounted holding a proposal id
      // from the previous mode, and committing would move the wrong proposal.
      // (Harmless before the takeover, only because the old overlay's
      // `fixed inset-0 z-50` backdrop occluded the tabs.) The saved scroll
      // offset goes with it: it was measured against the OLD mode's list.
      setMoveFor(null);
      setSavedScrollTop(null);
    },
    resetAfterAct: () => {
      setKeepSel({});
      setAdditionalKeep({});
      selection.clear();
      setLogKey((k) => k + 1);
    },
    refetch,
  });

  const scanStream = useDedupScanStream(() => props.mode, {
    refetch: async () => {
      resetQueueState();
      setOffset(0);
      setLogKey((k) => k + 1);
      await refetch();
    },
  });

  // Prune the skipped-id set against the live Pending ids on every load (AC7):
  // once a scan rotates proposal ids (ReplacePending delete+reinsert), the stale
  // skipped ids no longer match anything and are dropped — self-healing.
  // Claude 2026-08-05: wait until a page has loaded — proposals() is [] while
  //   loading, which would wipe localStorage skips before items arrive.
  createEffect(() => {
    const loaded = page();
    if (!loaded) return;
    const list = loaded.items ?? [];
    const pending = new Set(
      list.filter((p) => p.status === "pending").map((p) => p.id),
    );
    setSkippedIds((prev) => {
      const next = new Set([...prev].filter((id) => pending.has(id)));
      if (next.size !== prev.size) writeSkipped(props.mode, next);
      return next;
    });
  });

  // Restore the saved scroll offset exactly once, after the list DOM is
  // genuinely back — not merely after the takeover closes. On the commit path
  // onDone fires refetch() first, so page.loading is already true here and this
  // holds the value until the real table has replaced the Loading… fallback.
  // queueMicrotask is required: the effect runs before Solid has flushed the
  // re-inserted rows, so a synchronous assignment would clamp against the
  // pre-insert height. A null host (no <main> ancestor — the isolated screen
  // tests) is an expected state: clear the value and no-op.
  createEffect(() => {
    const y = savedScrollTop();
    if (y === null) return;
    if (moveFor() !== null) return;
    if (page.loading) return;
    if (proposals().length === 0) {
      setSavedScrollTop(null);
      return;
    }
    const host = scrollHost();
    if (host) {
      queueMicrotask(() => {
        host.scrollTop = y;
      });
    }
    setSavedScrollTop(null);
  });

  // commitMove is Dedup's own commit adapter. It is deliberately NOT shared
  // with Rename's line-for-line twin: the two read different `Proposal` types
  // (../api/dedup vs ../api/rename), and two callers of an eight-line adapter
  // is exactly the case this repo's no-premature-abstraction convention says
  // to leave duplicated. The slot pair is `!= null`, never truthiness — season
  // 0 is Specials and must ship a literal 0.
  const commitMove =
    (p: Proposal, target: Mode) =>
    async (pick: TakeoverPick): Promise<void> => {
      await moveProposalMode(
        p.id,
        pick.kind === "adult"
          ? {
              targetMode: "adult",
              title: pick.title,
              box: pick.box,
              sceneId: pick.sceneId,
              studio: pick.studio,
              date: pick.date,
            }
          : {
              targetMode: target,
              tmdbId: pick.tmdbId,
              title: pick.title,
              year: pick.year,
              ...(pick.seasonNumber != null && pick.episodeNumber != null
                ? {
                    seasonNumber: pick.seasonNumber,
                    episodeNumber: pick.episodeNumber,
                  }
                : {}),
            },
      );
    };

  const setViewModeAndPersist = (vm: ViewMode): void => {
    setViewMode(vm);
    writeViewMode(props.mode, vm);
  };

  // primaryOf is the effective primary keep index for a group: the operator's
  // radio choice if made, else the group's flagged winner. Always a real number
  // (including 0) so Apply never drops a literal-0 index.
  const primaryOf = (p: Proposal): number => {
    const chosen = keepSel()[p.id];
    return chosen ?? winnerIndex(p.candidates ?? []);
  };
  const additionalOf = (p: Proposal): ReadonlySet<number> =>
    additionalKeep()[p.id] ?? new Set<number>();
  const isPrimary = (p: Proposal, i: number): boolean => primaryOf(p) === i;
  const isAlsoKept = (p: Proposal, i: number): boolean =>
    additionalOf(p).has(i);

  // onPickPrimary sets the group's primary keeper to candidate i. When the group
  // has additional keepers checked (multi-keep mode), the OLD primary is moved
  // into the additional set so the whole kept set is preserved and only the
  // TRACKED copy changes (this doubles as the "change primary" control, AC5).
  // With no additional keepers (the plain sole-keep case, matching the original
  // single-keep flow and its tests), the old primary is simply dropped — "keep
  // only this one, delete the rest". i is removed from the additional set either
  // way, since the primary is never simultaneously an additional keeper.
  const onPickPrimary = (p: Proposal, i: number): void => {
    const oldPrimary = primaryOf(p);
    const prevAdd = additionalOf(p);
    // Multi-keep mode must be read from the set BEFORE removing i — checking
    // `add.size > 0` after the delete silently misclassified "promote the
    // sole also-kept candidate" as sole-keep mode, dropping oldPrimary out of
    // the kept set entirely instead of folding it into the additional set
    // (a real data-loss bug: the operator's explicitly-checked file got
    // deleted on Apply). See Dedup.test.tsx's "single additional keeper" case.
    const wasMultiKeep = prevAdd.size > 0;
    const add = new Set(prevAdd);
    add.delete(i);
    if (wasMultiKeep && oldPrimary !== i) add.add(oldPrimary);
    setKeepSel((prev) => ({ ...prev, [p.id]: i }));
    setAdditionalKeep((prev) => ({ ...prev, [p.id]: add }));
  };

  // onToggleAlsoKeep adds/removes candidate i from the group's additional-keep
  // set. Only ever called for a NON-primary candidate (the primary has no
  // also-keep control), so it can never touch the primary or empty the kept set.
  const onToggleAlsoKeep = (p: Proposal, i: number): void => {
    setAdditionalKeep((prev) => {
      const add = new Set(prev[p.id] ?? []);
      if (add.has(i)) add.delete(i);
      else add.add(i);
      return { ...prev, [p.id]: add };
    });
  };

  // pendingIds are the groups selectable/batchable — only Pending cards resolve,
  // and only those not currently skipped.
  const pendingIds = (): number[] =>
    proposals()
      .filter((p) => p.status === "pending" && !skippedIds().has(p.id))
      .map((p) => p.id);
  const allPendingSelected = (): boolean => {
    const ids = pendingIds();
    return ids.length > 0 && ids.every((id) => selection.has(id));
  };
  const toggleSelectAll = (): void => {
    if (allPendingSelected()) selection.clear();
    else selection.selectAll(pendingIds());
  };
  const titleOf = (id: number): string => {
    const p = proposals().find((x) => x.id === id);
    return p ? p.title || p.sourceName || "" : "";
  };

  // visibleProposals hides skipped Pending groups from the render (AC7); a
  // skipped group that later becomes non-pending (applied/dismissed) is not
  // hidden — skip only suppresses the Pending review row.
  const visibleProposals = (): Proposal[] =>
    proposals().filter(
      (p) => !(p.status === "pending" && skippedIds().has(p.id)),
    );

  const onSkip = (p: Proposal): void => {
    setSkippedIds((prev) => {
      const next = new Set(prev);
      next.add(p.id);
      writeSkipped(props.mode, next);
      return next;
    });
    // Drop a now-hidden group from the bulk selection so it can't be
    // batch-applied while skipped out of view.
    if (selection.has(p.id)) selection.toggle(p.id);
  };

  // onApply resolves one group: keep the primary + any additional keepers,
  // delete the rest. The additional set is sent only when non-empty (omitted,
  // never [], for the single-keep case — AC9).
  const onApply = (p: Proposal): void => {
    const primary = primaryOf(p);
    const add = [...additionalOf(p)];
    void act(() => applyKeep(p.id, primary, add));
  };

  // applySelected posts ONE apply-batch for the selected Pending groups. Per
  // group keepIndex rides along when the operator overrode the radio OR the
  // group has additional keepers (the backend rejects additionalKeepIndices with
  // a nil keepIndex); additionalKeepIndices rides along whenever the group has
  // any (AC13). keepSel()[id] may legitimately be 0, so the override presence
  // check is `!== undefined`, never a falsy test.
  const applySelected = (): void => {
    const overrides = keepSel();
    const items: ApplyBatchItem[] = [...selection.selected()].map((id) => {
      const p = proposals().find((x) => x.id === id);
      const add = p ? [...additionalOf(p)] : [];
      const overridden = overrides[id] !== undefined;
      const item: ApplyBatchItem = { id, sourcePath: p?.sourcePath };
      if (overridden || add.length > 0) {
        item.keepIndex = p ? primaryOf(p) : (overrides[id] ?? 0);
      }
      if (add.length > 0) item.additionalKeepIndices = add;
      return item;
    });
    if (items.length === 0) return;
    setBatchResult(null);
    setApplyProgress("");
    void act(async () => {
      const out = await applyBatchStreaming(items, (done, total) => {
        setApplyProgress(`Applied ${done}/${total}…`);
      });
      setBatchResult(out as ApplyBatchResponse);
      setApplyProgress("");
      setLogKey((k) => k + 1);
    });
  };

  return (
    <>
      <Show when={!moveFor()}>
        <div ref={rootEl}>
          <div class="flex flex-wrap items-center gap-3">
            <Button
              variant="primary"
              onClick={() => scanStream.initiate(props.mode)}
              disabled={scanStream.scanning()}
            >
              {scanStream.scanning() ? "Scanning…" : "Scan"}
            </Button>
            {/* List/Card view toggle (AC1) — mirrors Tag's Table/Grid toggle. */}
            <div class="flex items-center gap-1">
              <button
                type="button"
                class="rounded-md px-3 py-1.5 text-sm font-medium transition"
                classList={{
                  "bg-accent text-accent-fg": viewMode() === "list",
                  "bg-surface-2 text-muted hover:text-fg": viewMode() !== "list",
                }}
                onClick={() => setViewModeAndPersist("list")}
              >
                List
              </button>
              <button
                type="button"
                class="rounded-md px-3 py-1.5 text-sm font-medium transition"
                classList={{
                  "bg-accent text-accent-fg": viewMode() === "card",
                  "bg-surface-2 text-muted hover:text-fg": viewMode() !== "card",
                }}
                onClick={() => setViewModeAndPersist("card")}
              >
                Card
              </button>
            </div>
            <Show when={pendingIds().length > 0}>
              <label class="flex items-center gap-2 text-sm text-muted">
                <input
                  type="checkbox"
                  aria-label="Select all pending"
                  checked={allPendingSelected()}
                  onChange={toggleSelectAll}
                />
                Select all pending
              </label>
            </Show>
            <Show when={selection.size() > 0}>
              <Button variant="primary" onClick={applySelected}>
                Apply Selected ({selection.size()})
              </Button>
            </Show>
            <PageSizeSelect
              value={pageSize()}
              onChange={(n) => {
                savePageSize("dedup", n);
                setPageSize(n);
                setOffset(0);
              }}
            />
            <ShowHistoryToggle
              checked={showHistory()}
              onChange={(on) => {
                saveShowHistory("dedup", on);
                setShowHistory(on);
                setOffset(0);
                selection.clear();
              }}
            />
          </div>

          <Show when={applyProgress()}>
            <Muted class="mt-2">{applyProgress()}</Muted>
          </Show>

          <Show when={actionError()}>
            <ErrorText>{actionError()}</ErrorText>
          </Show>

          <Show
            when={scanStream.scanError()}
            fallback={
              <Show
                when={scanStream.scanning() || scanStream.logLines().length > 0}
              >
                <ScanLogBox
                  lines={scanStream.logLines()}
                  progress={scanStream.progress()}
                />
              </Show>
            }
          >
            <ErrorText>{scanStream.scanError()}</ErrorText>
          </Show>

          <Show when={batchResult()}>
            {(res) => <BatchResultSummary result={res()} titleOf={titleOf} />}
          </Show>

          <Show when={page.error}>
            <ErrorText>{(page.error as Error)?.message}</ErrorText>
          </Show>
          <Show
            when={!page.loading}
            fallback={<Muted class="mt-4">Loading…</Muted>}
          >
            <Show
              when={visibleProposals().length > 0}
              fallback={
                <Show when={!scanStream.scanning()}>
                  <Muted class="mt-4">
                    {showHistory()
                      ? "No history yet."
                      : "No duplicate groups yet — click Scan."}
                  </Muted>
                </Show>
              }
            >
              <div class="mt-4 flex flex-col gap-4">
                <For each={visibleProposals()}>
                  {(p) => {
                    const status = () => p.status as ProposalStatus;
                    const candidates = () => p.candidates ?? [];
                    const radioName = `keep-${p.id}`;
                    const pending = () => status() === "pending";
                    return (
                      <div class="rounded-xl border border-border bg-surface p-4">
                        <div class="flex items-center gap-2">
                          <Show when={pending()}>
                            <input
                              type="checkbox"
                              aria-label={`Select ${p.title || p.sourceName || ""}`}
                              checked={selection.has(p.id)}
                              onChange={() => selection.toggle(p.id)}
                            />
                          </Show>
                          <strong class="text-fg">
                            {p.title || p.sourceName || ""}
                          </strong>
                          <StatusPill status={p.status} />
                          <Show when={(p.pHashSimilarity ?? 0) > 0}>
                            <span class="text-xs text-muted">
                              {fmtSimilarity(p.pHashSimilarity!)} ·{" "}
                              {similarityLabel(p.pHashSimilarity!)}
                            </span>
                          </Show>
                        </div>

                        {/* CARD view — a row of candidate tiles with a click-to-play
                            video, metadata badge, keep controls, and (non-primary)
                            VMAF score. */}
                        <Show when={viewMode() === "card"}>
                          <div class="mt-3 flex flex-wrap gap-3">
                            <For each={candidates()}>
                              {(c, i) => (
                                <div class="w-64 shrink-0 rounded-xl border border-border bg-surface p-4">
                                  {/* Not Card: the label can be a long unwrapped
                                      filename, and Card's title is a plain
                                      unstyled <h3> with no way to inject a
                                      truncate class or title= tooltip from the
                                      call site. This renders Card's exact
                                      classes plus a truncated, tooltipped
                                      heading so the fixed-width card row stays
                                      even while the full label stays reachable
                                      on hover and in the DOM (getByText still
                                      finds it — only CSS clips it). Card's own
                                      mb-4 is dropped here — this row's gap-3
                                      already spaces cards on both axes, and
                                      mb-4 was sized for Card's original stacked
                                      (non-flex) usage. */}
                                  <h3
                                    class="mb-2 truncate px-2 text-sm font-semibold text-fg"
                                    title={c.label}
                                  >
                                    {c.label}
                                  </h3>
                                  <div class="w-56">
                                    {/* preload="none": no bytes are fetched until
                                        the operator hits play (AC3, click-to-play).
                                        muted default; each <video> is independent so
                                        unmuting one does not affect others (AC4). */}
                                    <video
                                      class="mb-2 w-full rounded bg-surface-2"
                                      controls
                                      muted
                                      preload="none"
                                      src={proposalVideoUrl(props.mode, p.id, i())}
                                      aria-label={`Preview ${c.label}`}
                                    />
                                    <div class="mb-2 flex items-center justify-between gap-2">
                                      <CandidateMeta c={c} />
                                      <Show
                                        when={!isPrimary(p, i())}
                                        fallback={
                                          <span class="text-xs font-medium text-ok">
                                            primary
                                          </span>
                                        }
                                      >
                                        <VmafBadge
                                          mode={props.mode}
                                          proposalId={p.id}
                                          candidateIndex={i()}
                                          referenceIndex={primaryOf(p)}
                                        />
                                      </Show>
                                    </div>
                                    <Show when={pending()}>
                                      <div class="flex flex-col gap-1">
                                        <label class="flex items-center gap-2 text-sm text-fg">
                                          <input
                                            type="radio"
                                            name={radioName}
                                            value={i()}
                                            checked={isPrimary(p, i())}
                                            aria-label={`Keep ${c.label}`}
                                            onChange={() => onPickPrimary(p, i())}
                                          />
                                          Primary (tracked)
                                        </label>
                                        <Show when={!isPrimary(p, i())}>
                                          <label class="flex items-center gap-2 text-sm text-muted">
                                            <input
                                              type="checkbox"
                                              checked={isAlsoKept(p, i())}
                                              aria-label={`Also keep ${c.label}`}
                                              onChange={() =>
                                                onToggleAlsoKeep(p, i())
                                              }
                                            />
                                            Also keep
                                          </label>
                                        </Show>
                                      </div>
                                    </Show>
                                  </div>
                                </div>
                              )}
                            </For>
                          </div>
                        </Show>

                        {/* LIST view — the original compact table, plus an
                            Also-keep column for the multi-keep set. */}
                        <Show when={viewMode() === "list"}>
                          <div class="mt-3 overflow-x-auto">
                            <table class="w-full text-left text-sm">
                              <thead>
                                <tr class="border-b border-border text-xs uppercase tracking-wide text-muted">
                                  <th class="px-2 py-2 font-medium">Keep</th>
                                  <th class="px-2 py-2 font-medium">Also keep</th>
                                  <th class="px-2 py-2 font-medium">Label</th>
                                  <th class="px-2 py-2 font-medium">Path</th>
                                  <th class="px-2 py-2 font-medium">Resolution</th>
                                  <th class="px-2 py-2 font-medium">Codec</th>
                                  <th class="px-2 py-2 font-medium">Bitrate</th>
                                </tr>
                              </thead>
                              <tbody>
                                <For each={candidates()}>
                                  {(c, i) => (
                                    <tr class="border-b border-border/60 align-top">
                                      <td class="px-2 py-2">
                                        <input
                                          type="radio"
                                          name={radioName}
                                          value={i()}
                                          checked={isPrimary(p, i())}
                                          aria-label={`Keep ${c.label}`}
                                          onChange={() => onPickPrimary(p, i())}
                                        />
                                      </td>
                                      <td class="px-2 py-2">
                                        <Show
                                          when={!isPrimary(p, i())}
                                          fallback={<span class="text-xs text-muted">—</span>}
                                        >
                                          <input
                                            type="checkbox"
                                            checked={isAlsoKept(p, i())}
                                            aria-label={`Also keep ${c.label}`}
                                            onChange={() => onToggleAlsoKeep(p, i())}
                                          />
                                        </Show>
                                      </td>
                                      <td class="px-2 py-2 text-fg">{c.label}</td>
                                      <td class="px-2 py-2 text-muted">{c.path}</td>
                                      <td class="px-2 py-2 text-muted">
                                        {c.resolution ? `${c.resolution}p` : ""}
                                      </td>
                                      <td class="px-2 py-2 text-muted">
                                        {c.codec || ""}
                                      </td>
                                      <td class="px-2 py-2 text-muted">
                                        {fmtBitrate(c.bitRate)}
                                      </td>
                                    </tr>
                                  )}
                                </For>
                              </tbody>
                            </table>
                          </div>
                        </Show>

                        <div class="mt-3 flex flex-wrap items-center gap-2">
                          {/* "Move to…" is deliberately gated on
                              pending-or-unmatched, NOT the pending-only Show
                              below: the backend accepts a move for either status
                              (§4.4 step 6), and an Unmatched group is exactly the
                              one an operator most needs to move — the one that's
                              stuck. Gating it inside the pending-only block would
                              hide the affordance where it matters most. */}
                          <Show when={pending() || p.status === "unmatched"}>
                            <select
                              class="rounded border border-border bg-bg px-2 py-1 text-sm text-fg"
                              aria-label={`Move group ${p.title ?? p.sourceName} to another mode`}
                              value=""
                              onChange={(e) => {
                                const target = e.currentTarget.value as Mode | "";
                                // Reset back to the placeholder immediately so
                                // re-selecting the SAME target later still fires
                                // onChange (a select that keeps its "selected"
                                // state would not re-fire for a repeat pick).
                                e.currentTarget.value = "";
                                if (!target) return;
                                // openTakeover, not setMoveFor: the scroll offset
                                // must be read off <main> BEFORE the list is
                                // swapped out for the takeover.
                                openTakeover({ proposal: p, target });
                              }}
                            >
                              <option value="" disabled>
                                Move to…
                              </option>
                              <For each={otherModes(props.mode)}>
                                {(m) => (
                                  <option value={m}>
                                    Move to {modeLabel(m)}
                                  </option>
                                )}
                              </For>
                            </select>
                          </Show>
                          <Show when={pending()}>
                            <Button variant="primary" onClick={() => onApply(p)}>
                              Apply
                            </Button>
                            <Button
                              onClick={() => void act(() => applyKeepAll(p.id))}
                            >
                              Keep All
                            </Button>
                            <Button onClick={() => onSkip(p)}>Skip</Button>
                            <Button
                              class="!bg-danger !text-accent-fg"
                              onClick={() => void act(() => dismissProposal(p.id))}
                            >
                              Dismiss
                            </Button>
                          </Show>
                        </div>
                      </div>
                    );
                  }}
                </For>
              </div>
            </Show>
          </Show>

          <PaginationBar
            total={page()?.total ?? 0}
            limit={pageSize()}
            offset={offset()}
            onPrev={() => setOffset((o) => Math.max(0, o - pageSize()))}
            onNext={() => setOffset((o) => o + pageSize())}
          />
          <ActivityLogPanel workflow="dedup" refreshKey={logKey()} />
        </div>
      </Show>

      {/* The takeover REPLACES the list in place (two sibling <Show>s, not a
          fallback prop, so the list tree is genuinely not created while it is
          open). ModeTabs stays visible above it — it lives in the Dedup shell,
          outside DedupView — which is deliberate: the tab bar is the app's mode
          chrome, and a mode switch safely closes the takeover through
          resetOnModeChange. */}
      <Show when={moveFor()}>
        {(mf) => (
          // searchMode is the TARGET mode, not props.mode: it selects both the
          // search endpoint (scene-search for Adult) and the shape of the pick
          // the commit adapter receives. autoSearch is false and load-bearing —
          // a mount-time GET would break the "Cancel issues zero requests"
          // guarantee this entry point has always had.
          <SearchTakeover
            heading={`Move “${mf().proposal.title || mf().proposal.sourceName || ""}” to another section`}
            initialQuery={mf().proposal.title || mf().proposal.sourceName || ""}
            searchMode={mf().target}
            autoSearch={false}
            notes={
              <>
                <Muted>
                  {"Files stay where they are; only the group's mode changes. Run a Rename scan in the target mode to relocate them."}
                </Muted>
                <Show when={mf().target === "adult"}>
                  <Muted class="mt-1">
                    This file has no perceptual hash yet, so it will be renamed
                    without its [phash-...] tag. A later Adult scan will add it.
                  </Muted>
                </Show>
              </>
            }
            onCommit={commitMove(mf().proposal, mf().target)}
            onDone={() => {
              // refetch() FIRST, then close, both inside batch(). onDone runs in
              // a post-await microtask, which Solid does NOT auto-batch — with
              // the setter first, the scroll-restore effect would observe
              // page.loading === false, restore into the about-to-be-replaced
              // DOM, and silently discard the offset on every commit.
              batch(() => {
                void refetch();
                setMoveFor(null);
              });
            }}
            onCancel={() => setMoveFor(null)}
          />
        )}
      </Show>
    </>
  );
};

// Dedup is the mode-switching shell: tab bar (Movies/Series/Adult) over the
// matching duplicate-group review queue.
export const Dedup: Component = () => {
  const [mode, setMode] = createSignal<Mode>("movies");
  return (
    <div>
      <ModeTabs current={mode} onSelect={setMode} />
      <DedupView mode={mode()} />
    </div>
  );
};
