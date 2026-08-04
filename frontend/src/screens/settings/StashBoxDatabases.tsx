// StashBoxDatabases — the Settings "Stash-box databases" panel (Library ->
// Adult), Stage 5 Wave 4 (plan
// .omc/plans/autopilot-impl-stage5-stashboxdb-ui.md §4). Same RowEditor +
// form idiom as RssFeedAdmin.tsx: rows are title-only, drag persists
// immediately (AC9), a footer "+ Add database" button swaps for an inline
// create form and disappears at the 5-database cap with a note (AC7), and
// confirm-first delete (AC8). AC11: every row — StashDB/FansDB (pre-seeded)
// or operator-added — renders through the exact same descriptor mapping
// below, with zero special-casing by name; TPDB never appears here at all
// (it stays a plain ConnectionRow in "Metadata sources (Adult)").
//
// Red tint (AC6/AC12) ports ConnectionServiceTable's shared `failing` map
// convention: every row holding a stored key is auto-tested as soon as the
// list resolves (and again after any save, since a refetch re-resolves it),
// PLUS a per-row manual Test action re-runs the same check — both converge on
// the one `failing` signal, which RowDescriptor.danger turns into the row's
// only failure signal (a CSS tint on the <li> — see RowEditor.tsx; no
// separate status icon or text, since the stored-test endpoint deliberately
// returns no diagnostic detail to echo).
//
// Persistence is this panel's OWN — every mutation (create/update/reorder/
// delete/enable-toggle) fires immediately, same as RssFeedAdmin — NOT the
// Library tab's SectionSave batch. See settings/index.tsx's mount-site
// comment for why it must stay outside that batch.

import {
  type Component,
  createEffect,
  createResource,
  createSignal,
  on,
  Show,
} from "solid-js";
import Pencil from "lucide-solid/icons/pencil";
import PlugZap from "lucide-solid/icons/plug-zap";
import {
  buildStashBoxDatabaseKeyPatch,
  createStashBoxDatabase,
  deleteStashBoxDatabase,
  fetchStashBoxDatabases,
  reorderStashBoxDatabases,
  testStashBoxDatabaseStored,
  updateStashBoxDatabase,
  type StashBoxDatabase,
} from "../../api/stashboxdb";
import {
  Button,
  ErrorText,
  Muted,
  SaveStatus,
  inputClass,
  labelClass,
  useSaveStatus,
} from "../../components/ui";
import { RowEditor, type RowDescriptor } from "../discover/RowEditor";

// MAX_DATABASES mirrors the server-side cap (stashboxdb.MaxDatabases) purely
// for the footer's disabled-state note — Create still enforces the real cap
// itself (AC16); this is presentational only, never load-bearing.
const MAX_DATABASES = 5;

// StashBoxDatabaseForm creates a new database (db prop undefined) or edits an
// existing one (db prop present). Unlike the retired stashdb/fansdb
// ConnectionRows, every row here has a real, operator-visible endpoint — there
// is no fixed-URL tier. The API key input starts blank on edit; a blank,
// untouched value on save preserves the stored (masked) key via
// buildStashBoxDatabaseKeyPatch's three-state gate (Guardrail #5).
// Fansite-only is edit-only: Create has no such parameter server-side
// (stashboxdb.Store.Create always seeds fansite_only=0) — only Update can set
// it, so a new database always starts with the gate off, editable afterward.
const StashBoxDatabaseForm: Component<{
  db?: StashBoxDatabase;
  onSave: (v: {
    name: string;
    endpoint: string;
    apiKey: string;
    keyTouched: boolean;
    fansiteOnly: boolean;
  }) => Promise<void>;
  onCancel: () => void;
}> = (props) => {
  const [name, setName] = createSignal(props.db?.name ?? "");
  const [endpoint, setEndpoint] = createSignal(props.db?.endpoint ?? "");
  const [apiKey, setApiKey] = createSignal("");
  const [keyTouched, setKeyTouched] = createSignal(false);
  const [fansiteOnly, setFansiteOnly] = createSignal(
    props.db?.fansiteOnly ?? false,
  );
  const status = useSaveStatus();

  const keyPlaceholder = () =>
    props.db?.hasApiKey
      ? `unchanged (••••${props.db.keySuffix ?? ""})`
      : "API key";

  const save = async () => {
    if (!name().trim()) {
      status.failed(new Error("name is required"));
      return;
    }
    if (!endpoint().trim()) {
      status.failed(new Error("endpoint is required"));
      return;
    }
    if (!props.db && !apiKey().trim()) {
      status.failed(new Error("an API key is required"));
      return;
    }
    try {
      await props.onSave({
        name: name().trim(),
        endpoint: endpoint().trim(),
        apiKey: apiKey(),
        keyTouched: keyTouched(),
        fansiteOnly: fansiteOnly(),
      });
    } catch (e) {
      status.failed(e);
    }
  };

  return (
    <div class="mt-3 rounded-md border border-dashed border-border p-3">
      <form onSubmit={(e) => (e.preventDefault(), void save())}>
        <label class="mb-2 block">
          <span class={labelClass}>Name</span>
          <input
            type="text"
            class={`${inputClass} mt-1`}
            aria-label="Database name"
            value={name()}
            onInput={(e) => setName(e.currentTarget.value)}
          />
        </label>
        <label class="mb-2 block">
          <span class={labelClass}>Endpoint</span>
          <input
            type="text"
            class={`${inputClass} mt-1`}
            aria-label="Database endpoint"
            placeholder="https://example.org/graphql"
            value={endpoint()}
            onInput={(e) => setEndpoint(e.currentTarget.value)}
          />
        </label>
        <label class="mb-2 block">
          <span class={labelClass}>API key</span>
          <input
            type="password"
            class={`${inputClass} mt-1`}
            aria-label="Database API key"
            placeholder={keyPlaceholder()}
            value={apiKey()}
            onInput={(e) => {
              setApiKey(e.currentTarget.value);
              setKeyTouched(true);
            }}
          />
        </label>
        <Show when={props.db}>
          <label class="flex items-center gap-2 text-xs text-muted">
            <input
              type="checkbox"
              aria-label="Fansite-hinted filenames only"
              checked={fansiteOnly()}
              onChange={(e) => setFansiteOnly(e.currentTarget.checked)}
            />
            Only consult this database for fansite-hinted filenames
          </label>
        </Show>
        <div class="mt-3 flex items-center gap-2">
          <Button variant="primary" type="submit">
            {props.db ? "Save changes" : "Add database"}
          </Button>
          <Button onClick={props.onCancel}>Cancel</Button>
          <SaveStatus text={status.status().text} error={status.status().error} />
        </div>
      </form>
    </div>
  );
};

// StashBoxDatabases is the whole panel — mounted as its own Card-equivalent
// (RowEditor renders the Card chrome itself) inside Settings -> Library ->
// Adult, adjacent to LibraryConnectionsSection.
export const StashBoxDatabases: Component = () => {
  const [dbs, { refetch }] = createResource(fetchStashBoxDatabases, {
    initialValue: [],
  });
  const [editing, setEditing] = createSignal<number | "new" | null>(null);
  const [listError, setListError] = createSignal("");
  // failing maps id -> true when its most recent test-stored (auto or manual
  // per-row Test) failed. Same shared-map convention as
  // ConnectionServiceTable's `failing` — one source of truth feeding the
  // row's red tint (RowDescriptor.danger); there is no separate status text.
  const [failing, setFailing] = createSignal<Record<number, boolean>>({});

  // Auto-test every row with a stored key whenever the list (re)resolves —
  // mirrors ConnectionServiceTable's createEffect exactly (AC6): on first
  // load AND after any save (create/update/reorder/delete all refetch, which
  // re-resolves dbs() and re-runs this). A row with no stored key is left
  // untested and untinted — "not configured yet" is not a failure. Fire-and-
  // forget per row; a thrown/transport error leaves the prior value rather
  // than tinting, so a backend hiccup doesn't red-tint every row at once.
  createEffect(
    on(dbs, (list) => {
      if (!list) return;
      for (const db of list) {
        if (!db.hasApiKey) continue;
        void testStashBoxDatabaseStored(db.id)
          .then((r) => setFailing((prev) => ({ ...prev, [db.id]: !r.ok })))
          .catch(() => {});
      }
    }),
  );

  const closeForm = () => setEditing(null);
  const editingDb = (): StashBoxDatabase | undefined => {
    const e = editing();
    if (e === null || e === "new") return undefined;
    return (dbs() ?? []).find((d) => d.id === e);
  };

  // submitForm handles both the add and edit submits from StashBoxDatabaseForm.
  // Edit sends only name/endpoint/fansiteOnly plus the three-state key patch;
  // priority/enabled are never touched here (Reorder and the row's own toggle
  // own those). Create sends the plain-string apiKey the backend requires.
  const submitForm = async (v: {
    name: string;
    endpoint: string;
    apiKey: string;
    keyTouched: boolean;
    fansiteOnly: boolean;
  }) => {
    setListError("");
    const db = editingDb();
    if (db) {
      const keyPatch = buildStashBoxDatabaseKeyPatch({
        keyTouched: v.keyTouched,
        keyValue: v.apiKey,
        hasExistingKey: db.hasApiKey,
      });
      await updateStashBoxDatabase(db.id, {
        name: v.name,
        endpoint: v.endpoint,
        fansiteOnly: v.fansiteOnly,
        ...keyPatch,
      });
    } else {
      await createStashBoxDatabase({
        name: v.name,
        endpoint: v.endpoint,
        apiKey: v.apiKey,
      });
    }
    closeForm();
    await refetch();
  };

  const toggleEnabled = async (db: StashBoxDatabase) => {
    setListError("");
    try {
      await updateStashBoxDatabase(db.id, { enabled: !db.enabled });
      await refetch();
    } catch (e) {
      setListError((e as Error).message);
    }
  };

  const remove = async (db: StashBoxDatabase) => {
    if (!confirm(`Delete the "${db.name}" database?`)) return;
    setListError("");
    try {
      await deleteStashBoxDatabase(db.id);
      if (editing() === db.id) closeForm();
      await refetch();
    } catch (e) {
      setListError((e as Error).message);
    }
  };

  // test is the per-row manual Test action, using the SAME stored-key
  // resolve path as the auto-test-all effect above, feeding the SAME shared
  // failing map (so a manual pass clears a prior auto-test failure and vice
  // versa) — mirrors ConnectionRow's onManualTestResult convergence.
  const test = async (db: StashBoxDatabase) => {
    setListError("");
    try {
      const r = await testStashBoxDatabaseStored(db.id);
      setFailing((prev) => ({ ...prev, [db.id]: !r.ok }));
    } catch (e) {
      setListError((e as Error).message);
    }
  };

  const persistReorder = async (ids: number[]) => {
    setListError("");
    try {
      await reorderStashBoxDatabases(ids);
      await refetch();
    } catch (e) {
      setListError((e as Error).message);
    }
  };

  const dbByKey = (key: string): StashBoxDatabase | undefined =>
    (dbs() ?? []).find((d) => String(d.id) === key);

  const atCap = () => (dbs() ?? []).length >= MAX_DATABASES;

  // AC11: no distinction whatsoever between a seeded row (StashDB/FansDB) and
  // an operator-added one — every row maps through this same descriptor.
  const descriptors = (): RowDescriptor[] =>
    (dbs() ?? []).map((db) => ({
      key: String(db.id),
      label: db.name,
      kind: "entity",
      enabled: db.enabled,
      danger: failing()[db.id] === true,
      actions: [
        {
          label: `Edit ${db.name}`,
          icon: <Pencil size={14} />,
          onClick: () => setEditing(db.id),
        },
        {
          label: `Test ${db.name}`,
          icon: <PlugZap size={14} />,
          onClick: () => void test(db),
        },
      ],
    }));

  return (
    <RowEditor
      title="Stash-box databases"
      description={
        <>
          <Muted class="mb-3">
            Up to {MAX_DATABASES} stash-box-protocol databases consulted
            during identification, in the order listed (drag to reorder).
            StashDB and FansDB are pre-configured and fully editable, exactly
            like a database you add yourself.
          </Muted>
          <Show when={dbs.error}>
            <ErrorText>{(dbs.error as Error)?.message}</ErrorText>
          </Show>
          <Show when={listError()}>
            <ErrorText>{listError()}</ErrorText>
          </Show>
        </>
      }
      footer={
        <Show
          when={editing() !== null}
          fallback={
            <div class="mt-3">
              <Show
                when={!atCap()}
                fallback={
                  <Muted>
                    {MAX_DATABASES} of {MAX_DATABASES} databases configured
                  </Muted>
                }
              >
                <Button variant="primary" onClick={() => setEditing("new")}>
                  + Add database
                </Button>
              </Show>
            </div>
          }
        >
          <StashBoxDatabaseForm
            db={editingDb()}
            onSave={submitForm}
            onCancel={closeForm}
          />
        </Show>
      }
      rows={descriptors()}
      onReorder={(keys) => void persistReorder(keys.map(Number))}
      onToggleEnabled={(r) => {
        const db = dbByKey(r.key);
        if (db) void toggleEnabled(db);
      }}
      onDelete={(r) => {
        const db = dbByKey(r.key);
        if (db) void remove(db);
      }}
    />
  );
};
