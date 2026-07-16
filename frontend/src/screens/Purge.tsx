// Purge — the staged scan→propose→apply DELETE queue, ported verbatim from the
// vanilla-JS frontend (internal/web/static/index.html's renderPurge). Layout,
// top to bottom: Scan button → Allowlist (tag chips + add input) → Proposals
// table. Scan matches the mode's tag allowlist against every tracked item and
// enqueues one delete proposal per match; the operator reviews the queue and
// acts on EXACTLY ONE item per click — Apply (Delete) / Dismiss a proposal, or
// add/remove a single allowlist tag. There is no bulk affordance ANYWHERE
// (proposals OR allowlist) per the project's no-bulk invariant; dedicated tests
// assert both halves.
//
// Verbatim deltas from Rename (Purge is NOT structurally identical — verified
// against the old frontend, do not "align" these with Rename):
//   - No Source column. Columns are Title / Status / Root Folder / Reason /
//     Actions (title cell = p.title || p.sourceName || "").
//   - Actions appear ONLY on a pending row: "Apply (Delete)" (danger-styled,
//     behind a window.confirm guard because it permanently deletes files) and
//     "Dismiss". No Re-pick, no Give back, no draft — Purge has none of those.
//   - The allowlist section (above the queue) is Purge-only: each tag is a chip
//     with a single "×" remove button, plus one text input + Add button.

import {
  type Component,
  createResource,
  createSignal,
  For,
  Show,
} from "solid-js";
import type { Mode } from "../api/discover";
import {
  type Proposal,
  type ProposalStatus,
  addAllowlistTag,
  applyProposal,
  dismissProposal,
  fetchAllowlist,
  fetchPurgeProposals,
  removeAllowlistTag,
  scanPurge,
} from "../api/purge";
import {
  Button,
  ErrorText,
  ModeTabs,
  Muted,
  StatusPill,
} from "../components/ui";
import { useWorkflowActions } from "./workflowHooks";

// PurgeView is one mode's allowlist editor + delete-review queue. Keyed on
// props.mode so both resources refetch when the shell switches tabs.
const PurgeView: Component<{ mode: Mode }> = (props) => {
  const [proposals, { refetch: refetchProposals }] = createResource(
    () => props.mode,
    (m) => fetchPurgeProposals(m),
  );
  const [allowlist, { refetch: refetchAllowlist }] = createResource(
    () => props.mode,
    (m) => fetchAllowlist(m),
  );
  const [newTag, setNewTag] = createSignal("");
  // applyingIds tracks which proposal rows have an Apply (Delete) in flight —
  // per-row, not global, so unrelated rows stay usable. Guards the one
  // destructive path here against a double-click firing two DELETE requests
  // for the same row (Dismiss is non-destructive and left as-is).
  const [applyingIds, setApplyingIds] = createSignal<ReadonlySet<number>>(
    new Set(),
  );

  // Switching modes clears the stale add-input and action error — the old
  // frontend rebuilt the whole view on a mode change, which had this effect.
  const { actionError, setActionError, scanning, scan, act } = useWorkflowActions(
    () => props.mode,
    {
      resetOnModeChange: () => setNewTag(""),
      scanFn: scanPurge,
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

  // allowlist mutations refresh only the allowlist resource; each acts on one
  // tag (add: the single input; remove: one chip's ×). No batch path exists.
  const addTag = async () => {
    const tag = newTag().trim();
    if (!tag) return;
    setActionError("");
    try {
      await addAllowlistTag(props.mode, tag);
      setNewTag("");
      await refetchAllowlist();
    } catch (e) {
      setActionError((e as Error).message);
    }
  };
  const removeTag = async (tag: string) => {
    setActionError("");
    try {
      await removeAllowlistTag(props.mode, tag);
      await refetchAllowlist();
    } catch (e) {
      setActionError((e as Error).message);
    }
  };

  return (
    <div>
      <div class="flex items-center gap-3">
        <Button variant="primary" onClick={() => void scan(props.mode)} disabled={scanning()}>
          {scanning() ? "Scanning…" : "Scan"}
        </Button>
      </div>

      <Show when={actionError()}>
        <ErrorText>{actionError()}</ErrorText>
      </Show>

      {/* Allowlist — Purge-only. One × per chip, one Add per input. */}
      <h3 class="mt-6 text-sm font-semibold text-fg">Allowlist</h3>
      <Show when={allowlist.error}>
        <ErrorText>{(allowlist.error as Error)?.message}</ErrorText>
      </Show>
      <div class="mt-2 flex flex-wrap items-center gap-2">
        <For each={allowlist()}>
          {(tag) => (
            <span class="inline-flex items-center gap-1 rounded-full bg-surface-2 px-2 py-0.5 text-xs text-fg">
              {tag}
              <button
                type="button"
                class="text-muted hover:text-danger"
                aria-label={`Remove ${tag}`}
                onClick={() => void removeTag(tag)}
              >
                ×
              </button>
            </span>
          )}
        </For>
        <form
          class="flex items-center gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            void addTag();
          }}
        >
          <input
            class="w-40 rounded-md border border-border bg-bg px-3 py-1.5 text-sm text-fg outline-none focus:border-accent"
            placeholder="tag name"
            value={newTag()}
            onInput={(e) => setNewTag(e.currentTarget.value)}
            aria-label="New allowlist tag"
          />
          <Button type="submit">Add</Button>
        </form>
      </div>

      {/* Proposals — delete-review queue. */}
      <h3 class="mt-6 text-sm font-semibold text-fg">Proposals</h3>
      <Show when={proposals.error}>
        <ErrorText>{(proposals.error as Error)?.message}</ErrorText>
      </Show>
      <Show
        when={!proposals.loading}
        fallback={<Muted class="mt-2">Loading…</Muted>}
      >
        <Show
          when={proposals() && proposals()!.length > 0}
          fallback={<Muted class="mt-2">No proposals yet — click Scan.</Muted>}
        >
          <div class="mt-2 overflow-x-auto">
            <table class="w-full text-left text-sm">
              <thead>
                <tr class="border-b border-border text-xs uppercase tracking-wide text-muted">
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
        </Show>
      </Show>
    </div>
  );
};

// Purge is the mode-switching shell: tab bar (Movies/Series/Adult) over the
// matching allowlist editor + delete queue.
export const Purge: Component = () => {
  const [mode, setMode] = createSignal<Mode>("movies");
  return (
    <div>
      <ModeTabs current={mode} onSelect={setMode} />
      <PurgeView mode={mode()} />
    </div>
  );
};
