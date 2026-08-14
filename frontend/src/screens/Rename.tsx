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
  createUniqueId,
  For,
  Show,
} from "solid-js";
import type { Mode } from "../api/discover";
import type { AdultOrganizeAspect } from "../api/organize";
import type { ApplyBatchResponse, ApplyBatchResultItem } from "@dto";
import { ApiError } from "../api/client";
import {
  type AdultReviewConfirmRequest,
  type Proposal,
  type RecentlyAppliedEntry,
  type UndoResult,
  applyBatchStreaming,
  applyProposal,
  confirmAdultReview,
  deleteBatch,
  dismissProposal,
  fetchAdultReview,
  fetchProposals,
  fetchRecentlyApplied,
  moveProposalMode,
  repickProposal,
  scanRename,
  undoProposal,
} from "../api/rename";
import {
  loadPageSize,
  loadShowHistory,
  proposalVideoUrl,
  savePageSize,
  saveShowHistory,
} from "../api/organize";
import { fetchNamingPreset } from "../api/settings";
import {
  type NamingPreset,
  proposedFileName,
} from "../naming";
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
  AdultOrganizeAspectBar,
  loadAdultOrganizeAspect,
  PageSizeSelect,
  PaginationBar,
  ShowHistoryToggle,
} from "./OrganizeChrome";

type RowActionId =
  | "rename"
  | "review"
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
  // Claude 2026-08-12: "Review" is second — after Rename, before Search.
  // Reason: Review is the constructive path for a web-identified Adult row with
  //   no catalog scene id. Positioning matters: it must not sit adjacent to the
  //   destructive Delete entry (same reasoning as the "Delete file" comment
  //   below). Gating is structural (isAdultWebIdentified), not status-based, so
  //   rowActionEnabled always returns false for it — the RowActions component's
  //   local `enabled` function consults isAdultWebIdentified directly.
  // Review if: Review becomes batchable (it is currently excluded from
  //   planActionForRow and must never be auto-selected as the default).
  // Context: autopilot-impl-adult-rename-review-alts.md §6 F1.
  { id: "review", label: "Review" },
  { id: "repick", label: "Search" },
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
  _titleMode: boolean,
): boolean {
  switch (id) {
    case "rename":
      return status === "pending";
    case "review":
      // Structural gating is done by isAdultWebIdentified (needs the whole
      // proposal and mode, not just status). rowActionEnabled always returns
      // false here so the selection-seeding effect never auto-selects Review
      // and the dropdown's generic `enabled()` call disables it. The
      // RowActions component's local `enabled` function overrides this for
      // the "review" id specifically, consulting isAdultWebIdentified.
      return false;
    case "repick":
      return status === "pending" || status === "unmatched";
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

// Claude 2026-08-12: structural predicate for the Review action gate.
// Reason: Review is Adult-only, unmatched-only, requires a web-identified title,
//   and must not fire for a row that already has a catalog scene id. This cannot
//   be expressed in rowActionEnabled's (id, status, titleMode) signature without
//   widening it to take the full proposal (8+ call sites). A sibling predicate
//   that is consulted in the three places that build/validate the dropdown is the
//   lowest-footprint correct approach.
// Troubleshooting: Review appearing for a pending row, or for an unmatched row
//   with no title, or for a row that already has a giveBackSceneId.
// Review if: the predicate gains a fourth condition — update the test matrix in
//   Rename.review.test.tsx and the inline table in the plan §6 F2.
// Context: autopilot-impl-adult-rename-review-alts.md §6 F2.
export function isAdultWebIdentified(p: Proposal, mode: Mode): boolean {
  if (mode !== "adult" || p.status !== "unmatched" || p.giveBackSceneId) {
    return false;
  }
  const reason = (p.reason || "").toLowerCase();
  if (reason.includes("web-identified")) return true;
  return !!p.title;
}

/** Pending alternate fold — "already in library" rows use Rename, not Review. */
export function isAdultAlternateInLibrary(p: Proposal): boolean {
  return (p.reason || "").toLowerCase().startsWith("alternate:");
}

/** Match found → Rename; web-identified unmatched → Review. */
function defaultRowAction(
  status: string,
  titleMode: boolean,
  mode?: Mode,
  proposal?: Proposal,
): RowActionId | "" {
  if (mode === "adult" && proposal && isAdultWebIdentified(proposal, mode)) {
    return "review";
  }
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
  mode?: Mode,
): "apply" | "dismiss" | "delete" | null {
  const action =
    dropdown || defaultRowAction(p.status, titleMode, mode, p);
  // Review is per-row and non-batchable by design (same as repick/move:*): it
  // opens a modal rather than committing, and Apply-All must never silently skip
  // or batch it. Return null so openApplyAll excludes it from the plan.
  if (action === "review") return null;
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
  // Review is Adult-only — hide the greyed-out option on Movies/Series rows.
  const actions = () =>
    props.mode === "adult"
      ? rowActions(props.mode)
      : rowActions(props.mode).filter((a) => a.id !== "review");
  // "review" is structurally gated by isAdultWebIdentified — rowActionEnabled
  // always returns false for it (see its "review" case above). Override here
  // so the dropdown option is correctly enabled/disabled for the specific row.
  const enabled = (id: RowActionId) =>
    id === "review"
      ? isAdultWebIdentified(props.proposal, props.mode)
      : rowActionEnabled(id, props.proposal.status, props.titleMode);
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
        defaultRowAction(
          props.proposal.status,
          props.titleMode,
          props.mode,
          props.proposal,
        ),
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
          found; searching and Review are skipped — each needs a manual pick).
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

// ---- Rename Undo: the "Recently Applied" section ---------------------------
//
// Its OWN section below the proposal table, deliberately NOT another entry in
// the per-row Actions dropdown (spec Round 6): undo acts on already-Applied
// items, which have left the live proposal list entirely, so there is no row to
// hang it off. It is also NOT the `Show history` view — that lists every
// Applied/Dismissed proposal; this is only the bounded, still-undoable set,
// capped at the mode's configured undo depth (Settings → Library).
//
// UndoOutcome renders a 200 response. A 200 does NOT mean "fully restored":
// undo is best-effort by design, so a drifted or not-moved-back file has to
// read visibly differently from a clean success, or the operator is told
// "done" about a file that never moved.
const UndoOutcome: Component<{ result: UndoResult }> = (props) => {
  const degraded = () =>
    !props.result.fileRestored || props.result.driftDetected;
  return (
    <>
      <Show
        when={degraded()}
        fallback={
          <Muted class="mt-2">Undo complete — {props.result.fileMessage}</Muted>
        }
      >
        <div class="mt-2 rounded-md border border-warn/40 bg-warn/10 px-3 py-2 text-sm text-warn">
          <div>Undo completed with warnings — {props.result.fileMessage}</div>
          <For each={props.result.driftWarnings ?? []}>
            {(wmsg) => <div class="mt-1">{wmsg}</div>}
          </For>
        </div>
      </Show>
      {/* Adult-only, and deliberately Muted rather than a warning: an
          already-sent community-database submission is an expected,
          irreversible side effect of the Apply, not a failure of the undo. */}
      <Show when={props.result.giveBackNotRetractable}>
        <Muted class="mt-1">
          This Apply submitted a fingerprint or draft to a community database —
          that submission cannot be retracted.
        </Muted>
      </Show>
    </>
  );
};

// ProposalFileNames shows the on-disk basename today and the pending Apply
// target basename — the two fields operators need before confirming a rename.
const ProposalFileNames: Component<{
  proposal: Proposal;
  mode: Mode;
  preset: NamingPreset;
  titleMode: boolean;
}> = (props) => {
  const p = () => props.proposal;
  const proposed = () => proposedFileName(props.mode, props.preset, p());
  return (
    <dl class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
      <dt class="text-muted">Current name</dt>
      <dd class="break-all font-mono">
        <div class="flex items-start gap-1">
          <span class="min-w-0 flex-1">{p().sourceName}</span>
          <Show
            when={rowActionEnabled(
              "preview",
              p().status,
              props.titleMode,
            )}
          >
            <SourcePreviewPopout
              src={proposalVideoUrl(props.mode, p().id)}
              label={p().sourceName}
            />
          </Show>
        </div>
      </dd>
      <Show when={p().status === "pending"}>
        <dt class="text-muted">Proposed name</dt>
        <dd class="break-all font-mono">{proposed() || "—"}</dd>
      </Show>
    </dl>
  );
};

// TitleProposalCard is the stacked card layout for Movies/Series Rename
// proposals — same treatment as AdultProposalCard and Recently Applied.
const TitleProposalCard: Component<{
  proposal: Proposal;
  mode: Mode;
  preset: NamingPreset;
  titleMode: boolean;
  selected: RowActionId | "";
  onSelect: (id: RowActionId | "") => void;
  onRun: (id: RowActionId) => void;
  disabled: boolean;
}> = (props) => {
  const p = () => props.proposal;
  return (
    <li
      data-proposal-row
      class="rounded-lg border border-border/60 p-3"
    >
      <ProposalFileNames
        proposal={p()}
        mode={props.mode}
        preset={props.preset}
        titleMode={props.titleMode}
      />
      <div class="mt-2 flex flex-wrap items-center gap-1 text-sm">
        <span class="font-medium text-fg">{p().title}</span>
        <Show
          when={(p().reason || "")
            .toLowerCase()
            .startsWith("web match:")}
        >
          <span class="rounded bg-surface-2 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-muted">
            web match
          </span>
        </Show>
      </div>
      <dl class="mt-2 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
        <dt class="text-muted">Year</dt>
        <dd>{p().year || ""}</dd>
        <Show when={props.mode === "series"}>
          <dt class="text-muted">Season</dt>
          <dd>{p().seasonNumber ?? ""}</dd>
          <dt class="text-muted">Episode</dt>
          <dd>{episodeDisplay(p().episodeNumber, p().extraEpisodeNumbers)}</dd>
        </Show>
        <dt class="text-muted">Root</dt>
        <dd class="break-all font-mono">{p().rootFolderPath}</dd>
      </dl>
      <Show when={p().reason}>
        <div class="mt-2 text-xs text-muted">{p().reason}</div>
      </Show>
      <div class="mt-3">
        <RowActions
          proposal={p()}
          mode={props.mode}
          titleMode={props.titleMode}
          selected={props.selected}
          onSelect={props.onSelect}
          onRun={props.onRun}
          disabled={props.disabled}
        />
      </div>
    </li>
  );
};

// AdultProposalCard is the mobile-friendly row layout for Adult Rename
// proposals — the eight-column table (Source/Title/Studio/Date/PHash/Root/
// Reason/Actions) collapses on narrow viewports the same way Recently Applied
// did before its card layout fix.
const AdultProposalCard: Component<{
  proposal: Proposal;
  mode: Mode;
  preset: NamingPreset;
  titleMode: boolean;
  selected: RowActionId | "";
  onSelect: (id: RowActionId | "") => void;
  onRun: (id: RowActionId) => void;
  disabled: boolean;
}> = (props) => {
  const p = () => props.proposal;
  return (
    <li
      data-proposal-row
      class="rounded-lg border border-border/60 p-3"
    >
      <ProposalFileNames
        proposal={p()}
        mode={props.mode}
        preset={props.preset}
        titleMode={props.titleMode}
      />
      <div class="mt-2 flex flex-wrap items-center gap-1 text-sm">
        <span class="font-medium text-fg">{p().title}</span>
        <Show when={isAdultWebIdentified(p(), props.mode)}>
          <span class="rounded bg-surface-2 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-muted">
            web identified
          </span>
        </Show>
        <Show when={isAdultAlternateInLibrary(p())}>
          <span class="rounded bg-surface-2 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-muted">
            already in library
          </span>
        </Show>
      </div>
      <dl class="mt-2 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
        <dt class="text-muted">Studio</dt>
        <dd>{p().studio}</dd>
        <dt class="text-muted">Date</dt>
        <dd>{p().date}</dd>
        <dt class="text-muted">PHash</dt>
        <dd class="font-mono" title={p().phash}>
          {shortHash(p().phash || "")}
        </dd>
        <dt class="text-muted">Root</dt>
        <dd class="break-all font-mono">{p().rootFolderPath}</dd>
      </dl>
      <Show when={p().reason}>
        <div class="mt-2 text-xs text-muted">{p().reason}</div>
      </Show>
      <div class="mt-3">
        <RowActions
          proposal={p()}
          mode={props.mode}
          titleMode={props.titleMode}
          selected={props.selected}
          onSelect={props.onSelect}
          onRun={props.onRun}
          disabled={props.disabled}
        />
      </div>
    </li>
  );
};

// formatAppliedAt turns backend timestamps (RFC3339 nano or legacy
// "YYYY-MM-DD HH:MM:SS") into a short, locale-stable label for the UI.
export function formatAppliedAt(raw: string): string {
  if (!raw) return "";
  const normalized = raw.includes("T") ? raw : raw.replace(" ", "T");
  const d = new Date(normalized);
  if (Number.isNaN(d.getTime())) return raw;
  return d.toLocaleString("en-US", {
    month: "short",
    day: "numeric",
    year: "numeric",
    hour: "numeric",
    minute: "2-digit",
    hour12: true,
  });
}

const RecentlyAppliedSection: Component<{
  entries: RecentlyAppliedEntry[];
  loadError: string;
  busyId: number | null;
  disabled: boolean;
  result: UndoResult | null;
  error: string;
  onUndo: (entry: RecentlyAppliedEntry) => void;
}> = (props) => (
  <div class="mt-4 rounded-xl border border-border bg-surface/95 p-3 shadow-sm backdrop-blur-md">
    <div class="text-xs font-medium uppercase tracking-wide text-muted">
      Recently applied
    </div>
    {/* A failed LIST fetch is reported here, scoped to this section — the
        proposal table above must keep working. An empty list is NOT an error
        (the endpoint answers 200 [] for "undo unavailable" and "nothing
        undoable"), so this only ever fires on a real 500 / transport failure. */}
    <Show when={props.loadError}>
      <ErrorText>Could not load recently applied: {props.loadError}</ErrorText>
    </Show>
    <Show
      when={props.entries.length > 0}
      fallback={<Muted class="mt-2">Nothing to undo yet.</Muted>}
    >
      <ul class="mt-2 space-y-3">
        <For each={props.entries}>
          {(e) => {
            const noteId = createUniqueId();
            return (
              <li class="rounded-lg border border-border/60 p-3">
                <div class="break-all font-mono text-xs text-muted">
                  {e.sourceName}
                </div>
                <div class="mt-1 flex flex-wrap items-center gap-1 text-sm">
                  <span class="font-medium text-fg">{e.title}</span>
                  <Show when={e.viaAlternateFold}>
                    <span
                      id={noteId}
                      class="rounded bg-surface-2 px-1.5 py-0.5 text-[10px] font-medium text-muted"
                    >
                      quality alternate — file won’t be moved back on undo
                    </span>
                  </Show>
                </div>
                <div class="mt-2 flex flex-wrap items-center justify-between gap-2">
                  <span class="text-xs text-muted">
                    Applied {formatAppliedAt(e.appliedAt)}
                  </span>
                  <Button
                    onClick={() => props.onUndo(e)}
                    disabled={props.disabled || props.busyId !== null}
                    aria-label={`Undo ${e.sourceName}`}
                    aria-describedby={e.viaAlternateFold ? noteId : undefined}
                  >
                    {props.busyId === e.proposalId ? "Undoing…" : "Undo"}
                  </Button>
                </div>
              </li>
            );
          }}
        </For>
      </ul>
    </Show>
    <Show when={props.result}>{(r) => <UndoOutcome result={r()} />}</Show>
    {/* Surfaced VERBATIM. internal/api/rename_undo.go writes these as
        complete, operator-readable sentences (which live proposal collides,
        what to do about it) — rewrapping them would throw that away. */}
    <Show when={props.error}>
      <ErrorText>{props.error}</ErrorText>
    </Show>
  </div>
);

// ReviewDialog — Adult Review modal.
//
// Claude 2026-08-12: modal, NOT SearchTakeover.
// Reason: SearchTakeover is a full-page takeover built around a catalog
//   search-and-pick flow with scroll-save/restore plumbing; Review is a
//   two-field confirm with no search results list. Reusing it would drag in
//   openTakeover, the scroll-restore effect, and the TakeoverPick discriminated
//   union for no benefit.
// Troubleshooting: dialog not focused / scroll position lost — Review is an
//   overlay, not a takeover; the list stays mounted underneath it.
// Context: autopilot-impl-adult-rename-review-alts.md §6 F4.
const ReviewDialog: Component<{
  proposal: Proposal;
  mode: Mode;
  onDone: () => void;
  onCancel: () => void;
}> = (props) => {
  const p = props.proposal;
  const [preview, { }] = createResource(
    () => ({ mode: props.mode, id: p.id }),
    ({ mode, id }) => fetchAdultReview(mode, id),
  );

  const [fileName, setFileName] = createSignal("");
  const [confirming, setConfirming] = createSignal(false);
  const [confirmError, setConfirmError] = createSignal("");
  // Claude 2026-08-12: seed proposed name once; do not re-seed from !fileName().
  // Reason: clearing the input made fileName() empty, so the effect snapped the
  //   field back to proposedName and blocked intentional empties / edits.
  // Troubleshooting: Confirm stayed enabled after operator cleared the name.
  // Review if: ReviewDialog remounts per open (then a flag is enough forever).
  const [nameSeeded, setNameSeeded] = createSignal(false);

  createEffect(() => {
    if (nameSeeded()) return;
    const data = preview();
    if (!data) return;
    setFileName(data.proposedName);
    setNameSeeded(true);
  });

  const isCatalogMatch = () => {
    const data = preview();
    return !!(data?.catalogBox && data.catalogSceneId);
  };

  const canConfirm = () => {
    if (confirming()) return false;
    if (!preview()) return false;
    // Can't confirm if no phash — Confirm will fail server-side anyway, and
    // the no-phash warning already tells the operator to use Cancel.
    if (!preview()?.phash) return false;
    // Catalog branch: no fileName needed (it is ignored).
    if (isCatalogMatch()) return true;
    // Local branch: fileName must be non-empty.
    return fileName().trim().length > 0;
  };

  const handleConfirm = () => {
    const data = preview();
    if (!data) return;
    setConfirming(true);
    setConfirmError("");
    let body: AdultReviewConfirmRequest;
    if (isCatalogMatch()) {
      body = {
        fileName: fileName(),
        box: data.catalogBox,
        sceneId: data.catalogSceneId,
        title: data.catalogTitle || data.title,
        studio: data.catalogStudio || data.studio,
        date: data.catalogDate || data.date,
      };
    } else {
      body = { fileName: fileName() };
    }
    void confirmAdultReview(props.mode, p.id, body)
      .then(() => {
        props.onDone();
      })
      .catch((e: Error) => {
        setConfirmError(e.message);
        setConfirming(false);
      });
  };

  return (
    <div
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
      role="dialog"
      aria-modal="true"
      aria-label={`Review ${p.sourceName}`}
    >
      <div class="max-h-[80vh] w-full max-w-xl overflow-hidden rounded-xl border border-border bg-surface shadow-lg">
        <div class="border-b border-border px-4 py-3">
          <h3 class="text-base font-semibold text-fg">
            Review &ldquo;{p.sourceName}&rdquo;
          </h3>
        </div>
        <div class="max-h-[55vh] overflow-y-auto px-4 py-3">
          <Show when={preview.loading}>
            <Muted>Loading preview…</Muted>
          </Show>
          <Show when={preview.error}>
            <ErrorText>
              Could not load preview: {(preview.error as Error)?.message}
            </ErrorText>
          </Show>
          <Show when={preview()}>
            {(data) => (
              <>
                <div class="mb-3">
                  <div class="mb-1 text-xs font-medium uppercase tracking-wide text-muted">
                    Current name
                  </div>
                  <div class="rounded border border-border bg-bg px-2 py-1 font-mono text-sm text-fg">
                    {p.sourceName}
                  </div>
                </div>

                <Show when={isCatalogMatch()}>
                  <div class="mb-3 rounded-md border border-accent/40 bg-accent/10 px-3 py-2 text-sm">
                    <div class="font-medium text-fg">
                      Catalog match found — {data().catalogTitle || data().title}
                    </div>
                    <div class="mt-1 text-muted">
                      This scene was found in the catalog (
                      {data().catalogBox}/{data().catalogSceneId}). Confirm will
                      apply the catalog identity. The proposed name below will be
                      ignored — the catalog name is computed automatically.
                    </div>
                  </div>
                </Show>

                <div class="mb-3">
                  <label class="mb-1 block text-xs font-medium uppercase tracking-wide text-muted">
                    Proposed name
                  </label>
                  <input
                    class="w-full rounded border border-border bg-bg px-2 py-1 font-mono text-sm text-fg disabled:opacity-50"
                    type="text"
                    aria-label="Proposed name"
                    value={fileName()}
                    disabled={isCatalogMatch()}
                    onInput={(e) => setFileName(e.currentTarget.value)}
                  />
                </div>

                <Show when={!data().phash}>
                  <div class="mb-3 rounded-md border border-danger/40 bg-danger/10 px-3 py-2 text-sm text-danger">
                    No perceptual hash available — Confirm will fail. Use Cancel
                    and let a Scan with a hashing pass run first.
                  </div>
                </Show>

                <Show when={data().recheckError}>
                  <Muted class="mb-3 block">
                    Catalog recheck: {data().recheckError}
                  </Muted>
                </Show>

                <SourcePreviewDisclosure
                  src={proposalVideoUrl(props.mode, p.id)}
                  label={p.sourceName}
                />
              </>
            )}
          </Show>
          <Show when={confirmError()}>
            <div class="mt-2">
              <ErrorText>{confirmError()}</ErrorText>
            </div>
          </Show>
        </div>
        <div class="flex justify-end gap-2 border-t border-border px-4 py-3">
          <Button variant="secondary" onClick={props.onCancel}>
            Cancel
          </Button>
          <Button
            variant="primary"
            disabled={!canConfirm()}
            onClick={handleConfirm}
          >
            {confirming() ? "Confirming…" : "Confirm"}
          </Button>
        </div>
      </div>
    </div>
  );
};

// TakeoverState replaces the two former `repickFor` / `moveFor` signals with
// ONE discriminated value, because the two entry points now render the SAME
// component (SearchTakeover) in the SAME slot — two independent signals could
// both be non-null at once and would race for that one slot.
// See .omc/plans/autopilot-impl.md §5.1 R2.
type TakeoverState =
  | { kind: "repick"; proposal: Proposal }
  | { kind: "move"; proposal: Proposal; target: Mode };

const RenameQueue: Component<{ mode: Mode; adultAspect: AdultOrganizeAspect }> = (props) => {
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
  const [namingPreset] = createResource(
    () => props.mode,
    (mode) => fetchNamingPreset(mode).catch(() => "jellyfin" as NamingPreset),
  );
  const preset = (): NamingPreset =>
    namingPreset() === "legacy" ? "legacy" : "jellyfin";
  const proposals = () => page()?.items ?? [];

  // Rename Undo's "Recently Applied" list. Its own resource keyed on the mode
  // signal, so a mode switch refetches it alongside the proposals list with no
  // manual wiring in resetOnModeChange (adding one there would double-fetch).
  // This is NOT the `view=history` list above — that shows every
  // Applied/Dismissed proposal; this is only the bounded, still-undoable set.
  const [recent, { refetch: refetchRecent }] = createResource(
    () => props.mode,
    fetchRecentlyApplied,
  );
  // Claude 2026-08-10: the `recent.error` guard is NOT redundant with `?? []`.
  // Reason: a Solid resource RE-THROWS on read once its fetcher has errored (by
  //   design, for ErrorBoundary integration) — `?? []` only handles undefined,
  //   so reading recent() after a 500 would throw mid-render and take the whole
  //   Rename screen (proposal table included) down with it. This is the exact
  //   shipped-bug shape CLAUDE.md records for GrabDialog's sibling <Show>
  //   blocks. The handler answers 200 [] for "undo unavailable" and "nothing
  //   undoable", so this fires only on a genuine 500 or transport failure — and
  //   that is precisely when the rest of the screen must survive.
  // Troubleshooting: the whole Rename screen blanking when
  //   /rename/recently-applied fails.
  // Review if: the section moves under its own ErrorBoundary.
  const recentEntries = (): RecentlyAppliedEntry[] =>
    recent.error ? [] : (recent() ?? []);

  const [takeover, setTakeover] = createSignal<TakeoverState | null>(null);
  const [reviewFor, setReviewFor] = createSignal<Proposal | null>(null);

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
    if (pick.kind === "adult") {
      await repickProposal(p.id, {
        title: pick.title,
        box: pick.box,
        sceneId: pick.sceneId,
        studio: pick.studio,
        date: pick.date,
      });
      return;
    }
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
    return defaultRowAction(p.status, isTitleMode(), props.mode, p);
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
    const mode = props.mode;
    const items = proposals();
    const nextStatus: Record<number, string> = {};
    setSelections((prev) => {
      let changed = false;
      const next = { ...prev };
      for (const p of items) {
        nextStatus[p.id] = p.status;
        if (next[p.id] === undefined) {
          next[p.id] = defaultRowAction(p.status, titleMode, mode, p);
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
          next[p.id] = defaultRowAction(p.status, titleMode, mode, p);
          changed = true;
          continue;
        }
        const cur = next[p.id];
        // "review" uses isAdultWebIdentified, not rowActionEnabled — check it
        // separately so a review selection on a row that is no longer eligible
        // (e.g. it gained a giveBackSceneId from a re-scan) gets cleared.
        const curEnabled =
          cur === "review"
            ? isAdultWebIdentified(p, mode)
            : cur
              ? rowActionEnabled(cur, p.status, titleMode)
              : false;
        if (cur && !curEnabled) {
          next[p.id] = defaultRowAction(p.status, titleMode, mode, p);
          changed = true;
        }
      }
      return changed ? next : prev;
    });
    lastKnownStatus = nextStatus;
  });

  const [undoBusyId, setUndoBusyId] = createSignal<number | null>(null);
  const [undoResult, setUndoResult] = createSignal<UndoResult | null>(null);
  const [undoError, setUndoError] = createSignal("");

  // Claude 2026-08-10: the hook's refetch refreshes BOTH lists.
  // Reason: an Apply is the primary way an entry ENTERS "Recently Applied", so
  //   a list that only refreshed on mode change or after an Undo would read
  //   stale during the exact workflow it exists to serve. act() is the single
  //   chokepoint for BOTH the single-row Apply (runRowAction's
  //   act(() => applyProposal(...))) and the batch path (confirmApplyAll, which
  //   wraps apply-batch + dismiss + delete in one act()), so wiring it here
  //   covers both — wiring only the single-row call site would pass casual
  //   manual testing and still be wrong for "Apply Selected".
  // Troubleshooting: applying a proposal, then finding no Undo button for it
  //   until the mode is switched away and back.
  // Review if: act() stops being the one post-Apply refresh path. Note the
  //   OTHER refetch() call site (SearchTakeover's onDone) deliberately does NOT
  //   use this — Re-pick/Move create no undo entries, and that call sits inside
  //   a batch() with a load-bearing ordering comment.
  //
  // Claude 2026-08-10: the `.catch()` below is a guard, not a live code path.
  // Reason: Solid's resource refetch() never rejects — load() resolves a
  //   fetcher error through p.then(_, e => loadEnd(...)), and loadEnd records
  //   it on the resource (recent.error) instead of rethrowing, so the promise
  //   always fulfils. Kept because it costs nothing and a rejection reaching
  //   act() would banner an ACTION error above a batch that succeeded. (This
  //   is distinct from recentEntries()'s recent.error guard above, which
  //   protects reading recent() — read() DOES rethrow on an errored resource;
  //   only refetch()'s own returned promise does not.)
  // Review if: Solid changes refetch()'s error propagation.
  const refetchRecentQuietly = (): Promise<unknown> =>
    Promise.resolve(refetchRecent()).catch(() => undefined);

  const refetchBoth = async (): Promise<void> => {
    // Started first, awaited last: the MAIN list is what determines whether
    // act() reports success, so it is the one awaited directly.
    const recentDone = refetchRecentQuietly();
    await refetch();
    await recentDone;
  };

  const { actionError, setActionError, scanning, acting, scan, act } =
    useWorkflowActions(() => props.mode, {
      resetOnModeChange: () => {
        // A mode switch abandons the pending scroll restore outright — the
        // list it was measured against is gone. (§5.1 R4.)
        setTakeover(null);
        setReviewFor(null);
        setSavedScrollTop(null);
        setBatchResult(null);
        setApplyAllPlan(null);
        setSelections({});
        setOffset(0);
        setApplyProgress("");
        // The undo feedback belongs to the mode it was produced in. The
        // recently-applied RESOURCE needs no reset here — its source signal is
        // props.mode, so it refetches on its own (a manual refetch would just
        // double-fetch on every mode switch).
        setUndoResult(null);
        setUndoError("");
      },
      scanFn: (m) =>
        scanRename(m, m === "adult" ? props.adultAspect : undefined),
      resetAfterScan: () => {
        setBatchResult(null);
        setApplyAllPlan(null);
        setSelections({});
        setOffset(0);
        setLogKey((k) => k + 1);
      },
      resetAfterAct: () => {
        setTakeover(null);
        setReviewFor(null);
        setSavedScrollTop(null);
      },
      refetch: refetchBoth,
    },
  );

  const titleOf = (id: number): string => {
    const p = proposals().find((x) => x.id === id);
    return p ? p.title || p.sourceName || "" : "";
  };

  const runRowAction = (p: Proposal, id: RowActionId) => {
    if (id === "rename") {
      void act(() => applyProposal(p.id)).then(() => setLogKey((k) => k + 1));
    } else if (id === "review") {
      // Opens the ReviewDialog modal. Not batchable (planActionForRow returns
      // null for "review"), so this branch fires only from a single row's Apply
      // button. No scroll-save/restore needed — the modal is an overlay, not a
      // full-page takeover, so the list stays mounted underneath it.
      setReviewFor(p);
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

  // Claude 2026-08-10: the 400/404 vs 409 branch below is load-bearing.
  // Reason: ListActive filters on mode + consumed_at/evicted_at only — it does
  //   NOT check that the underlying proposal is still Applied, and the archive
  //   row has no FK to it. So a row can sit here looking undoable while a
  //   cross-mode Move, a Purge, or anything else that retires the proposal has
  //   moved on underneath it; the mutation handler then answers 400 (no longer
  //   Applied) or 404 (proposal or entry gone). Those rows are genuinely dead,
  //   so refetching (which drops them) is correct. A 409 is the opposite: a
  //   live proposal now occupies the same source path, the entry is STILL
  //   undoable, and the operator resolves that proposal and retries — dropping
  //   the row there would hide a valid, still-actionable retry path.
  // Troubleshooting: a Recently Applied row vanishing after a collision error,
  //   or a permanently dead row that no click can clear.
  // Review if: the list endpoint starts joining against proposal status (then
  //   the 400/404 refetch becomes redundant, not wrong).
  // Claude 2026-08-10: every write below the await is gated on stillHere().
  // Reason: resetOnModeChange clears undoResult/undoError, but this IIFE can
  //   resolve AFTER a mode switch has already happened — and then it would
  //   write Movies' "Undo complete — moved back to /inbox/X.mkv" into Series'
  //   Recently Applied section, and `await refetch()` would refresh the NEW
  //   mode's proposals as though the undo belonged to it. props.mode is
  //   captured at CALL time, not read after the await, which is the whole
  //   point — reading it afterwards would compare the new value with itself.
  //   Bailing skips the refetches too, deliberately: switching modes already
  //   refetches both of the new mode's lists through their own source signals,
  //   and switching back refetches again, so nothing goes stale.
  // Troubleshooting: an undo banner from the previous mode appearing under the
  //   new mode's list after switching mid-undo.
  // Review if: the undo feedback ever moves to a mode-keyed store instead of
  //   these two plain signals.
  const runUndo = (entry: RecentlyAppliedEntry): void => {
    const startedIn = props.mode;
    const stillHere = () => props.mode === startedIn;
    setUndoBusyId(entry.proposalId);
    setUndoError("");
    setUndoResult(null);
    void (async () => {
      try {
        const res = await undoProposal(entry.proposalId);
        if (!stillHere()) return;
        setUndoResult(res);
        // BOTH lists: the entry is consumed and gone from this one, and the
        // proposal is back in Pending and should reappear in the main table.
        void refetchRecentQuietly();
        await refetch();
        setLogKey((k) => k + 1);
      } catch (e) {
        if (!stillHere()) return;
        setUndoError((e as Error).message);
        const status = e instanceof ApiError ? e.status : 0;
        if (status === 400 || status === 404) void refetchRecentQuietly();
      } finally {
        // Unconditional: the busy id is per-click UI state, not mode-scoped
        // feedback, and leaving it set would disable every Undo button on the
        // mode the operator switched to.
        setUndoBusyId(null);
      }
    })();
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
            sel[p.id] ?? defaultRowAction(p.status, titleMode, props.mode, p),
            titleMode,
            props.mode,
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
              <div class="mt-4 rounded-xl border border-border bg-surface/95 p-3 shadow-sm backdrop-blur-md">
                <ul class="space-y-3">
                  <For each={proposals()}>
                    {(p) => (
                      <Show
                        when={props.mode === "adult"}
                        fallback={
                          <TitleProposalCard
                            proposal={p}
                            mode={props.mode}
                            preset={preset()}
                            titleMode={isTitleMode()}
                            selected={selectionOf(p)}
                            onSelect={(id) => setSelection(p.id, id)}
                            onRun={(id) => runRowAction(p, id)}
                            disabled={acting() || scanning()}
                          />
                        }
                      >
                        <AdultProposalCard
                          proposal={p}
                          mode={props.mode}
                          preset={preset()}
                          titleMode={isTitleMode()}
                          selected={selectionOf(p)}
                          onSelect={(id) => setSelection(p.id, id)}
                          onRun={(id) => runRowAction(p, id)}
                          disabled={acting() || scanning()}
                        />
                      </Show>
                    )}
                  </For>
                </ul>
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

          <Show when={reviewFor()}>
            {(rp) => (
              <ReviewDialog
                proposal={rp()}
                mode={props.mode}
                onDone={() => {
                  batch(() => {
                    void refetch();
                    setReviewFor(null);
                  });
                  void refetchRecentQuietly();
                  setLogKey((k) => k + 1);
                }}
                onCancel={() => setReviewFor(null)}
              />
            )}
          </Show>

          <RecentlyAppliedSection
            entries={recentEntries()}
            loadError={(recent.error as Error | undefined)?.message ?? ""}
            busyId={undoBusyId()}
            disabled={acting() || scanning()}
            result={undoResult()}
            error={undoError()}
            onUndo={runUndo}
          />

          <ActivityLogPanel workflow="rename" refreshKey={logKey()} />
        </div>
      </Show>

      {/* ONE mount for BOTH entry points (.omc/plans/autopilot-impl.md §5.1).
          Re-pick and Move share SearchTakeover; repick uses props.mode as
          searchMode (same catalog as the proposal), move uses st.target.
          ModeTabs stays visible above it; a mode switch closes the takeover
          through resetOnModeChange. */}
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
                  ? `Search for a new match for “${p.sourceName}”`
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
                st.kind === "repick"
                  ? p.title || p.sourceName || ""
                  : p.sourceName
              }
              initialSeriesDatabase={
                st.kind === "repick" && props.mode === "series"
                  ? p.title
                    ? "tmdb"
                    : "tvdb"
                  : undefined
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
  const [adultAspect, setAdultAspect] = createSignal<AdultOrganizeAspect>(
    loadAdultOrganizeAspect(),
  );
  return (
    <div>
      <ModeTabs current={mode} onSelect={setMode} />
      <Show when={mode() === "adult"}>
        <AdultOrganizeAspectBar
          value={adultAspect()}
          onChange={setAdultAspect}
        />
      </Show>
      <RenameQueue mode={mode()} adultAspect={adultAspect()} />
    </div>
  );
};
