// Rename — the staged scan→propose→apply review queue, ported from the
// vanilla-JS frontend (internal/web/static/index.html's renderRename), with
// mode-specific columns (Wade-approved follow-up, see
// .omc/handoffs/stage-3-rename.md).
//
// Claude 2026-08-06: Apply all; dedicated row Apply; dropdown Re-pick/Dismiss only
//   (deep-interview-rename-apply-all-giveback-settings).
// Reason: Apply in the select looked like a mysterious pre-selection.
// Troubleshooting: Apply still in dropdown → OTHER_ACTIONS was reverted.
// Review if: Purge/Dedup adopt Apply all summary confirm.

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
  tmdbSearch,
} from "../api/rename";
import {
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
import { useWorkflowActions } from "./workflowHooks";
import {
  ActivityLogPanel,
  PageSizeSelect,
  PaginationBar,
  ShowHistoryToggle,
} from "./OrganizeChrome";

// Claude 2026-08-06: Apply is a dedicated button — not a dropdown option
// Reason: "Apply" in the select looked like a mysterious pre-selection; Apply
//   belongs on its own control. Dropdown is only Re-pick / Dismiss + Go.
// Troubleshooting: Apply still listed in dropdown → this block was reverted.
// Review if: operators want Apply back inside the select.
type OtherActionId = "repick" | "dismiss";

const OTHER_ACTIONS: { id: OtherActionId; label: string }[] = [
  { id: "repick", label: "Re-pick" },
  { id: "dismiss", label: "Dismiss" },
];

function otherActionEnabled(
  id: OtherActionId,
  status: string,
  titleMode: boolean,
): boolean {
  switch (id) {
    case "repick":
      return titleMode && (status === "pending" || status === "unmatched");
    case "dismiss":
      return status === "pending" || status === "unmatched";
  }
}

function anyOtherActionEnabled(status: string, titleMode: boolean): boolean {
  return OTHER_ACTIONS.some((a) =>
    otherActionEnabled(a.id, status, titleMode),
  );
}

function newNameOf(p: Proposal): string {
  if (p.title) {
    return p.year ? `${p.title} (${p.year})` : p.title;
  }
  return "(unknown)";
}

async function fetchAllLivePending(mode: Mode): Promise<Proposal[]> {
  const out: Proposal[] = [];
  let offset = 0;
  const limit = 100;
  for (;;) {
    const page = await fetchProposals(mode, limit, offset, "live");
    for (const p of page.items) {
      if (p.status === "pending") out.push(p);
    }
    offset += page.items.length;
    if (page.items.length === 0 || offset >= page.total) break;
  }
  return out;
}

const RowActions: Component<{
  proposal: Proposal;
  titleMode: boolean;
  onApply: () => void;
  onRepick: () => void;
  onDismiss: () => void;
}> = (props) => {
  const [selected, setSelected] = createSignal<OtherActionId | "">("");

  const enabled = (id: OtherActionId) =>
    otherActionEnabled(id, props.proposal.status, props.titleMode);
  const hasOther = () =>
    anyOtherActionEnabled(props.proposal.status, props.titleMode);
  const canApply = () => props.proposal.status === "pending";

  createEffect(() => {
    const cur = selected();
    if (cur && !enabled(cur)) setSelected("");
  });

  const runOther = () => {
    const id = selected();
    if (!id || !enabled(id)) return;
    if (id === "repick") props.onRepick();
    else props.onDismiss();
  };

  return (
    <div class="flex flex-wrap items-center gap-1">
      <Show when={canApply()}>
        <Button variant="primary" onClick={() => props.onApply()}>
          Apply
        </Button>
      </Show>
      <select
        class="rounded border border-border bg-bg px-2 py-1 text-sm text-fg"
        aria-label={`Action for ${props.proposal.sourceName}`}
        value={selected()}
        disabled={!hasOther()}
        onChange={(e) =>
          setSelected(e.currentTarget.value as OtherActionId | "")
        }
      >
        <option value="">select action</option>
        <For each={OTHER_ACTIONS}>
          {(a) => (
            <option value={a.id} disabled={!enabled(a.id)}>
              {a.label}
            </option>
          )}
        </For>
      </select>
      <Button
        variant="secondary"
        disabled={!selected() || !enabled(selected() as OtherActionId)}
        onClick={runOther}
      >
        Go
      </Button>
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

const ApplyAllConfirm: Component<{
  rows: Proposal[];
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
          Apply {props.rows.length} pending rename
          {props.rows.length === 1 ? "" : "s"}?
        </h3>
        <Muted class="mt-1">Review original → new name, then confirm.</Muted>
      </div>
      <div class="max-h-[50vh] overflow-y-auto px-4 py-2">
        <table class="w-full text-left text-sm">
          <thead>
            <tr class="text-xs uppercase tracking-wide text-muted">
              <th class="py-2 pr-2 font-medium">Original name</th>
              <th class="py-2 font-medium">New name</th>
            </tr>
          </thead>
          <tbody>
            <For each={props.rows}>
              {(p) => (
                <tr class="border-t border-border/60 align-top">
                  <td class="py-2 pr-2 font-mono text-xs">{p.sourceName}</td>
                  <td class="py-2 text-sm">{newNameOf(p)}</td>
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
  const [batchResult, setBatchResult] = createSignal<ApplyBatchResponse | null>(
    null,
  );
  const [applyAllRows, setApplyAllRows] = createSignal<Proposal[] | null>(null);
  const [applyAllLoading, setApplyAllLoading] = createSignal(false);

  const { actionError, setActionError, scanning, scan, act } = useWorkflowActions(
    () => props.mode,
    {
      resetOnModeChange: () => {
        setRepickFor(null);
        setBatchResult(null);
        setApplyAllRows(null);
        setOffset(0);
        setApplyProgress("");
      },
      scanFn: scanRename,
      resetAfterScan: () => {
        setBatchResult(null);
        setApplyAllRows(null);
        setOffset(0);
        setLogKey((k) => k + 1);
      },
      resetAfterAct: () => {
        setRepickFor(null);
      },
      refetch,
    },
  );

  const isTitleMode = () => props.mode === "movies" || props.mode === "series";

  const titleOf = (id: number): string => {
    const p = proposals().find((x) => x.id === id);
    return p ? p.title || p.sourceName || "" : "";
  };

  const openApplyAll = (): void => {
    setActionError("");
    setApplyAllLoading(true);
    void (async () => {
      try {
        const rows = await fetchAllLivePending(props.mode);
        if (rows.length === 0) {
          setActionError("No pending renames to apply.");
          return;
        }
        setApplyAllRows(rows);
      } catch (e) {
        setActionError((e as Error).message);
      } finally {
        setApplyAllLoading(false);
      }
    })();
  };

  const confirmApplyAll = (): void => {
    const rows = applyAllRows();
    if (!rows || rows.length === 0) return;
    setApplyAllRows(null);
    setBatchResult(null);
    setApplyProgress("");
    const items = rows.map((p) => ({ id: p.id }));
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
      <div class="flex flex-wrap items-center gap-3">
        <Button variant="primary" onClick={() => void scan(props.mode)} disabled={scanning()}>
          {scanning() ? "Scanning…" : "Scan"}
        </Button>
        <Show when={!showHistory()}>
          <Button
            variant="primary"
            onClick={openApplyAll}
            disabled={applyAllLoading() || scanning()}
          >
            {applyAllLoading() ? "Loading…" : "Apply all"}
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

      <Show when={applyAllRows()}>
        {(rows) => (
          <ApplyAllConfirm
            rows={rows()}
            onCancel={() => setApplyAllRows(null)}
            onConfirm={confirmApplyAll}
          />
        )}
      </Show>

      <ActivityLogPanel workflow="rename" refreshKey={logKey()} />
    </div>
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
