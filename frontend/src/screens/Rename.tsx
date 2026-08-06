// Rename — the staged scan→propose→apply review queue, ported from the
// vanilla-JS frontend (internal/web/static/index.html's renderRename), with one
// deliberate enhancement on top of the port: mode-specific columns (Wade-approved
// follow-up, see .omc/handoffs/stage-3-rename.md — the old frontend's single
// generic table never surfaced these, and an earlier wave correctly declined to
// add them without an explicit decision). Scan enqueues proposals; the operator
// reviews a table of them and acts on each row via dropdown + Go.
//
// Bulk apply — the one bounded exception to the project's original "one item at
// a time, no apply-everything" rule (a deliberate, documented reversal; see
// ROADMAP.md's Bulk apply entry and the top-level CLAUDE.md's amended
// engineering-conventions note). Pending rows carry a selection checkbox plus a
// select-all-pending header toggle; with a non-empty selection an "Apply
// Selected (N)" button posts one apply-batch request covering exactly those
// already-reviewed rows, which the backend applies sequentially with
// skip-and-continue and reports per item. This is NOT a queue-wide apply-all,
// and it does not change how any single row still applies one at a time via its
// own dropdown + Go.
//
// Table shape:
//   - Shared columns, every mode: Source / Title / Status / Root Folder /
//     Reason / Actions.
//   - Movies additionally show Year.
//   - Series additionally show Year / Season / Episode.
//   - Adult additionally show Studio / Date / PHash (truncated with a title
//     attribute for the full value — proposals.Proposal's PHash is a long
//     scheme-tagged hex string, not something to render in full inline).
//   Extra columns are only ever ADDED for a mode, never removed from the
//   shared set — Source/Title/Status/Root Folder/Reason/Actions stay present
//   and in the same relative order across all three modes.
//   - Per-row actions are a dropdown (Apply / Give back / Re-pick / Dismiss) with
//     inapplicable options disabled, plus a Go button that runs the selection
//     immediately. Default selection is the first enabled action for that row.
//   - Re-pick opens a single shared search panel below the table, auto-searches
//     the prefilled title on open, and sends the NEWLY chosen tmdbId (never the
//     proposal's current one).

import {
  type Component,
  createEffect,
  createResource,
  createSignal,
  For,
  Show,
} from "solid-js";
import type { Mode } from "../api/discover";
import type { ApplyBatchResponse } from "@dto";
import {
  type Proposal,
  type ProposalStatus,
  applyBatchStreaming,
  applyProposal,
  dismissProposal,
  fetchProposals,
  repickProposal,
  scanRename,
  submitDraft,
  tmdbSearch,
} from "../api/rename";
import {
  fetchPendingIDs,
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
  yearOf,
} from "../components/ui";
import { useBulkSelection, useWorkflowActions } from "./workflowHooks";
import {
  ActivityLogPanel,
  PageSizeSelect,
  PaginationBar,
  ShowHistoryToggle,
} from "./OrganizeChrome";

// Claude 2026-08-05: row action dropdown + Go (deep-interview-rename-row-action-dropdown).
// Reason: replace button cluster with select of all four actions (N/A disabled).
// Troubleshooting: Go disabled when no action is enabled for the row status.
// Review if: Purge/Dedup adopt the same control.
type RowActionId = "apply" | "giveback" | "repick" | "dismiss";

const ROW_ACTIONS: { id: RowActionId; label: string }[] = [
  { id: "apply", label: "Apply" },
  { id: "giveback", label: "Give back" },
  { id: "repick", label: "Re-pick" },
  { id: "dismiss", label: "Dismiss" },
];

function rowActionEnabled(
  id: RowActionId,
  status: string,
  titleMode: boolean,
): boolean {
  switch (id) {
    case "apply":
      return status === "pending";
    case "giveback":
      return status === "unmatched";
    case "repick":
      return titleMode && (status === "pending" || status === "unmatched");
    case "dismiss":
      return status === "pending" || status === "unmatched";
  }
}

function firstEnabledAction(
  status: string,
  titleMode: boolean,
): RowActionId | "" {
  for (const a of ROW_ACTIONS) {
    if (rowActionEnabled(a.id, status, titleMode)) return a.id;
  }
  return "";
}

// One proposal row's action dropdown + Go. Own signal so each row keeps its pick.
const RowActions: Component<{
  proposal: Proposal;
  titleMode: boolean;
  onApply: () => void;
  onGiveBack: () => void;
  onRepick: () => void;
  onDismiss: () => void;
}> = (props) => {
  const initial = () =>
    firstEnabledAction(props.proposal.status, props.titleMode);
  const [selected, setSelected] = createSignal<RowActionId | "">(initial());

  const enabled = (id: RowActionId) =>
    rowActionEnabled(id, props.proposal.status, props.titleMode);

  // If status changes after refetch, snap back to first enabled when current is invalid.
  createEffect(() => {
    const cur = selected();
    if (cur && enabled(cur)) return;
    setSelected(initial());
  });

  const run = () => {
    const id = selected();
    if (!id || !enabled(id)) return;
    if (id === "apply") props.onApply();
    else if (id === "giveback") props.onGiveBack();
    else if (id === "repick") props.onRepick();
    else props.onDismiss();
  };

  return (
    <div class="flex flex-wrap items-center gap-1">
      <select
        class="rounded border border-border bg-bg px-2 py-1 text-sm text-fg"
        aria-label={`Action for ${props.proposal.sourceName}`}
        value={selected()}
        disabled={!initial()}
        onChange={(e) =>
          setSelected(e.currentTarget.value as RowActionId | "")
        }
      >
        <For each={ROW_ACTIONS}>
          {(a) => (
            <option value={a.id} disabled={!enabled(a.id)}>
              {a.label}
            </option>
          )}
        </For>
      </select>
      <Button
        variant="primary"
        disabled={!selected() || !enabled(selected() as RowActionId)}
        onClick={run}
      >
        Go
      </Button>
    </div>
  );
};

// shortHash renders the PHash column value — the full scheme-tagged hash is
// too long to usefully show inline, so the cell shows a short prefix and the
// full value lives in the title attribute (hover) for anyone who needs it.
function shortHash(hash: string): string {
  return hash.length > 12 ? `${hash.slice(0, 12)}…` : hash;
}

// episodeDisplay renders the Episode column: a plain number for the
// ordinary single-episode case, or "N-M" (e.g. "1-2") for a logical-
// episode-split proposal (extraEpisodeNumbers non-empty) — so an operator
// sees BOTH episodes Apply will actually create before approving it,
// rather than only the primary number with the bundled one silently
// implied.
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

// RepickPanel is the shared Movies/Series re-pick search area — one instance
// below the table, opened against whichever proposal's Re-pick was clicked. It
// auto-searches the prefilled query on mount (matching the old openRepick's
// immediate runSearch()), and each result offers a single "Use this" that
// re-points the proposal at that NEW match, then closes and refreshes.
const RepickPanel: Component<{
  mode: "movies" | "series";
  proposal: Proposal;
  onDone: () => void;
  onCancel: () => void;
}> = (props) => {
  const [query, setQuery] = createSignal(
    props.proposal.title || props.proposal.sourceName || "",
  );
  const [submitted, setSubmitted] = createSignal(query());
  const [results] = createResource(submitted, async (q) => {
    if (!q.trim()) return [];
    return tmdbSearch(props.mode, q);
  });
  const [error, setError] = createSignal("");
  const [busy, setBusy] = createSignal(false);

  const use = async (id: number, title: string, year?: number) => {
    setError("");
    setBusy(true);
    try {
      await repickProposal(props.proposal.id, { tmdbId: id, title, year });
      props.onDone();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div class="mt-4 rounded-xl border border-border bg-surface p-4">
      <h4 class="text-sm font-semibold text-fg">
        Re-pick match for “{props.proposal.sourceName}”
      </h4>
      <Show when={props.proposal.title}>
        <Muted class="mt-1">
          Currently matched: {props.proposal.title}
          {props.proposal.year ? ` (${props.proposal.year})` : ""}
        </Muted>
      </Show>
      <form
        class="mt-2 flex items-center gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          setSubmitted(query());
        }}
      >
        <input
          class="w-80 max-w-full rounded-md border border-border bg-bg px-3 py-2 text-sm text-fg outline-none focus:border-accent"
          value={query()}
          onInput={(e) => setQuery(e.currentTarget.value)}
          aria-label="Re-pick search query"
        />
        <Button type="submit">Search</Button>
        <Button onClick={props.onCancel}>Cancel</Button>
      </form>
      <Show when={error()}>
        <ErrorText>{error()}</ErrorText>
      </Show>
      <div class="mt-3">
        <Show when={results.error}>
          <ErrorText>{(results.error as Error)?.message}</ErrorText>
        </Show>
        <Show when={!results.loading} fallback={<Muted>Searching…</Muted>}>
          <Show
            when={results() && results()!.length > 0}
            fallback={<Muted>No results.</Muted>}
          >
            <ul class="flex flex-col gap-1">
              <For each={results()}>
                {(item) => {
                  const y = yearOf(item.releaseDate);
                  return (
                    <li class="flex items-center gap-3 rounded-md border border-border bg-surface-2 p-2">
                      <span class="min-w-0 flex-1 truncate text-sm text-fg">
                        {item.title}
                        {y ? ` (${y})` : ""} — TMDB #{item.id}
                      </span>
                      <Button
                        variant="primary"
                        disabled={busy()}
                        onClick={() => use(item.id, item.title, y)}
                      >
                        Use this
                      </Button>
                    </li>
                  );
                }}
              </For>
            </ul>
          </Show>
        </Show>
      </div>
    </div>
  );
};

// RenameQueue is one mode's review table + actions. Keyed on props.mode so the
// resource refetches when the shell switches tabs.
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
  const [repickFor, setRepickFor] = createSignal<Proposal | null>(null);
  const selection = useBulkSelection();
  const [batchResult, setBatchResult] = createSignal<ApplyBatchResponse | null>(
    null,
  );

  const { actionError, setActionError, scanning, scan, act } = useWorkflowActions(
    () => props.mode,
    {
      resetOnModeChange: () => {
        setRepickFor(null);
        selection.clear();
        setBatchResult(null);
        setOffset(0);
        setApplyProgress("");
      },
      scanFn: scanRename,
      resetAfterScan: () => {
        selection.clear();
        setBatchResult(null);
        setOffset(0);
        setLogKey((k) => k + 1);
      },
      resetAfterAct: () => {
        setRepickFor(null);
        // Keep cross-page selection until apply clears it explicitly.
      },
      refetch,
    },
  );

  const isTitleMode = () => props.mode === "movies" || props.mode === "series";

  const pendingIds = (): number[] =>
    proposals()
      .filter((p) => p.status === "pending")
      .map((p) => p.id);
  const pagePendingSelected = (): boolean => {
    const ids = pendingIds();
    return ids.length > 0 && ids.every((id) => selection.has(id));
  };
  const selectPage = (): void => {
    selection.selectAll(pendingIds());
  };
  const selectAllMatching = (): void => {
    // Claude 2026-08-05: do not route through act() — selection is not a mutation
    // Reason: act() always refetches the proposal page after success, which flashed Loading and felt broken
    // Troubleshooting: Select all matching → pending-ids 200 but UI looked like it failed
    // Review if: select-all needs to refresh the page for a new reason
    void (async () => {
      setActionError("");
      try {
        const ids = await fetchPendingIDs(props.mode);
        selection.selectAll(ids);
      } catch (e) {
        setActionError((e as Error).message);
      }
    })();
  };
  const titleOf = (id: number): string => {
    const p = proposals().find((x) => x.id === id);
    return p ? p.title || p.sourceName || "" : "";
  };
  const applySelected = (): void => {
    const items = [...selection.selected()].map((id) => ({ id }));
    if (items.length === 0) return;
    setBatchResult(null);
    setApplyProgress("");
    void act(async () => {
      const out = await applyBatchStreaming(items, (done, total) => {
        setApplyProgress(`Applied ${done}/${total}…`);
      });
      setBatchResult(out as ApplyBatchResponse);
      setApplyProgress("");
      selection.clear();
      setLogKey((k) => k + 1);
    });
  };

  return (
    <div>
      <div class="flex flex-wrap items-center gap-3">
        <Button variant="primary" onClick={() => void scan(props.mode)} disabled={scanning()}>
          {scanning() ? "Scanning…" : "Scan"}
        </Button>
        <Show when={selection.size() > 0}>
          <Button variant="primary" onClick={applySelected}>
            Apply Selected ({selection.size()})
          </Button>
        </Show>
        <Button variant="secondary" onClick={selectPage} disabled={pendingIds().length === 0}>
          Select page
        </Button>
        <Button variant="secondary" onClick={selectAllMatching}>
          Select all matching
        </Button>
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
          {/* Claude 2026-08-05: frosted panel under Rename table
              Reason: deep-interview-rename-frosted-panel — navy/muted text was
                unreadable on ticket wallpaper (main bg-image)
              Troubleshooting: if text still washes out, raise opacity or solid bg-surface
              Review if: Purge/Dedup get the same treatment */}
          <div class="mt-4 overflow-x-auto rounded-xl border border-border bg-surface/95 p-3 shadow-sm backdrop-blur-md">
            <table class="w-full text-left text-sm">
              <thead>
                <tr class="border-b border-border text-xs uppercase tracking-wide text-muted">
                  <th class="px-2 py-2 font-medium">
                    <input
                      type="checkbox"
                      aria-label="Select all pending"
                      checked={pagePendingSelected()}
                      disabled={pendingIds().length === 0}
                      onChange={() => {
                        if (pagePendingSelected()) {
                          for (const id of pendingIds()) selection.toggle(id);
                        } else selectPage();
                      }}
                    />
                  </th>
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
                  <th class="px-2 py-2 font-medium">Status</th>
                  <th class="px-2 py-2 font-medium">Root Folder</th>
                  <th class="px-2 py-2 font-medium">Reason</th>
                  <th class="px-2 py-2 font-medium">Actions</th>
                </tr>
              </thead>
              <tbody>
                <For each={proposals()}>
                  {(p) => (
                    <tr class="border-b border-border/60 align-top">
                      <td class="px-2 py-2">
                        <Show when={p.status === "pending"}>
                          <input
                            type="checkbox"
                            checked={selection.has(p.id)}
                            onChange={() => selection.toggle(p.id)}
                            aria-label={`Select ${p.sourceName}`}
                          />
                        </Show>
                      </td>
                      <td class="px-2 py-2 font-mono text-xs">{p.sourceName}</td>
                      <td class="px-2 py-2">{p.title}</td>
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
                      <td class="px-2 py-2">
                        <div class="flex flex-wrap items-center gap-1">
                          <StatusPill status={p.status as ProposalStatus} />
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
                      <td class="px-2 py-2 font-mono text-xs">{p.rootFolderPath}</td>
                      <td class="px-2 py-2 text-muted">{p.reason}</td>
                      <td class="px-2 py-2">
                        <RowActions
                          proposal={p}
                          titleMode={isTitleMode()}
                          onApply={() =>
                            void act(() => applyProposal(p.id)).then(() =>
                              setLogKey((k) => k + 1),
                            )
                          }
                          onGiveBack={() =>
                            void act(() => submitDraft(p.id)).then(() =>
                              setLogKey((k) => k + 1),
                            )
                          }
                          onRepick={() => setRepickFor(p)}
                          onDismiss={() =>
                            void act(() => dismissProposal(p.id)).then(() =>
                              setLogKey((k) => k + 1),
                            )
                          }
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

      <Show when={isTitleMode() && repickFor()}>
        {(p) => (
          <RepickPanel
            mode={props.mode as "movies" | "series"}
            proposal={p()}
            onDone={() => {
              setActionError("");
              setRepickFor(null);
              void refetch();
            }}
            onCancel={() => setRepickFor(null)}
          />
        )}
      </Show>

      <ActivityLogPanel workflow="rename" refreshKey={logKey()} />
    </div>
  );
};

// Rename is the mode-switching shell: tab bar (Movies/Series/Adult) over the
// matching review queue.
export const Rename: Component = () => {
  const [mode, setMode] = createSignal<Mode>("movies");
  return (
    <div>
      <ModeTabs current={mode} onSelect={setMode} />
      <RenameQueue mode={mode()} />
    </div>
  );
};
