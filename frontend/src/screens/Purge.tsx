// Clean-up — the staged scan→propose→apply DELETE queue. Layout, top to
// bottom: Scan button → Rules card (collapsible) → Proposals table. Scan
// evaluates the mode's enabled pruning rules against every tracked item and
// enqueues one delete proposal per match; the operator reviews the queue and
// acts on each item via its own single-item control — Apply (Delete) or
// Dismiss.
//
// "Clean-up" is the OPERATOR-FACING name only. Every internal identifier here
// is still `purge` — the route, the Go package, the workflow key, the
// localStorage keys (sakms.purge.pageSize / .showHistory), the ?tab=purge
// value. Do not "finish" the rename.
//
// Claude 2026-08-11: the standalone per-mode tag Allowlist section that used to
// sit between the toolbar and the Proposals table is REPLACED by
// <PurgeRulesCard>, in the same position.
// Reason: the global allowlist mechanism is retired; tags are now a fourth
// AND'd condition on a pruning rule, so matching is configured entirely in the
// Rules card — one mechanism, on the screen that shows its results.
// Review if: Clean-up ever gains a second matching mechanism.
//
// One bounded bulk affordance exists on the PROPOSALS queue only: Pending
// rows carry a selection checkbox plus a select-all-pending header toggle, and
// with a non-empty selection an "Apply Selected (N)" button (behind the same
// window.confirm guard the single delete has, worded for the count) posts one
// apply-batch that the backend applies sequentially with skip-and-continue. It
// is NOT a queue-wide delete-all and does not change how any single row deletes.
// The RULES CARD's tag input stays deliberately bulk-free — one × per chip, one
// Add per input, no clear-all/remove-all path; a dedicated test asserts that.
//
// Deltas from Rename (Clean-up is NOT structurally identical — verified against
// the old frontend, do not "align" these with Rename):
//   - No Source column. Columns are Title / Status / Root Folder / Reason /
//     Actions (title cell = p.title || p.sourceName || ""), preceded by the
//     selection-checkbox column.
//   - Actions appear ONLY on a pending row: "Apply (Delete)" (danger-styled,
//     behind a window.confirm guard because it permanently deletes files) and
//     "Dismiss". No Re-pick, no Give back, no draft — Clean-up has none of those.

import {
  type Component,
  createResource,
  createSignal,
  For,
  Show,
} from "solid-js";
import type { ApplyBatchItem, ApplyBatchResponse } from "@dto";
import type { Mode } from "../api/discover";
import type { AdultOrganizeAspect } from "../api/organize";
import {
  type Proposal,
  type ProposalStatus,
  applyProposal,
  dismissProposal,
  fetchPurgeProposals,
  scanPurge,
} from "../api/purge";
import {
  applyBatchStreaming,
  loadPageSize,
  loadShowHistory,
  savePageSize,
  saveShowHistory,
} from "../api/organize";
import {
  BatchResultSummary,
  Button,
  ErrorText,
  ModeTabs,
  Muted,
  StatusPill,
} from "../components/ui";
import { useBulkSelection, useWorkflowActions } from "./workflowHooks";
import {
  ActivityLogPanel,
  AdultOrganizeAspectBar,
  loadAdultOrganizeAspect,
  PageSizeSelect,
  PaginationBar,
  ShowHistoryToggle,
} from "./OrganizeChrome";
import { PurgeRulesCard } from "./PurgeRulesCard";

// PurgeView is one mode's Rules card + delete-review queue. Keyed on
// props.mode so both refetch when the shell switches tabs.
const PurgeView: Component<{ mode: Mode; adultAspect: AdultOrganizeAspect }> = (props) => {
  const [pageSize, setPageSize] = createSignal(loadPageSize("purge"));
  const [offset, setOffset] = createSignal(0);
  const [logKey, setLogKey] = createSignal(0);
  const [applyProgress, setApplyProgress] = createSignal("");
  const [showHistory, setShowHistory] = createSignal(loadShowHistory("purge"));
  const listView = (): "live" | "history" =>
    showHistory() ? "history" : "live";
  const [page, { refetch: refetchProposals }] = createResource(
    () => ({
      mode: props.mode,
      limit: pageSize(),
      offset: offset(),
      view: listView(),
    }),
    ({ mode, limit, offset, view }) =>
      fetchPurgeProposals(mode, limit, offset, view),
  );
  const proposals = () => page()?.items ?? [];
  // applyingIds tracks which proposal rows have an Apply (Delete) in flight —
  // per-row, not global, so unrelated rows stay usable. Guards the one
  // destructive path here against a double-click firing two DELETE requests
  // for the same row (Dismiss is non-destructive and left as-is).
  const [applyingIds, setApplyingIds] = createSignal<ReadonlySet<number>>(
    new Set(),
  );
  // Bulk selection of Pending delete proposals + the last "Apply Selected"
  // outcome. Selection clears on mode-change/scan/act; batchResult persists past
  // act (so its own summary survives) but clears on the next scan, mode-change,
  // or new batch. Selection lives on the proposals queue only — the Rules
  // card has no batch path.
  const selection = useBulkSelection();
  const [batchResult, setBatchResult] = createSignal<ApplyBatchResponse | null>(
    null,
  );

  // Switching modes clears the stale action error, selection, and the previous
  // batch summary — the old frontend rebuilt the whole view on a mode change,
  // which had this effect. act clears the selection but NOT batchResult, so a
  // batch's own summary survives the act that produced it.
  const { actionError, scanning, scan, act } = useWorkflowActions(
    () => props.mode,
    {
      resetOnModeChange: () => {
        selection.clear();
        setBatchResult(null);
        setOffset(0);
        setApplyProgress("");
      },
      scanFn: (m) =>
        scanPurge(m, m === "adult" ? props.adultAspect : undefined),
      resetAfterScan: () => {
        selection.clear();
        setBatchResult(null);
        setOffset(0);
        setLogKey((k) => k + 1);
      },
      resetAfterAct: () => selection.clear(),
      refetch: refetchProposals,
    },
  );

  // apply guards the destructive delete behind a confirm, matching the old
  // frontend. jsdom's window.confirm returns false by default, so tests must
  // stub it — that is deliberate: the guard is real behavior worth testing.
  // The applyingIds check is a synchronous re-entrancy guard: a second click
  // on the same row while the first request is still pending returns early
  // before any fetch fires, so a double-click never issues two Apply calls.
  const apply = (p: Proposal) => {
    const id = p.id;
    if (applyingIds().has(id)) return;
    const label = p.title || p.sourceName || "";
    if (!window.confirm(`Delete "${label}" from ${props.mode}?`)) return;
    setApplyingIds((prev) => new Set(prev).add(id));
    void act(() => applyProposal(id)).finally(() => {
      setApplyingIds((prev) => {
        const next = new Set(prev);
        next.delete(id);
        return next;
      });
    });
  };

  // pendingIds are the rows selectable/batchable — only Pending rows can Apply.
  const pendingIds = (): number[] =>
    proposals().filter((p) => p.status === "pending").map((p) => p.id);
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
  // Claude 2026-08-05: Purge 20-item UI cap removed — page size is the bound.
  // Reason: deep-interview-organize-pagination-log
  // Review if: Purge regains a hard apply-batch ceiling.

  // applySelected deletes the selected Pending rows in one apply-batch. Guarded
  // by the same confirm the single delete has, worded for the count — bulk must
  // not skip the safety check single-item delete enforces.
  const applySelected = (): void => {
    const ids = [...selection.selected()];
    if (ids.length === 0) return;
    if (!window.confirm(`Delete ${ids.length} items from ${props.mode}?`)) return;
    const items: ApplyBatchItem[] = ids.map((id) => {
      const p = proposals().find((x) => x.id === id);
      return { id, sourcePath: p?.sourcePath };
    });
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
    <div>
      <h2 class="mb-4 text-lg font-semibold text-fg">Clean-up</h2>
      <div class="flex flex-wrap items-center gap-3">
        <Button variant="primary" onClick={() => void scan(props.mode)} disabled={scanning()}>
          {scanning() ? "Scanning…" : "Scan"}
        </Button>
        <Show when={selection.size() > 0}>
          <Button
            class="!bg-danger !text-accent-fg"
            onClick={applySelected}
          >
            Apply Selected ({selection.size()})
          </Button>
        </Show>
        <PageSizeSelect
          value={pageSize()}
          onChange={(n) => {
            savePageSize("purge", n);
            setPageSize(n);
            setOffset(0);
          }}
        />
        <ShowHistoryToggle
          checked={showHistory()}
          onChange={(on) => {
            saveShowHistory("purge", on);
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
      <Show when={batchResult()}>
        {(res) => <BatchResultSummary result={res()} titleOf={titleOf} />}
      </Show>

      {/* Rules — the sole matching mechanism, in the position the standalone
          tag list used to occupy. */}
      <PurgeRulesCard mode={props.mode} />

      {/* Proposals — delete-review queue. */}
      <h3 class="mt-6 text-sm font-semibold text-fg">Proposals</h3>
      <Show when={page.error}>
        <ErrorText>{(page.error as Error)?.message}</ErrorText>
      </Show>
      <Show
        when={!page.loading}
        fallback={<Muted class="mt-2">Loading…</Muted>}
      >
        <Show
          when={proposals().length > 0}
          fallback={
            <Muted class="mt-2">
              {showHistory()
                ? "No history yet."
                : "No proposals yet — click Scan."}
            </Muted>
          }
        >
          <div class="mt-2 overflow-x-auto">
            <table class="w-full text-left text-sm">
              <thead>
                <tr class="border-b border-border text-xs uppercase tracking-wide text-muted">
                  <th class="px-2 py-2 font-medium">
                    <input
                      type="checkbox"
                      aria-label="Select all pending"
                      checked={allPendingSelected()}
                      disabled={pendingIds().length === 0}
                      onChange={toggleSelectAll}
                    />
                  </th>
                  <th class="px-2 py-2 font-medium">Title</th>
                  <th class="px-2 py-2 font-medium">Status</th>
                  <th class="px-2 py-2 font-medium">Root Folder</th>
                  <th class="px-2 py-2 font-medium">Reason</th>
                  <th class="px-2 py-2 font-medium">Actions</th>
                </tr>
              </thead>
              <tbody>
                <For each={proposals()}>
                  {(p) => {
                    const status = p.status as ProposalStatus;
                    return (
                      <tr class="border-b border-border/60 align-top">
                        <td class="px-2 py-2">
                          <Show when={status === "pending"}>
                            <input
                              type="checkbox"
                              aria-label={`Select ${p.title || p.sourceName || ""}`}
                              checked={selection.has(p.id)}
                              onChange={() => selection.toggle(p.id)}
                            />
                          </Show>
                        </td>
                        <td class="px-2 py-2 text-fg">
                          {p.title || p.sourceName || ""}
                        </td>
                        <td class="px-2 py-2">
                          <StatusPill status={p.status} />
                        </td>
                        <td class="px-2 py-2 text-muted">
                          {p.rootFolderPath || ""}
                        </td>
                        <td class="px-2 py-2 text-muted">{p.reason || ""}</td>
                        <td class="px-2 py-2">
                          <Show when={status === "pending"}>
                            <div class="flex flex-wrap gap-1">
                              <Button
                                class="!bg-danger !text-accent-fg"
                                disabled={applyingIds().has(p.id)}
                                onClick={() => apply(p)}
                              >
                                {applyingIds().has(p.id)
                                  ? "Deleting…"
                                  : "Apply (Delete)"}
                              </Button>
                              <Button
                                onClick={() =>
                                  void act(() => dismissProposal(p.id))
                                }
                              >
                                Dismiss
                              </Button>
                            </div>
                          </Show>
                        </td>
                      </tr>
                    );
                  }}
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

      <ActivityLogPanel workflow="purge" refreshKey={logKey()} />
    </div>
  );
};

// Purge is the mode-switching shell: tab bar (Movies/Series/Adult) over the
// matching Rules card + delete queue.
export const Purge: Component = () => {
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
      <PurgeView mode={mode()} adultAspect={adultAspect()} />
    </div>
  );
};
