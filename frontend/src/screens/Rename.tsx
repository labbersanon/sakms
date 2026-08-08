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
  yearOf,
} from "../components/ui";
import { useWorkflowActions } from "./workflowHooks";
import {
  ActivityLogPanel,
  PageSizeSelect,
  PaginationBar,
  ShowHistoryToggle,
} from "./OrganizeChrome";

type RowActionId = "rename" | "repick" | "dismiss";

const ROW_ACTIONS: { id: RowActionId; label: string }[] = [
  { id: "rename", label: "Rename" },
  { id: "repick", label: "Re-pick" },
  { id: "dismiss", label: "Dismiss" },
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

function planActionForRow(
  p: Proposal,
  dropdown: RowActionId | "",
  titleMode: boolean,
): "apply" | "dismiss" | null {
  const action =
    dropdown || defaultRowAction(p.status, titleMode);
  if (action === "rename" && rowActionEnabled("rename", p.status, titleMode)) {
    return "apply";
  }
  if (action === "dismiss" && rowActionEnabled("dismiss", p.status, titleMode)) {
    return "dismiss";
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

type ApplyAllPlanItem = {
  proposal: Proposal;
  action: "apply" | "dismiss";
};

const RowActions: Component<{
  proposal: Proposal;
  titleMode: boolean;
  selected: RowActionId | "";
  onSelect: (id: RowActionId | "") => void;
  onRun: (id: RowActionId) => void;
  disabled?: boolean;
}> = (props) => {
  const enabled = (id: RowActionId) =>
    rowActionEnabled(id, props.proposal.status, props.titleMode);
  const hasAny = () => ROW_ACTIONS.some((a) => enabled(a.id));
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
        <For each={ROW_ACTIONS}>
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

  // Manual slot assignment (series only) — for a file with no recoverable
  // episode signal at all. Pre-filled from the proposal's current values when
  // they are non-zero so an operator correcting an ambiguous match starts from
  // what is already there, null otherwise ("not assigning a slot").
  const [season, setSeason] = createSignal<number | null>(
    props.proposal.seasonNumber ? props.proposal.seasonNumber : null,
  );
  const [episode, setEpisode] = createSignal<number | null>(
    props.proposal.episodeNumber ? props.proposal.episodeNumber : null,
  );
  const parseSlot = (raw: string): number | null => {
    const t = raw.trim();
    if (t === "") return null;
    const n = Number(t);
    return Number.isInteger(n) ? n : null;
  };
  // Exactly one of the two filled — the server 400s this too; the inline error
  // is the UX, not the guard.
  const halfPair = () => (season() != null) !== (episode() != null);

  const use = async (id: number, title: string, year?: number) => {
    setError("");
    if (halfPair()) {
      setError("Enter both a season and an episode, or leave both blank.");
      return;
    }
    setBusy(true);
    try {
      // `!= null`, NEVER a truthiness check: season 0 is Specials, a real
      // value an operator can mean, and `if (s)` would silently drop it —
      // the exact defect the *int DTO fields exist to prevent.
      const s = season();
      const e = episode();
      await repickProposal(
        props.proposal.id,
        s != null && e != null
          ? { tmdbId: id, title, year, seasonNumber: s, episodeNumber: e }
          : { tmdbId: id, title, year },
      );
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
      <Show when={props.mode === "series"}>
        <div class="mt-3 rounded-md border border-border bg-surface-2 p-3">
          <Muted>
            Optional: assign a season and episode directly. Use this when the
            filename has no recoverable episode information. Season 0 is
            Specials.
          </Muted>
          <div class="mt-2 flex items-center gap-3">
            <label class="flex items-center gap-2 text-sm text-fg">
              Season
              <input
                type="number"
                min="0"
                step="1"
                class="w-24 rounded-md border border-border bg-bg px-2 py-1 text-sm text-fg outline-none focus:border-accent"
                value={season() ?? ""}
                onInput={(e) => setSeason(parseSlot(e.currentTarget.value))}
                aria-label="Season number"
              />
            </label>
            <label class="flex items-center gap-2 text-sm text-fg">
              Episode
              <input
                type="number"
                min="1"
                step="1"
                class="w-24 rounded-md border border-border bg-bg px-2 py-1 text-sm text-fg outline-none focus:border-accent"
                value={episode() ?? ""}
                onInput={(e) => setEpisode(parseSlot(e.currentTarget.value))}
                aria-label="Episode number"
              />
            </label>
          </div>
          <Show when={halfPair()}>
            <ErrorText>
              Enter both a season and an episode, or leave both blank.
            </ErrorText>
          </Show>
        </div>
      </Show>
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
                    {item.action === "apply" ? "rename" : item.action}
                  </td>
                  <td class="py-2 text-sm">
                    {item.action === "apply"
                      ? newNameOf(item.proposal)
                      : "Dismiss proposal"}
                  </td>
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

  // Seed Rename for newly visible matches; clear invalid choices.
  createEffect(() => {
    const titleMode = isTitleMode();
    const items = proposals();
    setSelections((prev) => {
      let changed = false;
      const next = { ...prev };
      for (const p of items) {
        if (next[p.id] === undefined) {
          next[p.id] = defaultRowAction(p.status, titleMode);
          changed = true;
        } else {
          const cur = next[p.id];
          if (cur && !rowActionEnabled(cur, p.status, titleMode)) {
            next[p.id] = defaultRowAction(p.status, titleMode);
            changed = true;
          }
        }
      }
      return changed ? next : prev;
    });
  });

  const { actionError, setActionError, scanning, acting, scan, act } =
    useWorkflowActions(() => props.mode, {
      resetOnModeChange: () => {
        setRepickFor(null);
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
        setRepickFor(null);
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
      setRepickFor(p);
    } else {
      void act(() => dismissProposal(p.id)).then(() => setLogKey((k) => k + 1));
    }
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
    void act(async () => {
      const results: ApplyBatchResultItem[] = [];
      const total = toApply.length + toDismiss.length;
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
      setBatchResult({ results });
      setApplyProgress("");
      setLogKey((k) => k + 1);
    });
  };

  return (
    <div>
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
                      <td class="px-2 py-2 font-mono text-xs">{p.sourceName}</td>
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
