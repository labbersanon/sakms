// BulkBar is Discover select-mode's floating action bar (F3). It appears only
// while at least one card is selected, and offers exactly two actions:
// "Grab all" (build a capped batch from the current selection and POST it once
// to /api/autograb-batch) and "Clear".
//
// The batch it builds comes from selection.buildBatch(), which is where the
// pre-mortem #5 orphan-drop happens: only selected keys whose card is still
// rendered are included, and the payload is pulled live from that card — so a
// stale/wrong tmdbId can never reach the endpoint. If items were dropped, the
// result modal surfaces "N selected, M submitted".

import { type Component, Show, createSignal } from "solid-js";
import {
  type AutoGrabBatchItem,
  type AutoGrabBatchResponse,
  autoGrabBatch,
} from "../../api/grab";
import { useSelection } from "./selection";
import { Button, ErrorText } from "../../components/ui";
import { BulkResultModal } from "./BulkResultModal";

// MAX_BATCH_GRAB_ITEMS mirrors the backend's cap (each item = a live search +
// possible grab, so far below apply-batch's 200). The backend rejects an
// over-cap batch before any Prowlarr search fires; this client-side guard just
// gives a faster, clearer message and never lets an over-cap request leave.
const MAX_BATCH_GRAB_ITEMS = 20;

export const BulkBar: Component = () => {
  const selection = useSelection();
  const [submitting, setSubmitting] = createSignal(false);
  const [error, setError] = createSignal("");
  const [modal, setModal] = createSignal<{
    items: AutoGrabBatchItem[];
    response: AutoGrabBatchResponse;
    selectedCount: number;
  } | null>(null);

  const grabAll = async () => {
    if (!selection) return;
    setError("");
    const batch = selection.buildBatch();
    if (batch.items.length === 0) {
      setError(
        "None of the selected titles are still on screen — nothing to grab.",
      );
      return;
    }
    if (batch.items.length > MAX_BATCH_GRAB_ITEMS) {
      setError(
        `Too many items (${batch.items.length}). Select at most ${MAX_BATCH_GRAB_ITEMS} to grab at once.`,
      );
      return;
    }
    setSubmitting(true);
    try {
      const response = await autoGrabBatch(batch.items);
      setModal({
        items: batch.items,
        response,
        selectedCount: batch.selectedCount,
      });
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Show when={selection && selection.count() > 0}>
      <div class="fixed bottom-4 left-1/2 z-40 flex -translate-x-1/2 flex-col items-center gap-1">
        <div class="flex items-center gap-3 rounded-full border border-border bg-surface px-4 py-2 shadow-2xl">
          <span class="text-sm text-fg">{selection!.count()} selected</span>
          <Button
            variant="primary"
            class="!px-3 !py-1 !text-sm"
            onClick={grabAll}
            disabled={submitting()}
          >
            {submitting() ? "Grabbing…" : "Grab all"}
          </Button>
          <Button class="!px-3 !py-1 !text-sm" onClick={() => selection!.clear()}>
            Clear
          </Button>
        </div>
        <Show when={error()}>
          <div class="rounded-md border border-border bg-surface px-3 py-1 shadow-lg">
            <ErrorText>{error()}</ErrorText>
          </div>
        </Show>
      </div>
      {/* Results do NOT auto-dismiss; closing clears the selection so an
          already-grabbed item can't be re-grabbed by a second "Grab all". */}
      <Show when={modal()}>
        {(m) => (
          <BulkResultModal
            items={m().items}
            response={m().response}
            selectedCount={m().selectedCount}
            onClose={() => {
              setModal(null);
              selection!.clear();
            }}
          />
        )}
      </Show>
    </Show>
  );
};
