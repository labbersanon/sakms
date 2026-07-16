// Tag — direct CRUD on a tracked item's tags, ported verbatim from the
// vanilla-JS frontend (internal/web/static/index.html's renderTag). Layout, top
// to bottom: a mode tab bar (Movies/Series/Adult) over a table with one row per
// tracked item — Title / Tags (removable chips) / Add tag (an autocomplete
// input + Add button). A shared <datalist> feeds the input the mode's existing
// tag vocabulary.
//
// DELIBERATELY SIMPLER than Rename/Purge/Dedup: there is NO staged
// scan→propose→apply queue here — add and remove commit immediately, then the
// view refetches both vocab and tracked (mirroring the old renderTag's full
// re-render). This is verbatim old behavior, not a regression; the backend's
// addItemTagHandler/addSceneTagHandler are single, immediately-committed actions
// by design (see internal/api/tag.go).
//
// No-bulk invariant (Acceptance Criterion 6): the unit of action is ONE
// (item, tag) pair. Every chip's × removes one tag from one item; every Add
// assigns one tag to one item. There is no add-to-all / clear-all affordance
// anywhere — the risk is lower here than in the deleting workflows, but the
// invariant still holds and a test asserts it.
//
// CRITICAL per-mode endpoint routing (see src/api/tag.ts + internal/api/tag.go):
// Movies/Series hit the GENERIC item-tag routes; Adult hits its OWN DEDICATED
// scene-tag routes (the generic routes 400 for Adult). That branching lives
// entirely in the api/tag.ts helpers — this component just passes props.mode
// through, so it is genuinely mode-agnostic.

import {
  type Component,
  createResource,
  createSignal,
  For,
  Show,
} from "solid-js";
import type { Mode } from "../api/discover";
import {
  type TrackedItem,
  addTag,
  fetchTagVocabulary,
  fetchTrackedItems,
  removeTag,
} from "../api/tag";
import {
  Button,
  ErrorText,
  ModeTabs,
  Muted,
  inputClass,
} from "../components/ui";
import { useWorkflowActions } from "./workflowHooks";

// TagView is one mode's tag editor. Keyed on props.mode so both resources
// refetch when the shell switches tabs. vocab + tracked load in parallel.
const TagView: Component<{ mode: Mode }> = (props) => {
  const [vocab, { refetch: refetchVocab }] = createResource(
    () => props.mode,
    (m) => fetchTagVocabulary(m),
  );
  const [tracked, { refetch: refetchTracked }] = createResource(
    () => props.mode,
    (m) => fetchTrackedItems(m),
  );
  // draft maps a tracked item id → the text currently typed in its add-tag
  // input. Cleared on a successful add and on mode switch so a stale draft never
  // leaks onto another mode's rows.
  const [draft, setDraft] = createSignal<Record<number, string>>({});

  const datalistId = () => `tag-vocab-${props.mode}`;

  // refresh re-pulls both vocab (a newly added label may be new to the mode)
  // and the tracked rows after any mutation — verbatim old renderTag behavior.
  const refresh = async () => {
    await Promise.all([refetchVocab(), refetchTracked()]);
  };

  // Tag has no scan button — omit scanFn. act wraps the shared error-capture
  // and mode-change cleanup; refetch calls refresh() which refetches both
  // resources in parallel (vocab + tracked), matching the old renderTag behavior.
  const { actionError, act } = useWorkflowActions(
    () => props.mode,
    {
      resetOnModeChange: () => setDraft({}),
      refetch: refresh,
    },
  );

  const submitAdd = (item: TrackedItem) => {
    const label = (draft()[item.id] ?? "").trim();
    if (!label) return;
    void act(async () => {
      await addTag(props.mode, item.id, label);
      setDraft((prev) => ({ ...prev, [item.id]: "" }));
    });
  };

  const loading = () => vocab.loading || tracked.loading;

  return (
    <div>
      <Show when={actionError()}>
        <ErrorText>{actionError()}</ErrorText>
      </Show>
      <Show when={vocab.error}>
        <ErrorText>{(vocab.error as Error)?.message}</ErrorText>
      </Show>
      <Show when={tracked.error}>
        <ErrorText>{(tracked.error as Error)?.message}</ErrorText>
      </Show>

      {/* Autocomplete source for every row's add-tag input. */}
      <datalist id={datalistId()}>
        <For each={vocab() ?? []}>
          {(t) => <option value={t.label} />}
        </For>
      </datalist>

      <Show when={!loading()} fallback={<Muted class="mt-4">Loading…</Muted>}>
        <Show
          when={tracked() && tracked()!.length > 0}
          fallback={<Muted class="mt-4">Nothing tracked yet.</Muted>}
        >
          <div class="mt-2 overflow-x-auto">
            <table class="w-full text-left text-sm">
              <thead>
                <tr class="border-b border-border text-xs uppercase tracking-wide text-muted">
                  <th class="px-2 py-2 font-medium">Title</th>
                  <th class="px-2 py-2 font-medium">Tags</th>
                  <th class="px-2 py-2 font-medium">Add tag</th>
                </tr>
              </thead>
              <tbody>
                <For each={tracked()}>
                  {(item) => (
                    <tr class="border-b border-border/60 align-top">
                      <td class="px-2 py-2 text-fg">{item.title}</td>
                      <td class="px-2 py-2">
                        <div class="flex flex-wrap gap-1">
                          <For each={item.tags ?? []}>
                            {(tag) => (
                              <span class="inline-flex items-center gap-1 rounded-full bg-surface-2 px-2 py-0.5 text-xs text-fg">
                                {tag}
                                <button
                                  type="button"
                                  class="text-muted hover:text-danger"
                                  aria-label={`Remove ${tag}`}
                                  onClick={() =>
                                    void act(() =>
                                      removeTag(
                                        props.mode,
                                        item.id,
                                        tag,
                                      ),
                                    )
                                  }
                                >
                                  ×
                                </button>
                              </span>
                            )}
                          </For>
                        </div>
                      </td>
                      <td class="px-2 py-2">
                        <div class="flex items-center gap-2">
                          <input
                            type="text"
                            class={`${inputClass} !w-40`}
                            list={datalistId()}
                            placeholder="tag label"
                            aria-label={`Add tag to ${item.title}`}
                            value={draft()[item.id] ?? ""}
                            onInput={(e) =>
                              setDraft((prev) => ({
                                ...prev,
                                [item.id]: e.currentTarget.value,
                              }))
                            }
                            onKeyDown={(e) => {
                              if (e.key === "Enter") {
                                e.preventDefault();
                                submitAdd(item);
                              }
                            }}
                          />
                          <Button onClick={() => submitAdd(item)}>Add</Button>
                        </div>
                      </td>
                    </tr>
                  )}
                </For>
              </tbody>
            </table>
          </div>
        </Show>
      </Show>
    </div>
  );
};

// Tag is the mode-switching shell: tab bar (Movies/Series/Adult) over the
// matching mode's tag editor.
export const Tag: Component = () => {
  const [mode, setMode] = createSignal<Mode>("movies");
  return (
    <div>
      <ModeTabs current={mode} onSelect={setMode} />
      <TagView mode={mode()} />
    </div>
  );
};
