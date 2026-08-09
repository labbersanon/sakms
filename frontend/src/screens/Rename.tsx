// Rename — the staged scan→propose→apply review queue, ported from the
// vanilla-JS frontend (internal/web/static/index.html's renderRename), with
// mode-specific columns (Wade-approved follow-up, see
// .omc/handoffs/stage-3-rename.md).
//
// Claude 2026-08-06: Status column removed; dropdown is Rename/Re-pick/Dismiss
//   with Rename auto-selected when a match is found; one Apply button.
// Reason: Status "pending"/"rename" pill duplicated the action; operators want
//   Rename in the select (not Apply) and no Status column.
// Troubleshooting: two Apply buttons / Apply in dropdown → use Rename option.
// Review if: Re-pick becomes batchable (needs a chosen alternate).

import {
  batch,
  type Component,
  createEffect,
  createResource,
  createSignal,
  For,
  Show,
} from "solid-js";
import type { Mode } from "../api/discover";
import type { ApplyBatchResponse, ApplyBatchResultItem } from "@dto";
import {
  type Proposal,
  applyBatchStreaming,
  applyProposal,
  deleteBatch,
  dismissProposal,
  fetchProposals,
  moveProposalMode,
  repickProposal,
  scanRename,
} from "../api/rename";
import {
  loadPageSize,
  loadShowHistory,
  proposalVideoUrl,
  savePageSize,
  saveShowHistory,
} from "../api/organize";
import {
  SourcePreviewDisclosure,
  SourcePreviewPopout,
} from "../components/SourcePreview";
import {
  BatchResultSummary,
  Button,
  ErrorText,
  modeLabel,
  ModeTabs,
  Muted,
} from "../components/ui";
import { useWorkflowActions } from "./workflowHooks";
import { SearchTakeover, type TakeoverPick } from "./SearchTakeover";
import { otherModes } from "./moveTargets";
import {
  ActivityLogPanel,
  PageSizeSelect,
  PaginationBar,
  ShowHistoryToggle,
} from "./OrganizeChrome";

type RowActionId =
  | "rename"
  | "repick"
  | "dismiss"
  | "delete"
  | "move:movies"
  | "move:series"
  | "move:adult"
  | "preview";

// Claude 2026-08-09: "Delete file", not "Delete", and LAST in the base list.
// Reason: every other option here acts on the PROPOSAL; this one destroys the
//   FILE. Two words of disambiguation on a permanent action is cheap, and the
//   operator sees this label far more often than the confirm modal. Last so it
//   is never adjacent to the auto-selected "Rename" default.
// Troubleshooting: an operator reporting they meant to dismiss, not delete.
// Review if: the dropdown grows a second destructive entry — the positional
//   argument stops carrying its weight and needs a visual separator instead.
// Context: .omc/plans/autopilot-impl.md §3.2.
const BASE_ROW_ACTIONS: { id: RowActionId; label: string }[] = [
  { id: "rename", label: "Rename" },
  { id: "repick", label: "Re-pick" },
  { id: "dismiss", label: "Dismiss" },
  { id: "delete", label: "Delete file" },
];

// rowActions is a function of the proposal's CURRENT mode: the two move
// entries offered are always the OTHER two modes, never the current one.
const rowActions = (m: Mode): { id: RowActionId; label: string }[] => [
  ...BASE_ROW_ACTIONS,
  ...otherModes(m).map((t) => ({
    id: `move:${t}` as RowActionId,
    label: `Move to ${modeLabel(t)}`,
  })),
];

function rowActionEnabled(
  id: RowActionId,
  status: string,
  titleMode: boolean,
): boolean {
  switch (id) {
    case "rename":
      return status === "pending";
    case "repick":
      return titleMode && (status === "pending" || status === "unmatched");
    case "dismiss":
      return status === "pending" || status === "unmatched";
    case "delete":
      // Same eligibility as dismiss. Deliberately NOT gated on titleMode: like
      // the move:* entries below, the row-level control's availability is
      // independent of PIN-lock state — the Adult section lock is enforced
      // server-side (mode.Build), not by hiding this option.
      return status === "pending" || status === "unmatched";
    // Move affordances are deliberately NOT gated on titleMode (AC7): the
    // row-level control is available regardless of PIN-lock state; the
    // actual PIN gating happens deeper, in the panel's commit call.
    case "move:movies":
    case "move:series":
    case "move:adult":
      return status === "pending" || status === "unmatched";
    case "preview":
      // Same eligibility as dismiss/delete: Pending and Unmatched only. An
      // Applied row's sourcePath no longer points anywhere (the file moved);
      // a Dismissed row's may or may not, and neither is worth previewing.
      // Deliberately NOT added to BASE_ROW_ACTIONS above — this is a row
      // control rendered in the Source cell, not a dropdown action, so
      // rowActions() (which builds the dropdown from BASE_ROW_ACTIONS only)
      // never sees it. An id in RowActionId with no BASE_ROW_ACTIONS entry
      // looks like an omission; it is not.
      return status === "pending" || status === "unmatched";
  }
}

/** Match found → Rename is auto-selected. */
function defaultRowAction(
  status: string,
  titleMode: boolean,
): RowActionId | "" {
  if (status === "pending" && rowActionEnabled("rename", status, titleMode)) {
    return "rename";
  }
  return "";
}

// Claude 2026-08-09: exported (was module-private).
// Reason: Rename.delete.test.tsx (US-005) asserts directly against this
//   function for the "stale/invalid selection on an ineligible status never
//   enters the plan" guard — that assertion needs the real function, not a
//   UI-driven proxy for it. No behavior change.
// Review if: this acquires a second exported test-only helper — consider a
//   dedicated test-utils re-export instead of widening the module surface.
export function planActionForRow(
  p: Proposal,
  dropdown: RowActionId | "",
  titleMode: boolean,
): "apply" | "dismiss" | "delete" | null {
  const action =
    dropdown || defaultRowAction(p.status, titleMode);
  if (action === "rename" && rowActionEnabled("rename", p.status, titleMode)) {
    return "apply";
  }
  if (action === "dismiss" && rowActionEnabled("dismiss", p.status, titleMode)) {
    return "dismiss";
  }
  // Each branch re-checks rowActionEnabled so a stale dropdown selection on a
  // row whose status changed underneath it is DROPPED from the plan rather
  // than executed. That re-check is what makes the client-side gate honest,
  // and it matters far more for delete than for the other two — do not
  // collapse it as redundant.
  if (action === "delete" && rowActionEnabled("delete", p.status, titleMode)) {
    return "delete";
  }
  return null;
}

function newNameOf(p: Proposal): string {
  if (p.title) {
    return p.year ? `${p.title} (${p.year})` : p.title;
  }
  return "(unknown)";
}

async function fetchAllLiveProposals(mode: Mode): Promise<Proposal[]> {
  const out: Proposal[] = [];
  let offset = 0;
  const limit = 100;
  for (;;) {
    const page = await fetchProposals(mode, limit, offset, "live");
    out.push(...page.items);
    offset += page.items.length;
    if (page.items.length === 0 || offset >= page.total) break;
  }
  return out;
}

// This literal union is declared SEPARATELY from planActionForRow's return
// type, not by reference to it. That duplication is deliberate: it is what
// makes PLAN_ACTION_LABEL's Record<ApplyAllPlanItem["action"], string> below a
// real compile-time guard rather than a tautology. Edit the two in lockstep.
type ApplyAllPlanItem = {
  proposal: Proposal;
  action: "apply" | "dismiss" | "delete";
};

const RowActions: Component<{
  proposal: Proposal;
  mode: Mode;
  titleMode: boolean;
  selected: RowActionId | "";
  onSelect: (id: RowActionId | "") => void;
  onRun: (id: RowActionId) => void;
  disabled?: boolean;
}> = (props) => {
  const actions = () => rowActions(props.mode);
  const enabled = (id: RowActionId) =>
    rowActionEnabled(id, props.proposal.status, props.titleMode);
  const hasAny = () => actions().some((a) => enabled(a.id));
  const selectedOk = () => {
    const id = props.selected;
    return id && enabled(id) ? id : "";
  };
  const locked = () => !!props.disabled;

  createEffect(() => {
    const cur = props.selected;
    if (cur && !enabled(cur)) {
      props.onSelect(
        defaultRowAction(props.proposal.status, props.titleMode),
      );
    }
  });

  const run = () => {
    if (locked()) return;
    const id = selectedOk();
    if (!id) return;
    props.onRun(id);
  };

  return (
    <div class="flex flex-wrap items-center gap-1">
      <select
        class="rounded border border-border bg-bg px-2 py-1 text-sm text-fg"
        aria-label={`Action for ${props.proposal.sourceName}`}
        value={props.selected}
        disabled={!hasAny() || locked()}
        onChange={(e) =>
          props.onSelect(e.currentTarget.value as RowActionId | "")
        }
      >
        {/* Placeholder must stay enabled — Chromium skips disabled empty options. */}
        <option value="">select action</option>
        <For each={actions()}>
          {(a) => (
            <option value={a.id} disabled={!enabled(a.id)}>
              {a.label}
            </option>
          )}
        </For>
      </select>
      <Show when={selectedOk()}>
        <Button
          variant="primary"
          aria-label={`Apply selected action for ${props.proposal.sourceName}`}
          disabled={locked()}
          onClick={run}
        >
          Apply
        </Button>
      </Show>
    </div>
  );
};

function shortHash(hash: string): string {
  return hash.length > 12 ? `${hash.slice(0, 12)}…` : hash;
}

function episodeDisplay(
  episodeNumber?: number,
  extraEpisodeNumbers?: number[],
): string {
  if (episodeNumber == null) return "";
  if (!extraEpisodeNumbers || extraEpisodeNumbers.length === 0) {
    return String(episodeNumber);
  }
  const last = extraEpisodeNumbers[extraEpisodeNumbers.length - 1];
  return `${episodeNumber}-${last}`;
}

// Claude 2026-08-09: explicit per-action label/detail maps, no ternary
//   catch-all. Module scope (not inside ApplyAllConfirm) because that
//   component is a concise arrow body, and because both are pure functions of
//   their inputs — same placement as newNameOf above.
// Reason: both confirm-table cells used to be `action === "apply" ? X : Y`,
//   with dismiss as the IMPLICIT else. Adding a third action would have
//   silently rendered "Dismiss proposal" as the Detail for a permanent file
//   deletion — TypeScript-clean, and the operator's one confirmation step
//   would have been actively lying. Same defect shape as the
//   move:-before-dismiss catch-all fixed in runRowAction below.
// Troubleshooting: the confirm modal describing the wrong action for a row.
// Review if: ApplyAllPlanItem["action"] gains a fourth member — it needs an
//   entry in BOTH structures below, and the Record<> type plus the exhaustive
//   switch will fail the build if it does not get one.
// Context: .omc/plans/autopilot-impl.md §3.6.
const PLAN_ACTION_LABEL: Record<ApplyAllPlanItem["action"], string> = {
  apply: "rename",
  dismiss: "dismiss",
  delete: "DELETE FILE",
};

// Full sourcePath, not sourceName: the Original-name column already shows the
// bare name, and for an irreversible deletion the operator should see the
// absolute path being destroyed.
const planActionDetail = (item: ApplyAllPlanItem): string => {
  switch (item.action) {
    case "apply":
      return newNameOf(item.proposal);
    case "dismiss":
      return "Dismiss proposal";
    case "delete":
      return `Permanently delete ${item.proposal.sourcePath} — cannot be undone`;
  }
};

const ApplyAllConfirm: Component<{
  plan: ApplyAllPlanItem[];
  onCancel: () => void;
  onConfirm: () => void;
}> = (props) => (
  <div
    class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
    role="dialog"
    aria-modal="true"
    aria-label="Confirm apply all"
  >
    <div class="max-h-[80vh] w-full max-w-2xl overflow-hidden rounded-xl border border-border bg-surface shadow-lg">
      <div class="border-b border-border px-4 py-3">
        <h3 class="text-base font-semibold text-fg">
          Apply {props.plan.length} selected action
          {props.plan.length === 1 ? "" : "s"}?
        </h3>
        <Muted class="mt-1">
          Runs each row’s dropdown choice (Rename is selected when a match is
          found; Re-pick is skipped — needs a manual pick).
        </Muted>
        <Show when={props.plan.some((x) => x.action === "delete")}>
          <p class="mt-2 rounded-md border border-danger/40 bg-danger/10 px-3 py-2 text-sm text-danger">
            {props.plan.filter((x) => x.action === "delete").length} file(s)
            will be permanently deleted from disk. This cannot be undone —
            there is no trash or recycle bin.
          </p>
        </Show>
      </div>
      <div class="max-h-[50vh] overflow-y-auto px-4 py-2">
        <table class="w-full text-left text-sm">
          <thead>
            <tr class="text-xs uppercase tracking-wide text-muted">
              <th class="py-2 pr-2 font-medium">Original name</th>
              <th class="py-2 pr-2 font-medium">Action</th>
              <th class="py-2 font-medium">Detail</th>
            </tr>
          </thead>
          <tbody>
            <For each={props.plan}>
              {(item) => (
                <tr class="border-t border-border/60 align-top">
                  <td class="py-2 pr-2 font-mono text-xs">
                    {item.proposal.sourceName}
                  </td>
                  <td class="py-2 pr-2 text-sm capitalize">
                    {PLAN_ACTION_LABEL[item.action]}
                  </td>
                  <td class="py-2 text-sm">{planActionDetail(item)}</td>
                </tr>
              )}
            </For>
          </tbody>
        </table>
      </div>
      <div class="flex justify-end gap-2 border-t border-border px-4 py-3">
        <Button variant="secondary" onClick={props.onCancel}>
          Cancel
        </Button>
        <Button variant="primary" onClick={props.onConfirm}>
          Confirm
        </Button>
      </div>
    </div>
  </div>
);

// TakeoverState replaces the two former `repickFor` / `moveFor` signals with
// ONE discriminated value, because the two entry points now render the SAME
// component (SearchTakeover) in the SAME slot — two independent signals could
// both be non-null at once and would race for that one slot.
// See .omc/plans/autopilot-impl.md §5.1 R2.
type TakeoverState =
  | { kind: "repick"; proposal: Proposal }
  | { kind: "move"; proposal: Proposal; target: Mode };

const RenameQueue: Component<{ mode: Mode }> = (props) => {
  const [pageSize, setPageSize] = createSignal(loadPageSize("rename"));
  const [offset, setOffset] = createSignal(0);
  const [logKey, setLogKey] = createSignal(0);
  const [applyProgress, setApplyProgress] = createSignal<string>("");
  const [showHistory, setShowHistory] = createSignal(loadShowHistory("rename"));
  const listView = (): "live" | "history" =>
    showHistory() ? "history" : "live";
  const [page, { refetch }] = createResource(
    () => ({
      mode: props.mode,
      limit: pageSize(),
      offset: offset(),
      view: listView(),
    }),
    ({ mode, limit, offset, view }) => fetchProposals(mode, limit, offset, view),
  );
  const proposals = () => page()?.items ?? [];
  const [takeover, setTakeover] = createSignal<TakeoverState | null>(null);

  // Claude 2026-08-08: scroll save/restore around the full-page takeover.
  // Reason: the takeover removes the table from the DOM, which collapses the
  //   scroll container's height and makes the browser clamp its scrollTop —
  //   nothing restores it on return. The host is AppShell's <main>, NOT the
  //   document: the shell root is `h-screen overflow-hidden` and <main> is the
  //   app's only scroll region (AppShell.tsx:676, :738-739), so window.scrollY
  //   is permanently 0 and window.scrollTo() does nothing.
  // Troubleshooting: returning from Re-pick / Move jumps the list back to the
  //   top instead of where the operator was.
  // Review if: AppShell stops being the single-scroll-region shell, or the
  //   routed screen gains its own overflow-y-auto ancestor.
  // Context: see .omc/plans/autopilot-impl.md §1.3.
  let rootEl!: HTMLDivElement;
  const scrollHost = (): HTMLElement | null => rootEl?.closest("main") ?? null;

  const [savedScrollTop, setSavedScrollTop] = createSignal<number | null>(null);

  // Claude 2026-08-08: both setters wrapped in batch().
  // Reason: defence in depth, applied here after the IDENTICAL unbatched shape
  //   turned out to be a live production bug in Dedup.tsx. Dedup's only entry
  //   point is a <select onChange>, and `change` is NOT in Solid's delegated-
  //   event set, so setSavedScrollTop drove its own synchronous update cycle:
  //   the restore effect below ran in the gap between the two writes, saw
  //   savedScrollTop set with takeover still null and page.loading false,
  //   "restored" against a list that had not gone anywhere, and CLEARED the
  //   saved value — leaving nothing to restore on the way back. Rename's two
  //   call sites (:505, :519) both route through delegated `click` events
  //   today, and `click` IS auto-batched, which is the ONLY reason this
  //   currently works. batch() removes that dependency, so a future entry
  //   point on change/submit/anything non-delegated cannot silently
  //   reintroduce the dead-restore bug.
  // Troubleshooting: returning from Re-pick / Move jumps the list to the top.
  // Review if: the restore effect below stops reading savedScrollTop.
  // Context: .omc/plans/autopilot-impl.md §1.3; Dedup.tsx's twin comment.
  const openTakeover = (t: TakeoverState) => {
    batch(() => {
      setSavedScrollTop(scrollHost()?.scrollTop ?? null);
      setTakeover(t);
    });
  };

  createEffect(() => {
    const y = savedScrollTop();
    if (y === null) return;
    if (takeover() !== null) return; // still in the takeover
    if (page.loading) return; // the refetch is in flight; the table is not back
    if (proposals().length === 0) {
      // nothing to scroll to; drop the saved value
      setSavedScrollTop(null);
      return;
    }
    const host = scrollHost();
    // null host = no <main> ancestor (the isolated screen tests). Clear and
    // move on; this is an expected state, not a failure. The queueMicrotask is
    // required: this effect runs before Solid has flushed the re-inserted rows,
    // so a synchronous assignment would clamp against the pre-insert height.
    if (host) queueMicrotask(() => { host.scrollTop = y; });
    setSavedScrollTop(null);
  });

  // commitRepick / commitMove are parallel sibling adapters, deliberately NOT
  // hoisted into a shared module: they share none of their payload shape (a
  // RepickRequest vs a MoveModeRequest, different endpoints, different required
  // fields) and are ~8 lines each — exactly the case this repo's
  // no-premature-abstraction convention says to leave duplicated.
  // See .omc/plans/autopilot-impl.md §5.1 R9.
  const commitRepick = (p: Proposal) => async (pick: TakeoverPick) => {
    if (pick.kind !== "catalog") throw new Error("repick requires a catalog match");
    // `!= null`, never truthiness — season 0 is Specials (unchanged semantics,
    // now enforced by TakeoverPick's shape: the pair is present or absent).
    await repickProposal(p.id, {
      tmdbId: pick.tmdbId,
      title: pick.title,
      year: pick.year,
      ...(pick.seasonNumber != null && pick.episodeNumber != null
        ? { seasonNumber: pick.seasonNumber, episodeNumber: pick.episodeNumber }
        : {}),
    });
  };

  const commitMove = (p: Proposal, target: Mode) => async (pick: TakeoverPick) => {
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
              ? { seasonNumber: pick.seasonNumber, episodeNumber: pick.episodeNumber }
              : {}),
          },
    );
  };

  const [batchResult, setBatchResult] = createSignal<ApplyBatchResponse | null>(
    null,
  );
  const [applyAllPlan, setApplyAllPlan] = createSignal<ApplyAllPlanItem[] | null>(
    null,
  );
  const [applyAllLoading, setApplyAllLoading] = createSignal(false);
  // Per-row dropdown; match found (pending) seeds Rename.
  const [selections, setSelections] = createSignal<
    Record<number, RowActionId | "">
  >({});

  const isTitleMode = () => props.mode === "movies" || props.mode === "series";

  const selectionOf = (p: Proposal): RowActionId | "" => {
    const cur = selections()[p.id];
    if (cur !== undefined) return cur;
    return defaultRowAction(p.status, isTitleMode());
  };

  const setSelection = (id: number, action: RowActionId | "") => {
    setSelections((prev) => ({ ...prev, [id]: action }));
  };

  // Claude 2026-08-09: track each row's PREVIOUS status, not just its current
  //   one, so the seeding effect below can detect a genuine unmatched ->
  //   pending TRANSITION rather than only re-checking validity in isolation.
  // Reason: rowActionEnabled treats "repick" (and dismiss/delete/move:*) as
  //   valid for BOTH "pending" and "unmatched" BY DESIGN, so an operator can
  //   still re-pick an already-matched row. That means a stale "repick"
  //   selection stays "technically valid" straight through a successful
  //   Re-pick (unmatched -> pending), and the validity check below never
  //   catches it — the dropdown stays stuck on Re-pick and Apply just
  //   re-opens the takeover instead of applying the fresh match. A plain
  //   object, not a signal: nothing else reads this, so it doesn't need to be
  //   reactive.
  // Troubleshooting: after committing a Re-pick match, the row's dropdown
  //   still reads "Re-pick" instead of resetting to "Rename".
  // Review if: a third status value is added that this effect needs to seed
  //   through.
  // Context: .omc/state/sessions/432c4084-7a92-43fb-9208-320d3452ed3c/prd.json US-001.
  let lastKnownStatus: Record<number, string> = {};

  // Seed Rename for newly visible matches; reset a row whose status just
  // transitioned unmatched -> pending; clear invalid choices.
  createEffect(() => {
    const titleMode = isTitleMode();
    const items = proposals();
    const nextStatus: Record<number, string> = {};
    setSelections((prev) => {
      let changed = false;
      const next = { ...prev };
      for (const p of items) {
        nextStatus[p.id] = p.status;
        if (next[p.id] === undefined) {
          next[p.id] = defaultRowAction(p.status, titleMode);
          changed = true;
          continue;
        }
        const transitionedToPending =
          lastKnownStatus[p.id] === "unmatched" && p.status === "pending";
        if (transitionedToPending) {
          // Unconditional reset — deliberately NOT gated on rowActionEnabled,
          // since a stale "repick"/"dismiss"/"delete"/"move:*" selection is
          // exactly what stays "valid" for the new "pending" status and is
          // exactly what this branch exists to catch.
          next[p.id] = defaultRowAction(p.status, titleMode);
          changed = true;
          continue;
        }
        const cur = next[p.id];
        if (cur && !rowActionEnabled(cur, p.status, titleMode)) {
          next[p.id] = defaultRowAction(p.status, titleMode);
          changed = true;
        }
      }
      return changed ? next : prev;
    });
    lastKnownStatus = nextStatus;
  });

  const { actionError, setActionError, scanning, acting, scan, act } =
    useWorkflowActions(() => props.mode, {
      resetOnModeChange: () => {
        // A mode switch abandons the pending scroll restore outright — the
        // list it was measured against is gone. (§5.1 R4.)
        setTakeover(null);
        setSavedScrollTop(null);
        setBatchResult(null);
        setApplyAllPlan(null);
        setSelections({});
        setOffset(0);
        setApplyProgress("");
      },
      scanFn: scanRename,
      resetAfterScan: () => {
        setBatchResult(null);
        setApplyAllPlan(null);
        setSelections({});
        setOffset(0);
        setLogKey((k) => k + 1);
      },
      resetAfterAct: () => {
        setTakeover(null);
        setSavedScrollTop(null);
      },
      refetch,
    },
  );

  const titleOf = (id: number): string => {
    const p = proposals().find((x) => x.id === id);
    return p ? p.title || p.sourceName || "" : "";
  };

  const runRowAction = (p: Proposal, id: RowActionId) => {
    if (id === "rename") {
      void act(() => applyProposal(p.id)).then(() => setLogKey((k) => k + 1));
    } else if (id === "repick") {
      openTakeover({ kind: "repick", proposal: p });
    } else if (id === "delete") {
      // Claude 2026-08-09: explicit delete branch, placed with the move:*
      //   branch BELOW the rename/repick checks and ABOVE the id-specific
      //   dismiss branch — the same explicit-branch-not-catch-all rule the
      //   move:* comment below records.
      // Reason: this branch does NOT delete immediately. It seeds a ONE-ROW
      //   plan and opens the SAME ApplyAllConfirm modal the bulk flow uses,
      //   so a permanent, irreversible file deletion can never be a single
      //   unconfirmed click — note the dismiss branch below fires with ZERO
      //   confirmation, which is fine for a reversible status flip and is not
      //   fine here. It also keeps exactly ONE code path to delete-batch
      //   app-wide (single-row and bulk both funnel through confirmApplyAll),
      //   so there is no second call site that could drift from the first.
      // Troubleshooting: choosing "Delete file" and clicking Apply either
      //   destroying the file with no confirmation, or doing nothing at all.
      // NOTE FOR TESTS/DOCS: the row's clickable control is labelled literally
      //   "Apply" and does NOT relabel itself per selection. The real operator
      //   gesture is "choose 'Delete file' in the row's dropdown, then click
      //   the row's 'Apply' button" — which opens the confirm modal, not an
      //   immediate deletion. Writing "click Delete" describes a control that
      //   does not exist.
      // Review if: an immediate single-row delete is ever explicitly
      //   requested — it would need its own confirmation, not a bare act().
      // Context: .omc/plans/autopilot-impl.md §3.8.
      setApplyAllPlan([{ proposal: p, action: "delete" }]);
    } else if (id.startsWith("move:")) {
      // Claude 2026-08-08: explicit move:* branch BEFORE the dismiss check.
      // Reason: the previous trailing `else` was a catch-all (any unmatched
      //   id silently dismissed), not an "id === dismiss" check — a move:*
      //   id added to the union without this branch would have fallen
      //   through and dismissed the proposal instead of opening the move
      //   takeover. See .omc/plans/autopilot-impl-cross-mode-move.md §6.3
      //   step 5 for the original fix.
      // Troubleshooting: a Move action deleting/dismissing a proposal instead
      //   of opening SearchTakeover.
      // Review if: RowActionId gains another id — it needs its own branch
      //   here too, since the trailing branch below is now id-specific, not
      //   a catch-all. (2026-08-09: that prediction came true — "delete" is
      //   the second instance, and it got its own branch above.)
      openTakeover({
        kind: "move",
        proposal: p,
        target: id.slice("move:".length) as Mode,
      });
    } else if (id === "dismiss") {
      void act(() => dismissProposal(p.id)).then(() => setLogKey((k) => k + 1));
    }
    // Claude 2026-08-09: "preview" deliberately gets NO branch here, answering
    //   the "Review if" note on the move:* branch above directly.
    // Reason: "preview" is never selectable in the row's <select> (it has no
    //   BASE_ROW_ACTIONS entry — see rowActionEnabled's "preview" case), so it
    //   never reaches onRun/runRowAction at all. Falling through every branch
    //   above and doing nothing is therefore the correct outcome here, not a
    //   gap to fill.
    // Review if: this chain is ever converted to an exhaustive switch —
    //   "preview" will then need an explicit no-op case, which is a good
    //   outcome, not a problem.
  };

  const openApplyAll = (): void => {
    setActionError("");
    setApplyAllLoading(true);
    void (async () => {
      try {
        const rows = await fetchAllLiveProposals(props.mode);
        const titleMode = isTitleMode();
        const sel = selections();
        const plan: ApplyAllPlanItem[] = [];
        for (const p of rows) {
          const action = planActionForRow(
            p,
            sel[p.id] ?? defaultRowAction(p.status, titleMode),
            titleMode,
          );
          if (action) plan.push({ proposal: p, action });
        }
        if (plan.length === 0) {
          setActionError(
            "Nothing to apply — set each row’s action (matches default to Rename).",
          );
          return;
        }
        setApplyAllPlan(plan);
      } catch (e) {
        setActionError((e as Error).message);
      } finally {
        setApplyAllLoading(false);
      }
    })();
  };

  const confirmApplyAll = (): void => {
    const plan = applyAllPlan();
    if (!plan || plan.length === 0) return;
    setApplyAllPlan(null);
    setBatchResult(null);
    setApplyProgress("");
    const toApply = plan.filter((x) => x.action === "apply");
    const toDismiss = plan
      .filter((x) => x.action === "dismiss")
      .map((x) => x.proposal.id);
    const toDelete = plan.filter((x) => x.action === "delete");
    void act(async () => {
      const results: ApplyBatchResultItem[] = [];
      const total = toApply.length + toDismiss.length + toDelete.length;
      let done = 0;
      if (toApply.length > 0) {
        const out = (await applyBatchStreaming(
          toApply.map((x) => ({
            id: x.proposal.id,
            // Claude 2026-08-06: send sourcePath for id-miss re-resolve safety net
            // Reason: apply-batch can re-find the live row if id churned.
            // Troubleshooting: Apply all failures with "no proposal with that id".
            // Review if: proposal ids are guaranteed stable and path is redundant.
            sourcePath: x.proposal.sourcePath,
          })),
          (d, t) => {
            setApplyProgress(`Applied ${d}/${t} renames…`);
          },
        )) as ApplyBatchResponse;
        results.push(...out.results);
        done += toApply.length;
      }
      for (const id of toDismiss) {
        try {
          await dismissProposal(id);
          results.push({ id, ok: true });
        } catch (e) {
          results.push({ id, ok: false, error: (e as Error).message });
        }
        done++;
        setApplyProgress(`Processed ${done}/${total}…`);
      }
      // Claude 2026-08-09: delete runs LAST, and as ONE batch call.
      // Reason: (a) ordering — if the operator's session dies mid-flow, the
      //   non-destructive work has already committed and only the
      //   irreversible work is skipped, the strictly better failure mode.
      //   (b) one call, not a per-id loop like dismiss above — the backend
      //   groups every deletion's PathChange by mode and fires ONE
      //   NotifyPlayers per mode; looping here would defeat that grouping.
      // Troubleshooting: N separate player-rescan notifications for one bulk
      //   delete, or deletes committing before renames that then fail.
      // Review if: delete-batch ever gains NDJSON streaming (then it wants
      //   applyBatchStreaming's per-item progress shape, not this).
      // Context: .omc/plans/autopilot-impl.md §1.2 and §3.7.
      if (toDelete.length > 0) {
        try {
          const out = await deleteBatch(
            toDelete.map((x) => ({
              id: x.proposal.id,
              sourcePath: x.proposal.sourcePath,
            })),
          );
          results.push(...out.results);
        } catch (e) {
          // A transport-level failure of the whole call — attribute it to
          // every item so no requested id silently vanishes from the summary
          // (the per-id dismiss loop gets this for free from its own catch).
          for (const x of toDelete) {
            results.push({
              id: x.proposal.id,
              ok: false,
              error: (e as Error).message,
            });
          }
        }
        done += toDelete.length;
        setApplyProgress(`Processed ${done}/${total}…`);
      }
      setBatchResult({ results });
      setApplyProgress("");
      setLogKey((k) => k + 1);
    });
  };

  return (
    <>
      <Show when={!takeover()}>
        <div ref={rootEl}>
          <div class="flex flex-wrap items-center gap-3">
            <Button
              variant="primary"
              onClick={() => void scan(props.mode)}
              disabled={scanning() || acting()}
            >
              {scanning() ? "Scanning…" : "Scan"}
            </Button>
            <Show when={!showHistory()}>
              <Button
                variant="primary"
                onClick={openApplyAll}
                disabled={applyAllLoading() || scanning() || acting()}
              >
                {applyAllLoading()
                  ? "Loading…"
                  : acting()
                    ? "Applying…"
                    : "Apply all"}
              </Button>
            </Show>
            <PageSizeSelect
              value={pageSize()}
              onChange={(n) => {
                savePageSize("rename", n);
                setPageSize(n);
                setOffset(0);
              }}
            />
            <ShowHistoryToggle
              checked={showHistory()}
              onChange={(on) => {
                saveShowHistory("rename", on);
                setShowHistory(on);
                setOffset(0);
              }}
            />
          </div>

          <Show when={applyProgress()}>
            <Muted class="mt-2">{applyProgress()}</Muted>
          </Show>
          <Show when={actionError()}>
            <ErrorText>{actionError()}</ErrorText>
          </Show>
          <Show when={batchResult()}>
            {(res) => <BatchResultSummary result={res()} titleOf={titleOf} />}
          </Show>
          <Show when={page.error}>
            <ErrorText>{(page.error as Error)?.message}</ErrorText>
          </Show>

          <Show
            when={!page.loading}
            fallback={
              <div class="mt-4 rounded-xl border border-border bg-surface/95 p-3 shadow-sm backdrop-blur-md">
                <Muted>Loading…</Muted>
              </div>
            }
          >
            <Show
              when={proposals().length > 0}
              fallback={
                <div class="mt-4 rounded-xl border border-border bg-surface/95 p-3 shadow-sm backdrop-blur-md">
                  <Muted>
                    {showHistory()
                      ? "No history yet."
                      : "No proposals yet — click Scan."}
                  </Muted>
                </div>
              }
            >
              <div class="mt-4 overflow-x-auto rounded-xl border border-border bg-surface/95 p-3 shadow-sm backdrop-blur-md">
                <table class="w-full text-left text-sm">
                  <thead>
                    <tr class="border-b border-border text-xs uppercase tracking-wide text-muted">
                      <th class="px-2 py-2 font-medium">Source</th>
                      <th class="px-2 py-2 font-medium">Title</th>
                      <Show when={props.mode === "movies" || props.mode === "series"}>
                        <th class="px-2 py-2 font-medium">Year</th>
                      </Show>
                      <Show when={props.mode === "series"}>
                        <th class="px-2 py-2 font-medium">Season</th>
                        <th class="px-2 py-2 font-medium">Episode</th>
                      </Show>
                      <Show when={props.mode === "adult"}>
                        <th class="px-2 py-2 font-medium">Studio</th>
                        <th class="px-2 py-2 font-medium">Date</th>
                        <th class="px-2 py-2 font-medium">PHash</th>
                      </Show>
                      <th class="px-2 py-2 font-medium">Root Folder</th>
                      <th class="px-2 py-2 font-medium">Reason</th>
                      <th class="px-2 py-2 font-medium">Actions</th>
                    </tr>
                  </thead>
                  <tbody>
                    <For each={proposals()}>
                      {(p) => (
                        <tr class="border-b border-border/60 align-top">
                          <td class="px-2 py-2 font-mono text-xs">
                            <div class="flex items-center gap-1">
                              <span>{p.sourceName}</span>
                              <Show
                                when={rowActionEnabled(
                                  "preview",
                                  p.status,
                                  isTitleMode(),
                                )}
                              >
                                <SourcePreviewPopout
                                  src={proposalVideoUrl(props.mode, p.id)}
                                  label={p.sourceName}
                                />
                              </Show>
                            </div>
                          </td>
                          <td class="px-2 py-2">
                            <div class="flex flex-wrap items-center gap-1">
                              <span>{p.title}</span>
                              <Show
                                when={(p.reason || "")
                                  .toLowerCase()
                                  .startsWith("web match:")}
                              >
                                <span class="rounded bg-surface-2 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-muted">
                                  web match
                                </span>
                              </Show>
                            </div>
                          </td>
                          <Show when={props.mode === "movies" || props.mode === "series"}>
                            <td class="px-2 py-2">{p.year || ""}</td>
                          </Show>
                          <Show when={props.mode === "series"}>
                            <td class="px-2 py-2">{p.seasonNumber ?? ""}</td>
                            <td class="px-2 py-2">
                              {episodeDisplay(p.episodeNumber, p.extraEpisodeNumbers)}
                            </td>
                          </Show>
                          <Show when={props.mode === "adult"}>
                            <td class="px-2 py-2">{p.studio}</td>
                            <td class="px-2 py-2">{p.date}</td>
                            <td class="px-2 py-2" title={p.phash}>
                              {shortHash(p.phash || "")}
                            </td>
                          </Show>
                          <td class="px-2 py-2 font-mono text-xs">{p.rootFolderPath}</td>
                          <td class="px-2 py-2 text-muted">{p.reason}</td>
                          <td class="px-2 py-2">
                            <RowActions
                              proposal={p}
                              mode={props.mode}
                              titleMode={isTitleMode()}
                              selected={selectionOf(p)}
                              onSelect={(id) => setSelection(p.id, id)}
                              onRun={(id) => runRowAction(p, id)}
                              disabled={acting() || scanning()}
                            />
                          </td>
                        </tr>
                      )}
                    </For>
                  </tbody>
                </table>
              </div>
              <PaginationBar
                total={page()?.total ?? 0}
                limit={pageSize()}
                offset={offset()}
                onPrev={() => setOffset((o) => Math.max(0, o - pageSize()))}
                onNext={() => setOffset((o) => o + pageSize())}
              />
            </Show>
          </Show>

          <Show when={applyAllPlan()}>
            {(plan) => (
              <ApplyAllConfirm
                plan={plan()}
                onCancel={() => setApplyAllPlan(null)}
                onConfirm={confirmApplyAll}
              />
            )}
          </Show>

          <ActivityLogPanel workflow="rename" refreshKey={logKey()} />
        </div>
      </Show>

      {/* ONE mount for BOTH entry points, and it deliberately carries NO
          isTitleMode() guard — this is intentional, not an oversight
          (.omc/plans/autopilot-impl.md §5.1). (a) Re-pick is already gated
          upstream: rowActionEnabled("repick", ...) requires titleMode, so a
          repick takeover is structurally unreachable for Adult rows through
          the UI. (b) Move MUST mount for Adult rows (AC7) — adding a guard
          here would silently break every Adult move. Do not "fix" this.
          ModeTabs stays visible above it (it lives in the parent shell,
          outside RenameQueue); a mode switch closes the takeover through
          resetOnModeChange. */}
      <Show when={takeover()}>
        {(t) => {
          // Bound ONCE so TypeScript's discriminant narrowing carries across
          // the props below; the state only ever changes to null, which
          // unmounts this block, so there is no reactivity lost.
          const st = t();
          const p = st.proposal;
          return (
            <SearchTakeover
              heading={
                st.kind === "repick"
                  ? `Re-pick match for “${p.sourceName}”`
                  : `Move “${p.sourceName}” to another section`
              }
              subheading={
                st.kind === "repick" && p.title ? (
                  <Muted class="mt-1">
                    Currently matched: {p.title}
                    {p.year ? ` (${p.year})` : ""}
                  </Muted>
                ) : undefined
              }
              notes={
                st.kind === "move" ? (
                  <>
                    <Muted>
                      If the target library is on a different filesystem, Apply
                      may fail with a cross-device error. The move itself is
                      reversible.
                    </Muted>
                    <Show when={st.target === "adult"}>
                      <Muted class="mt-1">
                        This file has no perceptual hash yet, so it will be
                        renamed without its [phash-...] tag. A later Adult scan
                        will add it.
                      </Muted>
                    </Show>
                  </>
                ) : undefined
              }
              // CRITICAL: props.mode, NEVER st.target, on BOTH kinds. The
              // adjacent searchMode prop below deliberately uses st.target on
              // a move (searching the DESTINATION catalog) — but the file
              // being previewed still lives at the proposal's OWN mode's
              // storage regardless of which mode it is being moved to. Using
              // st.target here would silently 400 the preview on every Move
              // takeover (prop.Mode != m) with no console error and no
              // visible failure — see .omc/plans/autopilot-impl-rename-preview.md §6.3.
              preview={
                <SourcePreviewDisclosure
                  src={proposalVideoUrl(props.mode, p.id)}
                  label={p.sourceName}
                />
              }
              searchMode={st.kind === "repick" ? props.mode : st.target}
              initialQuery={
                st.kind === "repick" ? p.title || p.sourceName || "" : p.sourceName
              }
              // LOAD-BEARING and deliberately asymmetric: repick searches on
              // mount, move must NOT — a mount-time GET would break the move
              // entry point's "Cancel issues zero requests" guarantee. See
              // SearchTakeover's own autoSearch prop doc.
              autoSearch={st.kind === "repick"}
              currentSlot={
                p.seasonNumber != null && p.episodeNumber != null
                  ? { season: p.seasonNumber, episode: p.episodeNumber }
                  : null
              }
              onCommit={
                st.kind === "repick"
                  ? commitRepick(p)
                  : commitMove(p, st.target)
              }
              // Claude 2026-08-08: refetch() runs FIRST, and both writes are
              //   inside batch().
              // Reason: onDone is invoked from SearchTakeover's commit handler
              //   in a POST-await microtask, which Solid does NOT auto-batch.
              //   refetch() flips page.loading to true synchronously; if
              //   setTakeover(null) ran first, the scroll-restore effect's
              //   `if (page.loading) return` gate would still read false, fire
              //   against the about-to-be-replaced DOM, and be discarded — the
              //   restore would silently fail on EVERY successful commit in
              //   production, not just in tests. batch() removes the
              //   dependence on refetch()'s exact microtask timing by making
              //   both writes observable as one consistent state.
              // Troubleshooting: list jumps to the top after a successful
              //   Re-pick / Move commit.
              // Review if: the scroll-restore effect stops gating on
              //   page.loading. Do NOT reorder these two calls.
              // Context: .omc/plans/autopilot-impl.md §1.3 and §5.1 R5b.
              onDone={() => {
                setActionError("");
                batch(() => {
                  void refetch();
                  setTakeover(null);
                });
              }}
              // onCancel needs no batch/reorder: no refetch happens, so the
              // restore correctly fires straight away against the still-loaded
              // list.
              onCancel={() => setTakeover(null)}
            />
          );
        }}
      </Show>
    </>
  );
};

export const Rename: Component = () => {
  const [mode, setMode] = createSignal<Mode>("movies");
  return (
    <div>
      <ModeTabs current={mode} onSelect={setMode} />
      <RenameQueue mode={mode()} />
    </div>
  );
};
