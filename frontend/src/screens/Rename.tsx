// Rename — the staged scan→propose→apply review queue, ported from the
// vanilla-JS frontend (internal/web/static/index.html's renderRename), with one
// deliberate enhancement on top of the port: mode-specific columns (Wade-approved
// follow-up, see .omc/handoffs/stage-3-rename.md — the old frontend's single
// generic table never surfaced these, and an earlier wave correctly declined to
// add them without an explicit decision). Scan enqueues proposals; the operator
// reviews a table of them and acts on EXACTLY ONE per click. There is no bulk
// affordance anywhere — no "apply all", no multi-select (Guardrail #3 / the
// project's no-bulk invariant); a dedicated test asserts this.
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
//   - Apply shows on a `pending` row; Give back on an `unmatched` row (any mode,
//     even though it is Adult-give-back-semantic); Re-pick on pending/unmatched
//     for Movies/Series only; Dismiss on pending/unmatched.
//   - Re-pick opens a single shared search panel below the table, auto-searches
//     the prefilled title on open, and sends the NEWLY chosen tmdbId (never the
//     proposal's current one).

import {
  type Component,
  createEffect,
  createResource,
  createSignal,
  For,
  on,
  Show,
} from "solid-js";
import type { Mode } from "../api/discover";
import {
  type Proposal,
  type ProposalStatus,
  applyProposal,
  dismissProposal,
  fetchProposals,
  repickProposal,
  scanRename,
  submitDraft,
  tmdbSearch,
} from "../api/rename";
import { Button, ErrorText, Muted } from "../components/ui";

const MODES: { id: Mode; label: string }[] = [
  { id: "movies", label: "Movies" },
  { id: "series", label: "Series" },
  { id: "adult", label: "Adult" },
];

// STATUS_STYLE colors the status pill — pending amber, applied green, unmatched
// muted, dismissed muted-strike. Keeps the review state scannable at a glance.
const STATUS_STYLE: Record<string, string> = {
  pending: "bg-warn/20 text-warn",
  applied: "bg-ok/20 text-ok",
  unmatched: "bg-surface-2 text-muted",
  dismissed: "bg-surface-2 text-muted",
};

const StatusPill: Component<{ status: string }> = (props) => (
  <span
    class="inline-block rounded-full px-2 py-0.5 text-[11px] font-medium"
    classList={{
      [STATUS_STYLE[props.status] ?? "bg-surface-2 text-muted"]: true,
    }}
  >
    {props.status}
  </span>
);

// yearOf pulls the leading 4-digit year from a TMDB date string ("YYYY-..").
function yearOf(date: string): number | undefined {
  const y = date && date.length >= 4 ? parseInt(date.slice(0, 4), 10) : NaN;
  return Number.isFinite(y) ? y : undefined;
}

// shortHash renders the PHash column value — the full scheme-tagged hash is
// too long to usefully show inline, so the cell shows a short prefix and the
// full value lives in the title attribute (hover) for anyone who needs it.
function shortHash(hash: string): string {
  return hash.length > 12 ? `${hash.slice(0, 12)}…` : hash;
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
  const [proposals, { refetch }] = createResource(
    () => props.mode,
    (m) => fetchProposals(m),
  );
  const [scanning, setScanning] = createSignal(false);
  const [actionError, setActionError] = createSignal("");
  const [repickFor, setRepickFor] = createSignal<Proposal | null>(null);

  // Switching modes clears any open re-pick panel and stale action error — the
  // old frontend rebuilt the whole view on a mode change, which had this effect.
  createEffect(
    on(
      () => props.mode,
      () => {
        setRepickFor(null);
        setActionError("");
      },
      { defer: true },
    ),
  );

  const isTitleMode = () => props.mode === "movies" || props.mode === "series";

  const scan = async () => {
    setActionError("");
    setScanning(true);
    try {
      await scanRename(props.mode);
      await refetch();
    } catch (e) {
      setActionError((e as Error).message);
    } finally {
      setScanning(false);
    }
  };

  // act runs one proposal mutation then refreshes the queue, surfacing any
  // error at the top. Every caller passes exactly one proposal id — the
  // no-bulk invariant lives here structurally: there is no "act on many" path.
  const act = async (fn: () => Promise<unknown>) => {
    setActionError("");
    try {
      await fn();
      setRepickFor(null);
      await refetch();
    } catch (e) {
      setActionError((e as Error).message);
    }
  };

  return (
    <div>
      <div class="flex items-center gap-3">
        <Button variant="primary" onClick={scan} disabled={scanning()}>
          {scanning() ? "Scanning…" : "Scan"}
        </Button>
      </div>

      <Show when={actionError()}>
        <ErrorText>{actionError()}</ErrorText>
      </Show>
      <Show when={proposals.error}>
        <ErrorText>{(proposals.error as Error)?.message}</ErrorText>
      </Show>

      <Show when={!proposals.loading} fallback={<Muted class="mt-4">Loading…</Muted>}>
        <Show
          when={proposals() && proposals()!.length > 0}
          fallback={<Muted class="mt-4">No proposals yet — click Scan.</Muted>}
        >
          <div class="mt-4 overflow-x-auto">
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
                  {(p) => {
                    const status = p.status as ProposalStatus;
                    const actionable =
                      status === "pending" || status === "unmatched";
                    return (
                      <tr class="border-b border-border/60 align-top">
                        <td class="px-2 py-2 text-fg">{p.sourceName}</td>
                        <td class="px-2 py-2 text-fg">{p.title || ""}</td>
                        <Show when={props.mode === "movies" || props.mode === "series"}>
                          <td class="px-2 py-2 text-muted">{p.year || ""}</td>
                        </Show>
                        <Show when={props.mode === "series"}>
                          <td class="px-2 py-2 text-muted">
                            {p.seasonNumber ?? ""}
                          </td>
                          <td class="px-2 py-2 text-muted">
                            {p.episodeNumber ?? ""}
                          </td>
                        </Show>
                        <Show when={props.mode === "adult"}>
                          <td class="px-2 py-2 text-muted">{p.studio || ""}</td>
                          <td class="px-2 py-2 text-muted">{p.date || ""}</td>
                          <td class="px-2 py-2 text-muted">
                            <Show when={p.phash}>
                              <span title={p.phash}>{shortHash(p.phash!)}</span>
                            </Show>
                          </td>
                        </Show>
                        <td class="px-2 py-2">
                          <StatusPill status={p.status} />
                        </td>
                        <td class="px-2 py-2 text-muted">
                          {p.rootFolderPath || ""}
                        </td>
                        <td class="px-2 py-2 text-muted">{p.reason || ""}</td>
                        <td class="px-2 py-2">
                          <div class="flex flex-wrap gap-1">
                            <Show when={status === "pending"}>
                              <Button
                                variant="primary"
                                onClick={() => act(() => applyProposal(p.id))}
                              >
                                Apply
                              </Button>
                            </Show>
                            <Show when={status === "unmatched"}>
                              <Button
                                disabled={!!p.draftId}
                                onClick={() => act(() => submitDraft(p.id))}
                              >
                                {p.draftId ? "Give backed" : "Give back"}
                              </Button>
                            </Show>
                            <Show when={actionable && isTitleMode()}>
                              <Button onClick={() => setRepickFor(p)}>
                                Re-pick
                              </Button>
                            </Show>
                            <Show when={actionable}>
                              <Button
                                class="!bg-danger !text-accent-fg"
                                onClick={() => act(() => dismissProposal(p.id))}
                              >
                                Dismiss
                              </Button>
                            </Show>
                          </div>
                        </td>
                      </tr>
                    );
                  }}
                </For>
              </tbody>
            </table>
          </div>
        </Show>
      </Show>

      <Show when={isTitleMode() && repickFor()}>
        {(p) => (
          <RepickPanel
            mode={props.mode as "movies" | "series"}
            proposal={p()}
            onDone={() => act(() => Promise.resolve())}
            onCancel={() => setRepickFor(null)}
          />
        )}
      </Show>
    </div>
  );
};

// Rename is the mode-switching shell: tab bar (Movies/Series/Adult) over the
// matching review queue.
export const Rename: Component = () => {
  const [mode, setMode] = createSignal<Mode>("movies");
  return (
    <div>
      <div class="mb-4 flex gap-1">
        <For each={MODES}>
          {(m) => (
            <button
              type="button"
              class="rounded-md px-3 py-1.5 text-sm font-medium transition"
              classList={{
                "bg-accent text-accent-fg": mode() === m.id,
                "bg-surface-2 text-muted hover:text-fg": mode() !== m.id,
              }}
              onClick={() => setMode(m.id)}
            >
              {m.label}
            </button>
          )}
        </For>
      </div>
      <RenameQueue mode={mode()} />
    </div>
  );
};
