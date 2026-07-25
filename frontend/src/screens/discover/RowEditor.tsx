// RowEditor — the Discover Edit-mode surface: given the FULL merged, ordered
// row list for one screen (built-in/structural rows + every dynamic entity row
// type — sliders, adult newest rows, rss feeds — already resolved to one flat
// descriptor list by the caller), renders it with drag-and-drop reordering
// (via @thisbeyond/solid-dnd, the one new frontend dependency this feature
// adds) plus per-row controls keyed off each descriptor's `kind`:
//   - "structural" rows (built-in TMDB/stash-box rows, Studios/Performers, the
//     Trakt/library rows) have no backing entity to delete, so they get a
//     Show/Hide toggle whose state persists in the per-screen row-hidden store.
//     A hidden structural row still renders here (dimmed) so it's always
//     re-showable — Edit mode is never a dead end.
//   - "entity" rows (custom sliders, adult newest rows, rss feeds) have their
//     own admin CRUD, so they get an Enabled toggle + Delete (confirm-first),
//     exactly as before.
// Drag is initiated ONLY from the explicit ⠿ grip handle (its dragActivators),
// so the checkbox / Show-Hide / Delete controls stay independently clickable —
// the row body itself is not draggable. Purely presentational — callers own the
// actual row-order/hidden state and persistence (persistOrder / toggleHidden /
// the entity update/delete APIs); RowEditor only reports the computed new order
// (onReorder) and which control was activated.

import { type Component, For, Show } from "solid-js";
import {
  DragDropProvider,
  DragDropSensors,
  SortableProvider,
  closestCenter,
  createSortable,
  transformStyle,
  type DragEvent,
} from "@thisbeyond/solid-dnd";
import { Button, Card, Muted } from "../../components/ui";

export type RowDescriptor = {
  key: string;
  label: string;
  // "structural" rows get a Show/Hide toggle (no Delete); "entity" rows
  // (sliders, adult newest rows, rss feeds) get an Enabled toggle + Delete.
  kind: "structural" | "entity";
  enabled?: boolean; // entity rows only: the entity's own enabled flag
  hidden?: boolean; // structural rows only: current per-screen hidden state
};

// reorderKeys is the pure move computed on drop: relocate fromKey so it occupies
// toKey's slot, shifting the rest. Extracted (not inlined in onDragEnd) so the
// resulting-order logic is unit-testable without simulating a jsdom pointer
// drag. Mirrors solid-dnd's own sortable-list move idiom (splice-out then
// splice-in at the target's original index). A no-op when either key is absent
// or they're identical.
export function reorderKeys(
  keys: string[],
  fromKey: string,
  toKey: string,
): string[] {
  if (fromKey === toKey) return keys;
  const fromIndex = keys.indexOf(fromKey);
  const toIndex = keys.indexOf(toKey);
  if (fromIndex < 0 || toIndex < 0) return keys;
  const next = keys.slice();
  next.splice(toIndex, 0, ...next.splice(fromIndex, 1));
  return next;
}

// RowItem is one sortable <li>. createSortable owns its drag transform; the
// grip <span> carries the dragActivators so ONLY it starts a drag.
const RowItem: Component<{
  row: RowDescriptor;
  onToggleEnabled: (row: RowDescriptor) => void;
  onToggleHidden: (row: RowDescriptor) => void;
  onDelete: (row: RowDescriptor) => void;
}> = (props) => {
  const sortable = createSortable(props.row.key);
  return (
    <li
      ref={sortable.ref}
      style={transformStyle(sortable.transform)}
      class="flex items-center gap-3 border-b border-border/60 py-2"
      classList={{
        "opacity-50":
          props.row.kind === "structural" && (props.row.hidden ?? false),
        "opacity-25": sortable.isActiveDraggable,
      }}
    >
      <span
        {...sortable.dragActivators}
        role="button"
        aria-label={`Drag ${props.row.label}`}
        class="cursor-grab touch-none select-none rounded border border-border px-1.5 text-xs text-muted"
      >
        ⠿
      </span>
      <div class="min-w-0 flex-1 truncate text-sm text-fg">
        {props.row.label}
      </div>
      <Show when={props.row.kind === "entity"}>
        <label class="flex items-center gap-1 text-xs text-muted">
          <input
            type="checkbox"
            aria-label={`${props.row.label} enabled`}
            checked={props.row.enabled ?? true}
            onChange={() => props.onToggleEnabled(props.row)}
          />
          enabled
        </label>
        <Button
          class="!px-2 !py-1 !text-xs"
          onClick={() => props.onDelete(props.row)}
        >
          Delete
        </Button>
      </Show>
      <Show when={props.row.kind === "structural"}>
        <label class="flex items-center gap-1 text-xs text-muted">
          <input
            type="checkbox"
            aria-label={`${props.row.label} visible`}
            checked={!(props.row.hidden ?? false)}
            onChange={() => props.onToggleHidden(props.row)}
          />
          visible
        </label>
      </Show>
    </li>
  );
};

export const RowEditor: Component<{
  rows: RowDescriptor[];
  onReorder: (orderedKeys: string[]) => void;
  onToggleEnabled: (row: RowDescriptor) => void;
  onToggleHidden: (row: RowDescriptor) => void;
  onDelete: (row: RowDescriptor) => void;
}> = (props) => {
  const ids = () => props.rows.map((r) => r.key);
  const onDragEnd = ({ draggable, droppable }: DragEvent) => {
    if (!droppable) return;
    props.onReorder(
      reorderKeys(ids(), String(draggable.id), String(droppable.id)),
    );
  };
  return (
    <Card title="Reorder rows">
      <Muted class="mb-3">
        Drag the ⠿ handle to reorder. Built-in rows can be shown or hidden;
        dynamic rows (custom sliders, RSS feeds, and Adult newest rows) can also
        be disabled or deleted here.
      </Muted>
      <Show when={props.rows.length > 0} fallback={<Muted>No rows yet.</Muted>}>
        <DragDropProvider onDragEnd={onDragEnd} collisionDetector={closestCenter}>
          <DragDropSensors />
          <SortableProvider ids={ids()}>
            <ul>
              <For each={props.rows}>
                {(row) => (
                  <RowItem
                    row={row}
                    onToggleEnabled={props.onToggleEnabled}
                    onToggleHidden={props.onToggleHidden}
                    onDelete={props.onDelete}
                  />
                )}
              </For>
            </ul>
          </SortableProvider>
        </DragDropProvider>
      </Show>
    </Card>
  );
};
