// AdultRowAdmin — the admin UI for creating/editing/reordering/deleting
// admin-defined Adult "newest" Discover rows (Stage 2; the Prowlarr-backed
// sibling of SliderAdmin's TMDB-backed sliders). Lives as its own Settings
// section tab ("Adult Rows"), consuming Stage 2's confirmed CRUD endpoints/DTOs
// (src/api/adultNewestRows.ts). Mirrors SliderAdmin's structure closely, just
// adapted to the simpler field set: no target, and genreFilter is ALWAYS
// optional (there is no required/forbidden pairing rule to enforce).
//
// No bulk actions (project convention): create/update/delete/enable-toggle each
// act on exactly one row. (Still holds HERE — Adult-row CRUD is genuinely
// single-item. Not absolute app-wide: two bounded, documented bulk exceptions
// exist elsewhere — bulk-apply on Rename/Dedup/Purge, and bulk-grab in
// Discover's opt-in Select mode — neither touching this screen.)
//
// Reordering is DRAG-AND-DROP, via Discover's own <RowEditor> rendered
// literally (not reimplemented, not extracted into a shared helper). This
// screen and Discover's Adult row editor are now two views of ONE order —
// adultnewest's `sort_order` column, POST /api/modes/adult/newest-rows/reorder,
// shared through useAdultRowOrder — so an order set on either is what the other
// shows. The header used to say reordering was button-based "same as
// SliderAdmin"; that comparison is TEMPORARILY FALSE BY DESIGN, not stale by
// accident: SliderAdmin (and RssFeedAdmin) still use ▲▼ buttons, and converting
// them is the sibling row-dnd-consolidation-and-pagination spec's job, not
// this one's. It becomes true again — in the opposite direction — once that
// lands.

import {
  type Component,
  createResource,
  createSignal,
  For,
  Show,
} from "solid-js";
import {
  ROW_TYPES,
  ROW_TYPE_LABELS,
  createAdultNewestRow,
  deleteAdultNewestRow,
  fetchAdultNewestGenres,
  fetchAdultNewestRows,
  updateAdultNewestRow,
  type AdultNewestRow,
  type RowType,
} from "../api/adultNewestRows";
import { RowEditor, type RowDescriptor } from "./discover/RowEditor";
import { useAdultRowOrder } from "./discover/useAdultRowOrder";
import {
  fetchAdultNewestScanInterval,
  putAdultNewestScanInterval,
} from "../api/settings";
import {
  Button,
  Card,
  ErrorText,
  Muted,
  SaveStatus,
  inputClass,
  labelClass,
  useSaveStatus,
} from "../components/ui";
import { DurationSetting } from "./settings/Advanced";

// AdultRowForm creates a new row (row prop undefined) or edits an existing one
// (row prop present) — the same form either way, seeded from props.row at mount
// (same pattern as SliderForm).
const AdultRowForm: Component<{
  row?: AdultNewestRow;
  onSaved: () => void;
  onCancel: () => void;
}> = (props) => {
  const [title, setTitle] = createSignal(props.row?.title ?? "");
  const [rowType, setRowType] = createSignal<RowType>(
    (props.row?.rowType as RowType) ?? "movie",
  );
  const [genreFilter, setGenreFilter] = createSignal(
    props.row?.genreFilter ?? "",
  );
  const [enabled, setEnabled] = createSignal(props.row?.enabled ?? true);
  const status = useSaveStatus();

  // The genre list is sourced from whatever's actually been matched so far, so
  // it can legitimately be empty on a fresh install before the background scan
  // has run — fetched once on form open.
  const [genres] = createResource(fetchAdultNewestGenres, { initialValue: [] });

  const save = async () => {
    if (!title().trim()) {
      status.failed(new Error("title is required"));
      return;
    }
    const trimmedGenre = genreFilter().trim();
    const body = {
      title: title().trim(),
      rowType: rowType(),
      genreFilter: trimmedGenre || undefined,
      enabled: enabled(),
    };
    try {
      if (props.row) {
        await updateAdultNewestRow(props.row.id, body);
      } else {
        await createAdultNewestRow(body);
      }
      props.onSaved();
    } catch (e) {
      status.failed(e);
    }
  };

  return (
    <div class="mt-3 rounded-md border border-dashed border-border p-3">
      <form onSubmit={(e) => (e.preventDefault(), void save())}>
        <label class="mb-2 block">
          <span class={labelClass}>Title</span>
          <input
            type="text"
            class={`${inputClass} mt-1`}
            aria-label="Row title"
            value={title()}
            onInput={(e) => setTitle(e.currentTarget.value)}
          />
        </label>
        <div class="grid gap-3 sm:grid-cols-2">
          <label class="block">
            <span class={labelClass}>Row type</span>
            <select
              class={`${inputClass} mt-1`}
              aria-label="Row type"
              value={rowType()}
              onChange={(e) => setRowType(e.currentTarget.value as RowType)}
            >
              <For each={ROW_TYPES}>
                {(rt) => <option value={rt}>{ROW_TYPE_LABELS[rt]}</option>}
              </For>
            </select>
          </label>
          <label class="block">
            <span class={labelClass}>Genre filter</span>
            <Show
              when={(genres() ?? []).length > 0}
              fallback={
                <>
                  <select
                    class={`${inputClass} mt-1`}
                    aria-label="Genre filter"
                    disabled
                  >
                    <option>No genre filter</option>
                  </select>
                  <Muted class="mt-1">
                    No genres available yet — the background scan needs to run
                    first.
                  </Muted>
                </>
              }
            >
              <select
                class={`${inputClass} mt-1`}
                aria-label="Genre filter"
                value={genreFilter()}
                onChange={(e) => setGenreFilter(e.currentTarget.value)}
              >
                <option value="">No genre filter</option>
                <For each={genres()}>
                  {(g) => <option value={g}>{g}</option>}
                </For>
              </select>
            </Show>
          </label>
        </div>
        <label class="mb-2 mt-2 flex items-center gap-2">
          <input
            type="checkbox"
            aria-label="Row enabled"
            checked={enabled()}
            onChange={(e) => setEnabled(e.currentTarget.checked)}
          />
          <span class="text-sm text-fg">Enabled</span>
        </label>
        <div class="mt-2 flex items-center gap-2">
          <Button variant="primary" type="submit">
            {props.row ? "Save changes" : "Create row"}
          </Button>
          <Button onClick={props.onCancel}>Cancel</Button>
          <SaveStatus text={status.status().text} error={status.status().error} />
        </div>
      </form>
    </div>
  );
};

// AdultRowAdminSection is the Settings "Adult Rows" tab's whole panel.
export const AdultRowAdminSection: Component = () => {
  const [rows, { refetch }] = createResource(fetchAdultNewestRows, {
    initialValue: [],
  });
  const [scanInterval] = createResource(fetchAdultNewestScanInterval);
  const [editing, setEditing] = createSignal<number | "new" | null>(null);
  const [listError, setListError] = createSignal("");

  const closeForm = () => setEditing(null);
  const afterSave = () => {
    closeForm();
    void refetch();
  };

  const {
    orderedRows,
    persistOrder,
    error: reorderError,
    clearError: clearReorderError,
  } = useAdultRowOrder(rows, () => void refetch());

  // A reorder failure (notably Store.Reorder's 400 when the id set no longer
  // matches — reachable when this tab and Discover's row editor are both open
  // and one deletes a row) lands on the hook's own error signal, not on
  // listError. Both render through this one accessor so neither failure mode
  // can go silent.
  // Each mutation clears the OTHER signal on entry (remove/toggleEnabled clear
  // reorderError, the onReorder call site clears listError), so at most one is
  // ever set and only the LATEST failure shows. Without that, a stale delete
  // error sat on the left of the || and permanently masked every later reorder
  // failure. The || order is therefore decorative, not a precedence rule — it
  // matches Adult.tsx's editError so the two surfaces read identically.
  const displayError = () => reorderError() || listError();

  const rowForKey = (key: string): AdultNewestRow | undefined => {
    const id = Number(key.slice("newestrow:".length));
    return orderedRows().find((r) => r.id === id);
  };

  // Title-only body: the row-type/genre subtitle the old list entry carried is
  // deliberately dropped, matching Discover's plain rows. Edit is carried as a
  // RowAction because RowEditor's built-in controls are enabled-toggle +
  // Delete only — this screen is the app's ONLY place to edit a row's
  // title/rowType/genreFilter, so it cannot be lost. The glyph is a deliberate
  // placeholder; the sibling row-dnd spec swaps in a real icon library, which
  // is a call-site-only change because RowAction.icon is typed JSX.Element.
  const descriptors = (): RowDescriptor[] =>
    orderedRows().map((row) => ({
      key: `newestrow:${row.id}`,
      label: row.title,
      kind: "entity" as const,
      enabled: row.enabled,
      actions: [
        {
          label: `Edit ${row.title}`,
          icon: <span aria-hidden>✎</span>,
          onClick: () => setEditing(row.id),
        },
      ],
    }));

  const remove = async (descriptor: RowDescriptor) => {
    const row = rowForKey(descriptor.key);
    if (!row) return;
    if (!confirm(`Delete the "${row.title}" row?`)) return;
    setListError("");
    clearReorderError();
    try {
      await deleteAdultNewestRow(row.id);
      if (editing() === row.id) closeForm();
      await refetch();
    } catch (e) {
      setListError((e as Error).message);
    }
  };

  const toggleEnabled = async (descriptor: RowDescriptor) => {
    const row = rowForKey(descriptor.key);
    if (!row) return;
    setListError("");
    clearReorderError();
    try {
      await updateAdultNewestRow(row.id, {
        title: row.title,
        rowType: row.rowType,
        genreFilter: row.genreFilter,
        enabled: !row.enabled,
      });
      await refetch();
    } catch (e) {
      setListError((e as Error).message);
    }
  };

  const editingRow = (): AdultNewestRow | undefined => {
    const e = editing();
    if (e === null || e === "new") return undefined;
    return (rows() ?? []).find((r) => r.id === e);
  };

  return (
    <>
      <Card title="Adult newest rows — background scan">
        <DurationSetting
          id="adult-newest-scan-interval"
          label="Background scan interval"
          help="How often Prowlarr's newest Adult releases are scanned and matched to TPDB/StashDB/FansDB entities to populate these rows."
          value={() => scanInterval()}
          onSave={(v) => putAdultNewestScanInterval(v)}
        />
      </Card>

      {/* RowEditor renders its own <Card>, so this is NOT wrapped in another
          one. title/description/footer exist precisely so a Settings host can
          supply its own heading, copy + error lines, and create affordance
          without nesting Cards or inheriting Discover's copy. */}
      <RowEditor
        title="Adult newest rows"
        description={
          <>
            <Muted class="mb-3">
              Admin-defined Adult Discover rows sourced from Prowlarr's newest
              releases, matched to TPDB/StashDB/FansDB entities. The four
              default rows (Movie/Scene/Performer/Studio) already exist — add
              more here, e.g. a genre-narrowed variant of an existing type.
              Drag the ⠿ handle to reorder; this is the same order Discover
              shows, so a change here is a change there.
            </Muted>
            <Show when={rows.error}>
              <ErrorText>{(rows.error as Error)?.message}</ErrorText>
            </Show>
            <Show when={displayError()}>
              <ErrorText>{displayError()}</ErrorText>
            </Show>
          </>
        }
        footer={
          <Show
            when={editing() !== null}
            fallback={
              <div class="mt-3">
                <Button variant="primary" onClick={() => setEditing("new")}>
                  + New row
                </Button>
              </div>
            }
          >
            <AdultRowForm
              row={editingRow()}
              onSaved={afterSave}
              onCancel={closeForm}
            />
          </Show>
        }
        rows={descriptors()}
        onReorder={(keys) => {
          // Clear the other failure surface first: only the LATEST failure
          // should ever be visible (see displayError above).
          setListError("");
          void persistOrder(
            keys.map((k) => Number(k.slice("newestrow:".length))),
          );
        }}
        onToggleEnabled={(r) => void toggleEnabled(r)}
        onDelete={(r) => void remove(r)}
      />
    </>
  );
};
