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
//
// It is no longer Discover-only: Settings' AdultRowAdmin renders it too, which
// is what the optional title/description/footer props and the optional
// onToggleHidden exist for — a host with no structural rows passes no hidden
// handler and gets no visible checkbox, and supplies its own Card heading,
// copy/error lines, and create affordance instead of Discover's.

import { type Component, type JSX, For, Show } from "solid-js";
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

// RowAction is one extra per-row control a caller can inject between the row
// label and the enabled/visible cluster — e.g. AdultRowAdmin's Edit affordance,
// which has no equivalent in Discover and would otherwise be lost when that
// screen adopts RowEditor. `icon` is JSX (not a string) so swapping today's
// unicode glyph placeholders for a real icon library is a call-site-only
// change with zero type churn.
export type RowAction = {
  label: string;
  icon: JSX.Element;
  onClick: (row: RowDescriptor) => void;
};

export type RowDescriptor = {
  key: string;
  label: string;
  // "structural" rows get a Show/Hide toggle (no Delete); "entity" rows
  // (sliders, adult newest rows, rss feeds) get an Enabled toggle + Delete.
  kind: "structural" | "entity";
  enabled?: boolean; // entity rows only: the entity's own enabled flag
  hidden?: boolean; // structural rows only: current per-screen hidden state
  actions?: RowAction[];
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
  onToggleHidden?: (row: RowDescriptor) => void;
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
      <For each={props.row.actions ?? []}>
        {(action) => (
          <button
            type="button"
            aria-label={action.label}
            class="rounded border border-border px-1.5 py-0.5 text-xs text-fg"
            onClick={() => action.onClick(props.row)}
          >
            {action.icon}
          </button>
        )}
      </For>
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
      {/* The visible checkbox needs BOTH a structural row and a handler: an
          always-rendered checkbox with an optional handler would be a silently
          inert control on a screen that passes none (D2). */}
      <Show when={props.row.kind === "structural" && props.onToggleHidden}>
        <label class="flex items-center gap-1 text-xs text-muted">
          <input
            type="checkbox"
            aria-label={`${props.row.label} visible`}
            checked={!(props.row.hidden ?? false)}
            onChange={() => props.onToggleHidden?.(props.row)}
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
  // Optional (D2): a screen with no structural rows passes none, and the
  // visible checkbox is then not rendered at all.
  onToggleHidden?: (row: RowDescriptor) => void;
  onDelete: (row: RowDescriptor) => void;
  // title/description/footer let a non-Discover host (Settings' AdultRowAdmin,
  // and later SliderAdmin/RssFeedAdmin) supply its own Card heading, its own
  // explanatory copy + error lines, and its own create affordance, instead of
  // nesting this Card inside another one with Discover-specific copy.
  title?: string;
  description?: JSX.Element;
  footer?: JSX.Element;
}> = (props) => {
  const ids = () => props.rows.map((r) => r.key);
  const onDragEnd = ({ draggable, droppable }: DragEvent) => {
    if (!droppable) return;
    props.onReorder(
      reorderKeys(ids(), String(draggable.id), String(droppable.id)),
    );
  };
  return (
    <Card title={props.title ?? "Reorder rows"}>
      {props.description ?? (
        <Muted class="mb-3">
          Drag the ⠿ handle to reorder. Built-in rows can be shown or hidden;
          dynamic rows (custom sliders, RSS feeds, and Adult newest rows) can
          also be disabled or deleted here.
        </Muted>
      )}
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
      {/* footer is a SIBLING of the empty-state <Show>, never inside it: a host
          that puts its "+ New row" button here must still render it when the
          list is empty, or a fresh install can never create its first row. */}
      {props.footer}
    </Card>
  );
};
