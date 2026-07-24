// BulkResultModal renders the outcome of one Discover select-mode bulk grab
// (F3). It is the counterpart to GrabDialog for the single-grab path: same
// three-state vocabulary, but one row per submitted item.
//
// Three per-item states (modeled on AutoGrabResponse, NOT apply-batch's binary
// ok/error): grabbed ✓ / fallback (an inline FallbackPickList so the operator
// can complete the pick without leaving) / error ✗. Errors and fallbacks — the
// rows that still need attention — are grouped FIRST so nothing actionable is
// buried under a wall of successes. The modal NEVER auto-dismisses: a silently
// closed results view is exactly the pre-mortem #1 failure (operator believes N
// grabbed when some fell back or errored).
//
// If the selection dropped orphaned keys before the request was sent (pre-mortem
// #5 — a card left the screen between selection and submit), the header shows
// "N selected, M submitted" so the drop is visible, never silent.

import {
  type Component,
  For,
  Match,
  Show,
  Switch,
  createSignal,
} from "solid-js";
import {
  type AutoGrabBatchItem,
  type AutoGrabBatchResponse,
  type AutoGrabBatchResult,
  type AutoGrabCandidate,
  type AutoGrabResponse,
  libraryRootFolder,
  manualGrab,
} from "../../api/grab";
import { FallbackPickList, Modal } from "./shared";
import { ErrorText, Muted } from "../../components/ui";

// FallbackRow completes one fell-back bulk item through the SAME manual pick
// path the single GrabDialog uses: fetch the mode's root folder, then POST one
// release to /search/grab. The original request (title/tmdbId/season) comes
// from the index-aligned submitted item, not the result (which doesn't carry
// it) — so the manual grab targets exactly what was selected.
const FallbackRow: Component<{
  item: AutoGrabBatchItem;
  result: AutoGrabBatchResult;
}> = (props) => {
  const [grabbing, setGrabbing] = createSignal("");
  const [error, setError] = createSignal("");
  const [grabbed, setGrabbed] = createSignal<string | null>(null);

  // Adapt the batch result into the AutoGrabResponse shape FallbackPickList
  // already renders, so there is exactly one pick-list implementation.
  const response = (): AutoGrabResponse => ({
    grabbed: false,
    fallback: true,
    message: props.result.message ?? "",
    candidates: props.result.candidates,
  });

  const pick = async (c: AutoGrabCandidate) => {
    setError("");
    setGrabbing(c.downloadUrl);
    try {
      const root = await libraryRootFolder(props.item.mode);
      if (!root) {
        throw new Error(
          "no root folder configured for this mode — set one in Settings first",
        );
      }
      const req = props.item.request;
      await manualGrab(props.item.mode, {
        title: req.title,
        tmdbId: req.tmdbId,
        seasonNumber: req.seasonNumber,
        episodeNumber: req.episodeNumber,
        seasonSpecified: req.seasonSpecified,
        indexer: c.indexer,
        protocol: c.protocol,
        downloadUrl: c.downloadUrl,
        rootFolderPath: root,
      });
      setGrabbed(c.title);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setGrabbing("");
    }
  };

  return (
    <Show
      when={grabbed()}
      fallback={
        <FallbackPickList
          response={response()}
          onPick={pick}
          grabbing={grabbing()}
          error={error()}
        />
      }
    >
      <div class="text-sm text-ok">
        Grabbed “{grabbed()}”. Tracked in the Grabs view.
      </div>
    </Show>
  );
};

export const BulkResultModal: Component<{
  // items is the batch actually submitted (index-aligned with response.results).
  items: AutoGrabBatchItem[];
  response: AutoGrabBatchResponse;
  // selectedCount is how many the operator had selected — may exceed
  // items.length if orphaned keys were dropped before submit (pre-mortem #5).
  selectedCount: number;
  onClose: () => void;
}> = (props) => {
  const results = () => props.response.results ?? [];
  const submittedCount = () => props.items.length;
  const droppedCount = () => props.selectedCount - submittedCount();
  const grabbedCount = () => results().filter((r) => r.grabbed).length;

  // Errors first, then fallbacks (both need action), then successes.
  const ordered = () => {
    const rank = (x: AutoGrabBatchResult) =>
      x.error ? 0 : x.fallback ? 1 : 2;
    return [...results()].sort((a, b) => rank(a) - rank(b));
  };

  return (
    <Modal title="Bulk grab results" onClose={props.onClose}>
      <div class="mb-3 text-sm text-muted">
        {props.selectedCount} selected, {submittedCount()} submitted
        <Show when={droppedCount() > 0}>
          {" "}
          · {droppedCount()} dropped (no longer on screen)
        </Show>
        {" "}
        · {grabbedCount()} grabbed
      </div>
      <ul class="flex flex-col gap-3">
        <For each={ordered()}>
          {(result) => (
            <li class="rounded-md border border-border bg-surface-2 p-3">
              <div class="mb-1 flex items-center justify-between gap-2">
                <span class="min-w-0 flex-1 truncate text-sm text-fg">
                  {result.label}
                </span>
                <span
                  class="shrink-0 text-xs font-semibold uppercase tracking-wide"
                  classList={{
                    "text-ok": result.grabbed,
                    "text-accent": result.fallback,
                    "text-danger": !!result.error,
                  }}
                >
                  <Switch>
                    <Match when={result.grabbed}>✓ Grabbed</Match>
                    <Match when={result.fallback}>Needs a pick</Match>
                    <Match when={result.error}>✗ Error</Match>
                  </Switch>
                </span>
              </div>
              <Switch>
                <Match when={result.grabbed}>
                  <Muted>{result.message}</Muted>
                </Match>
                <Match when={result.error}>
                  <ErrorText>{result.error}</ErrorText>
                </Match>
                <Match when={result.fallback}>
                  <FallbackRow item={props.items[result.index]!} result={result} />
                </Match>
              </Switch>
            </li>
          )}
        </For>
      </ul>
    </Modal>
  );
};
