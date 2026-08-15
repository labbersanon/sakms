// API Connections — the global, mode-independent service connections, rendered
// under Settings -> Advanced (above the mode selector, since none of these vary
// by mode).
//
// Named "API Connections", NOT "API": an APIAccessSection already exists on the
// Auth tab for the break-glass X-Api-Key, and two things called "API" in
// Settings would be genuinely confusing. That one keeps its name and its home.
//
// This section is where the old Connections tab's Prowlarr and Stash rows
// landed, plus OMDb (IMDb/RT scores for the detail popup — not mode-specific).
// Stash is here rather than next to StashDB/FansDB/TPDB in Library ->
// Adult despite the obvious functional adjacency — that was an individually
// confirmed operator placement decision, not an oversight. Do not "improve" the
// grouping by moving it.

import {
  type Component,
  For,
  Show,
  createResource,
  createSignal,
} from "solid-js";
import {
  ADULT_ONLY_CONNECTION_SERVICES,
  API_SECTION_SERVICES,
} from "../../api/settings";
import {
  PLAYER_MODES,
  PLAYER_PROVIDERS,
  buildServiceConnectionBody,
  createServiceConnection,
  deleteServiceConnection,
  fetchServiceConnections,
  sameModes,
  setServiceConnectionModes,
  testServiceConnection,
  testStoredServiceConnection,
  updateServiceConnection,
  type PlayerProvider,
  type ServiceConnectionSummary,
} from "../../api/serviceConnections";
import {
  Button,
  ErrorText,
  Muted,
  inputClass,
  labelClass,
  useAdultEnabled,
} from "../../components/ui";
import {
  MODE_LABELS,
  Card,
  SaveStatus,
  SectionSave,
  useSaveStatus,
  useSectionSaveItem,
} from "./shared";
import { ConnectionServiceTable } from "./ConnectionRow";

export const APISection: Component = () => {
  const adultEnabled = useAdultEnabled();
  // Stash is Adult-only (phash-first identification), so it keeps the same
  // global adult_mode_enabled visibility rule it had under the Connections
  // table's ADULT_ONLY_CONNECTION_SERVICES filter — reusing that same constant
  // rather than re-stating "stash" here, so the two can't drift. Without this,
  // Stash would become visible with Adult mode off purely as a side effect of
  // moving to a mode-independent section: a silent behaviour change, not a
  // decision. Kept as a FUNCTION called inline below so it stays reactive to
  // adultEnabled() resolving after mount, rather than freezing at whatever this
  // one synchronous render pass read.
  const services = () =>
    API_SECTION_SERVICES.filter(
      (s) => adultEnabled() || !ADULT_ONLY_CONNECTION_SERVICES.includes(s),
    );

  return (
    <>
      {/* Own SectionSave, inside the Card — the same shape AdvancedSection uses.
          Nothing else on the Advanced tab would batch these rows, and without a
          SectionSave in scope each ConnectionRow falls back to rendering its own
          per-row Save button. */}
      <Card title="API Connections">
        <SectionSave>
          <ConnectionServiceTable services={services} />
        </SectionSave>
      </Card>
      <MediaPlayersCard />
    </>
  );
};

// PlayerDraft is one media player's editable field set. Provider is a FIXED
// three-item enum (Jellyfin | Emby | Plex), never a freeform type field — a
// generic "add any connection type" framework is an explicit non-goal.
interface PlayerDraft {
  label: string;
  provider: PlayerProvider;
  url: string;
  secret: string;
  enabled: boolean;
}

// PlayerFields renders the field set shared by the edit row and the Add form so
// the two can't drift. ariaPrefix keeps several simultaneously-rendered rows
// distinguishable in the accessibility tree.
const PlayerFields: Component<{
  draft: () => PlayerDraft;
  onChange: (patch: Partial<PlayerDraft>) => void;
  secretPlaceholder: string;
  ariaPrefix: string;
}> = (props) => (
  <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
    <label class="block">
      <span class={labelClass}>Label</span>
      <input
        type="text"
        class={inputClass}
        placeholder="e.g. Living room"
        aria-label={`${props.ariaPrefix} label`}
        value={props.draft().label}
        onInput={(e) => props.onChange({ label: e.currentTarget.value })}
      />
    </label>
    <label class="block">
      <span class={labelClass}>Provider</span>
      <select
        class={inputClass}
        aria-label={`${props.ariaPrefix} provider`}
        value={props.draft().provider}
        onChange={(e) =>
          props.onChange({ provider: e.currentTarget.value as PlayerProvider })
        }
      >
        <For each={PLAYER_PROVIDERS}>
          {(p) => <option value={p.value}>{p.label}</option>}
        </For>
      </select>
    </label>
    <label class="block">
      <span class={labelClass}>URL</span>
      <input
        type="text"
        class={inputClass}
        placeholder="https://..."
        aria-label={`${props.ariaPrefix} URL`}
        value={props.draft().url}
        onInput={(e) => props.onChange({ url: e.currentTarget.value })}
      />
    </label>
    <label class="block">
      <span class={labelClass}>API key / token</span>
      <input
        type="password"
        class={inputClass}
        placeholder={props.secretPlaceholder}
        aria-label={`${props.ariaPrefix} API key`}
        value={props.draft().secret}
        onInput={(e) => props.onChange({ secret: e.currentTarget.value })}
      />
    </label>
    <label class="flex items-center gap-2">
      <input
        type="checkbox"
        aria-label={`${props.ariaPrefix} enabled`}
        checked={props.draft().enabled}
        onChange={(e) => props.onChange({ enabled: e.currentTarget.checked })}
      />
      <span class="text-sm text-fg">Enabled</span>
    </label>
  </div>
);

// ModeCheckboxes is the per-player Movies/Series/Adult assignment. Many-to-many
// with NO exclusivity enforcement: 0, 1 or many players per mode are all valid,
// so nothing here warns that a mode "already has" a player.
//
// The Adult box is hidden while the global adult_mode_enabled switch is off
// (same visibility rule the rest of this section follows), but an assignment
// already stored for Adult is left untouched in the modes array rather than
// silently dropped — hiding a control must not unassign anything.
const ModeCheckboxes: Component<{
  modes: () => string[];
  onToggle: (mode: string, on: boolean) => void;
  ariaPrefix: string;
}> = (props) => {
  const adultEnabled = useAdultEnabled();
  const visible = () =>
    PLAYER_MODES.filter((m) => m !== "adult" || adultEnabled());
  return (
    <div class="mt-3">
      <span class={labelClass}>Notify for</span>
      <div class="mt-1 flex flex-wrap gap-4">
        <For each={visible()}>
          {(m) => (
            <label class="flex items-center gap-2">
              <input
                type="checkbox"
                aria-label={`${props.ariaPrefix} ${MODE_LABELS[m]}`}
                checked={props.modes().includes(m)}
                onChange={(e) => props.onToggle(m, e.currentTarget.checked)}
              />
              <span class="text-sm text-fg">{MODE_LABELS[m]}</span>
            </label>
          )}
        </For>
      </div>
    </div>
  );
};

// PlayerRow is one saved player's always-editable controls. secretTouched
// tracks whether the operator actually typed a key — the input is blank for a
// configured player (the stored secret is never sent back), so an untouched
// blank MUST be omitted from the body rather than persisted as "", which the
// backend reads as "clear it".
const PlayerRow: Component<{
  conn: ServiceConnectionSummary;
  onChanged: () => void;
}> = (props) => {
  const [draft, setDraft] = createSignal<PlayerDraft>({
    label: props.conn.label ?? "",
    provider: props.conn.provider as PlayerProvider,
    url: props.conn.url ?? "",
    secret: "",
    enabled: props.conn.enabled,
  });
  const [modes, setModes] = createSignal<string[]>([...(props.conn.modes ?? [])]);
  const [secretTouched, setSecretTouched] = createSignal(false);
  const [dirty, setDirty] = createSignal(false);
  const status = useSaveStatus();

  const change = (patch: Partial<PlayerDraft>) => {
    if (patch.secret !== undefined) setSecretTouched(true);
    setDraft((d) => ({ ...d, ...patch }));
    setDirty(true);
  };
  const toggleMode = (m: string, on: boolean) => {
    setModes((prev) =>
      on ? [...prev, m] : prev.filter((existing) => existing !== m),
    );
    setDirty(true);
  };

  const body = () =>
    buildServiceConnectionBody({
      base: {
        kind: "player",
        provider: draft().provider,
        label: draft().label,
        enabled: draft().enabled,
        url: draft().url,
      },
      secretTouched: secretTouched(),
      secretValue: draft().secret,
      hasExistingSecret: props.conn.hasSecret,
    });

  // Mode assignment needs its OWN request: ServiceConnectionUpdateRequest has
  // no modes field and the backend's Store.Update ignores incoming modes by
  // contract, so a single PUT would silently no-op a checkbox change. Only sent
  // when the assignment actually changed.
  const save = async () => {
    try {
      await updateServiceConnection(props.conn.id, body());
      if (!sameModes(modes(), props.conn.modes ?? [])) {
        await setServiceConnectionModes(props.conn.id, modes());
      }
      status.set("✓ saved");
      setDraft((d) => ({ ...d, secret: "" }));
      setSecretTouched(false);
      setDirty(false);
      props.onChanged();
    } catch (e) {
      status.failed(e);
      throw e;
    }
  };

  const batched = useSectionSaveItem({
    id: `player:${props.conn.id}`,
    label: props.conn.label || props.conn.provider,
    dirty,
    save,
  });

  // An untouched key isn't in the client's hands at all, so a saved player is
  // tested through its own id route (which uses the stored secret); a retyped
  // one is validated before saving through the stateless route.
  const test = async () => {
    status.set("testing…");
    try {
      const r = secretTouched()
        ? await testServiceConnection({
            provider: draft().provider,
            url: draft().url,
            secret: draft().secret,
          })
        : await testStoredServiceConnection(props.conn.id);
      if (r.ok) status.set("✓ ok");
      else status.failed(new Error(r.error || "connection failed"));
    } catch (e) {
      status.failed(e);
    }
  };

  const name = () => props.conn.label || props.conn.provider;
  const remove = async () => {
    if (!confirm(`Remove the ${name()} player?`)) return;
    try {
      await deleteServiceConnection(props.conn.id);
      props.onChanged();
    } catch (e) {
      status.failed(e);
    }
  };

  return (
    <div class="rounded border border-border p-3">
      <PlayerFields
        draft={draft}
        onChange={change}
        secretPlaceholder={
          props.conn.hasSecret
            ? `unchanged (••••${props.conn.secretSuffix ?? ""})`
            : "api key / token"
        }
        ariaPrefix={`Player ${props.conn.id}`}
      />
      <ModeCheckboxes
        modes={modes}
        onToggle={toggleMode}
        ariaPrefix={`Player ${props.conn.id}`}
      />
      <div class="mt-3 flex items-center gap-2">
        <Button class="!px-2 !py-1 !text-xs" onClick={() => void test()}>
          Test
        </Button>
        {/* Own Save button only when standalone; inside a SectionSave the
            section's one button drives this row. Test/Delete stay per-row. */}
        <Show when={!batched()}>
          <Button
            variant="primary"
            class="!px-2 !py-1 !text-xs"
            onClick={() => void save().catch(() => {})}
          >
            Save
          </Button>
        </Show>
        <Button class="!px-2 !py-1 !text-xs" onClick={() => void remove()}>
          Delete
        </Button>
        <SaveStatus text={status.status().text} error={status.status().error} />
      </div>
    </div>
  );
};

// AddPlayerForm is the explicit Add flow — unlike a fixed always-visible
// ConnectionRow per service, a player only exists once an operator adds it.
// Create takes a PLAIN secret and honours `modes` in the same request (every
// LATER mode change goes through the /modes route instead).
const AddPlayerForm: Component<{
  onAdded: () => void;
  onCancel: () => void;
}> = (props) => {
  const [draft, setDraft] = createSignal<PlayerDraft>({
    label: "",
    provider: "jellyfin",
    url: "",
    secret: "",
    enabled: true,
  });
  const [modes, setModes] = createSignal<string[]>([]);
  const status = useSaveStatus();
  const change = (patch: Partial<PlayerDraft>) =>
    setDraft((d) => ({ ...d, ...patch }));
  const toggleMode = (m: string, on: boolean) =>
    setModes((prev) =>
      on ? [...prev, m] : prev.filter((existing) => existing !== m),
    );

  const test = async () => {
    status.set("testing…");
    try {
      const r = await testServiceConnection({
        provider: draft().provider,
        url: draft().url,
        secret: draft().secret,
      });
      if (r.ok) status.set("✓ ok");
      else status.failed(new Error(r.error || "connection failed"));
    } catch (e) {
      status.failed(e);
    }
  };

  const add = async () => {
    if (!draft().url.trim()) {
      status.failed(new Error("URL is required"));
      return;
    }
    try {
      await createServiceConnection({
        kind: "player",
        provider: draft().provider,
        label: draft().label,
        enabled: draft().enabled,
        url: draft().url,
        secret: draft().secret,
        modes: modes(),
      });
      props.onAdded();
    } catch (e) {
      status.failed(e);
    }
  };

  return (
    <div class="mt-3 rounded border border-dashed border-border p-3">
      <PlayerFields
        draft={draft}
        onChange={change}
        secretPlaceholder="api key / token"
        ariaPrefix="New player"
      />
      <ModeCheckboxes
        modes={modes}
        onToggle={toggleMode}
        ariaPrefix="New player"
      />
      <div class="mt-3 flex items-center gap-2">
        <Button class="!px-2 !py-1 !text-xs" onClick={() => void test()}>
          Test
        </Button>
        <Button
          variant="primary"
          class="!px-2 !py-1 !text-xs"
          onClick={() => void add()}
        >
          Add player
        </Button>
        <Button class="!px-2 !py-1 !text-xs" onClick={() => props.onCancel()}>
          Cancel
        </Button>
        <SaveStatus text={status.status().text} error={status.status().error} />
      </div>
    </div>
  );
};

// MediaPlayersCard is the media-player half of the registry: an explicit Add
// flow over a fixed provider enum, with per-row mode assignment. Players are
// notify-only downstream consumers (a rescan ping after SAK's own file ops) —
// they have no organizational authority, so nothing here is per-mode config
// beyond which modes' operations they get told about.
const MediaPlayersCard: Component = () => {
  const [conns, { refetch }] = createResource(fetchServiceConnections);
  const [adding, setAdding] = createSignal(false);
  // The registry holds usenet subscriptions too; those belong to the Usenet
  // page, not here.
  const players = () => (conns() ?? []).filter((c) => c.kind === "player");

  return (
    <Card title="Media players">
      <Muted>
        Jellyfin, Emby and Plex connections live here, where you can configure
        more than one and assign each to any combination of Movies, Series and
        Adult. A player is told to rescan after SAK moves or renames a file; it
        never decides what's tracked, where a file lives, or what a duplicate is.
      </Muted>
      <Show when={conns.error}>
        <ErrorText>{(conns.error as Error)?.message}</ErrorText>
      </Show>
      {/* Rows mount only AFTER the list resolves: each row seeds hasSecret from
          props.conn at mount, so mounting early would seed false and an
          untouched save would send secret:"" and WIPE the stored key — the same
          hazard ConnectionServiceTable documents. */}
      <Show when={!conns.error && conns() !== undefined}>
        <SectionSave>
          <div class="mt-3 space-y-3">
            <For each={players()}>
              {(c) => <PlayerRow conn={c} onChanged={() => void refetch()} />}
            </For>
            <Show when={players().length === 0}>
              <Muted>No media players configured yet.</Muted>
            </Show>
          </div>
        </SectionSave>
      </Show>
      <Show
        when={adding()}
        fallback={
          <Button class="mt-3" onClick={() => setAdding(true)}>
            Add player
          </Button>
        }
      >
        <AddPlayerForm
          onAdded={() => {
            setAdding(false);
            void refetch();
          }}
          onCancel={() => setAdding(false)}
        />
      </Show>
    </Card>
  );
};
