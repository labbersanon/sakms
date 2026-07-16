// AdultRowAdmin — the admin UI for creating/editing/reordering/deleting
// admin-defined Adult "newest" Discover rows (Stage 2; the Prowlarr-backed
// sibling of SliderAdmin's TMDB-backed sliders). Lives as its own Settings
// section tab ("Adult Rows"), consuming Stage 2's confirmed CRUD endpoints/DTOs
// (src/api/adultNewestRows.ts). Mirrors SliderAdmin's structure closely, just
// adapted to the simpler field set: no target, and genreFilter is ALWAYS
// optional (there is no required/forbidden pairing rule to enforce).
//
// No bulk actions (project convention): create/update/delete/enable-toggle each
// act on exactly one row. Reordering is button-based (up/down), not
// drag-and-drop — same as SliderAdmin.

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
  reorderAdultNewestRows,
  updateAdultNewestRow,
  type AdultNewestRow,
  type RowType,
} from "../api/adultNewestRows";
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

// AdultRow is one existing row's list entry: position controls, summary, an
// inline enabled toggle (immediate save, no separate form), Edit/Delete.
const AdultRow: Component<{
  row: AdultNewestRow;
  isFirst: boolean;
  isLast: boolean;
  editing: boolean;
  onMoveUp: () => void;
  onMoveDown: () => void;
  onEdit: () => void;
  onDelete: () => void;
  onToggleEnabled: () => void;
}> = (props) => (
  <li class="flex items-center gap-3 border-b border-border/60 py-2">
    <div class="flex flex-col gap-0.5">
      <button
        type="button"
        aria-label={`Move ${props.row.title} up`}
        class="rounded border border-border px-1 text-xs text-fg disabled:opacity-30"
        disabled={props.isFirst}
        onClick={props.onMoveUp}
      >
        ▲
      </button>
      <button
        type="button"
        aria-label={`Move ${props.row.title} down`}
        class="rounded border border-border px-1 text-xs text-fg disabled:opacity-30"
        disabled={props.isLast}
        onClick={props.onMoveDown}
      >
        ▼
      </button>
    </div>
    <div class="min-w-0 flex-1">
      <div class="truncate text-sm text-fg">{props.row.title}</div>
      <div class="truncate text-xs text-muted">
        {ROW_TYPE_LABELS[props.row.rowType as RowType] ?? props.row.rowType}
        {props.row.genreFilter ? ` · ${props.row.genreFilter}` : ""}
      </div>
    </div>
    <label class="flex items-center gap-1 text-xs text-muted">
      <input
        type="checkbox"
        aria-label={`${props.row.title} enabled`}
        checked={props.row.enabled}
        onChange={props.onToggleEnabled}
      />
      enabled
    </label>
    <div class="flex gap-1">
      <Button class="!px-2 !py-1 !text-xs" onClick={props.onEdit}>
        {props.editing ? "Editing…" : "Edit"}
      </Button>
      <Button class="!px-2 !py-1 !text-xs" onClick={props.onDelete}>
        Delete
      </Button>
    </div>
  </li>
);

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

  const move = async (id: number, direction: -1 | 1) => {
    const list = rows() ?? [];
    const idx = list.findIndex((r) => r.id === id);
    const swapWith = idx + direction;
    if (idx < 0 || swapWith < 0 || swapWith >= list.length) return;
    const ids = list.map((r) => r.id);
    [ids[idx], ids[swapWith]] = [ids[swapWith]!, ids[idx]!];
    setListError("");
    try {
      await reorderAdultNewestRows(ids);
      await refetch();
    } catch (e) {
      setListError((e as Error).message);
    }
  };

  const remove = async (row: AdultNewestRow) => {
    if (!confirm(`Delete the "${row.title}" row?`)) return;
    setListError("");
    try {
      await deleteAdultNewestRow(row.id);
      if (editing() === row.id) closeForm();
      await refetch();
    } catch (e) {
      setListError((e as Error).message);
    }
  };

  const toggleEnabled = async (row: AdultNewestRow) => {
    setListError("");
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
          label="Background scan interval"
          help="How often Prowlarr's newest Adult releases are scanned and matched to TPDB/StashDB/FansDB entities to populate these rows."
          value={() => scanInterval()}
          onSave={(v) => putAdultNewestScanInterval(v)}
        />
      </Card>

      <Card title="Adult newest rows">
        <Muted class="mb-3">
          Admin-defined Adult Discover rows sourced from Prowlarr's newest
          releases, matched to TPDB/StashDB/FansDB entities. The four default
          rows (Movie/Scene/Performer/Studio) already exist — add more here,
          e.g. a genre-narrowed variant of an existing type, and control display
          order.
        </Muted>
        <Show when={rows.error}>
          <ErrorText>{(rows.error as Error)?.message}</ErrorText>
        </Show>
        <Show when={listError()}>
          <ErrorText>{listError()}</ErrorText>
        </Show>
        <Show
          when={(rows() ?? []).length > 0}
          fallback={<Muted>No custom rows yet.</Muted>}
        >
          <ul>
            <For each={rows()}>
              {(row, i) => (
                <AdultRow
                  row={row}
                  isFirst={i() === 0}
                  isLast={i() === (rows() ?? []).length - 1}
                  editing={editing() === row.id}
                  onMoveUp={() => void move(row.id, -1)}
                  onMoveDown={() => void move(row.id, 1)}
                  onEdit={() => setEditing(row.id)}
                  onDelete={() => void remove(row)}
                  onToggleEnabled={() => void toggleEnabled(row)}
                />
              )}
            </For>
          </ul>
        </Show>

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
      </Card>
    </>
  );
};
