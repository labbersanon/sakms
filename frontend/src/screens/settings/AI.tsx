// AI section — BYOAI fallback toggle + provider/model selection + entity cache
// admin. Extracted from the original single-file Settings.tsx.
//
// With DB-first filename parsing (internal/parseentity), the AI path is now an
// optional fallback — off by default. The toggle at the top of the AI card
// gates provider+model+connection fields: when disabled, those fields are shown
// dimmed (so the operator can still pre-configure them) but the backend
// buildAIClient returns nil and ParseFilename is never called.
//
// The "Entity Database" card (Phase 6) shows the parse_studios +
// parse_performers cache counts, per-source last-synced state, and "Sync now"
// buttons that POST /api/admin/entity-sync/{source} and fire an on-demand sync
// in the background. The operator polls with the refresh button to see when
// counts update.

import {
  type Component,
  createEffect,
  createResource,
  createSignal,
  For,
  Show,
} from "solid-js";
import {
  AI_PROVIDERS,
  type EntitySyncSource,
  fetchAIFallbackEnabled,
  fetchAIModel,
  fetchAIProvider,
  fetchConnections,
  fetchEntitySyncStatus,
  fetchNetscanKnown,
  putAIFallbackEnabled,
  putAIModel,
  putAIProvider,
  triggerEntitySync,
} from "../../api/settings";
import type { ConnectionSummary, NetscanFinding } from "../../api/settings";
import { Button, Muted, inputClass, labelClass } from "../../components/ui";
import { ConnectionMiniTable, ConnectionRow } from "./Connections";
import {
  Card,
  SaveStatus,
  SectionSave,
  useSaveStatus,
  useSectionSaveItem,
} from "./shared";

// AISection is a thin shell so the provider/model form + provider/Brave
// connection rows all sit INSIDE one SectionSave (so their registrations see its
// context), while the Entity Database card — with its own independent Sync
// buttons — stays outside the batched Save.
export const AISection: Component = () => (
  <>
    <SectionSave>
      <AIProviderModelCard />
    </SectionSave>
    <EntityDatabaseCard />
  </>
);

// AIProviderModelCard holds the batched AI fallback form and the provider/Brave
// connection rows. It registers the form with the enclosing SectionSave and the
// two ConnectionRows register themselves — each keeping its own three-state
// secret gate; nothing is merged.
const AIProviderModelCard: Component = () => {
  const [fallbackEnabled, { refetch: refetchFallback }] = createResource(
    fetchAIFallbackEnabled,
  );
  const [provider] = createResource(fetchAIProvider);
  const [model] = createResource(fetchAIModel);

  const [enabled, setEnabled] = createSignal(false);
  const [prov, setProv] = createSignal("ollama");
  const [mdl, setMdl] = createSignal("");
  // dirty flips true on any provider/model/toggle edit and resets on save or a
  // fresh server load, so the AI tab's one Save button knows this form's state.
  const [dirty, setDirty] = createSignal(false);

  createEffect(() => {
    const v = fallbackEnabled();
    if (v !== undefined) {
      setEnabled(v);
      setDirty(false);
    }
  });
  createEffect(() => {
    const p = provider();
    if (p) setProv(p);
  });
  createEffect(() => {
    const m = model();
    if (m !== undefined) setMdl(m);
  });

  const status = useSaveStatus();
  const save = async () => {
    try {
      await putAIFallbackEnabled(enabled());
      await putAIProvider(prov());
      await putAIModel(mdl());
      void refetchFallback();
      setDirty(false);
      status.saved();
    } catch (e) {
      status.failed(e);
      throw e;
    }
  };
  // The provider/model form folds into the AI tab's one Save button. The provider
  // ConnectionRow and Brave ConnectionRow below register themselves separately
  // (same SectionSave), each keeping its own three-state secret gate.
  const batched = useSectionSaveItem({
    id: "ai-form",
    label: "AI settings",
    dirty,
    save,
  });

  // Connection data owned here — same gate as the Connections table (mount
  // only after conns() resolves to avoid wiping stored secrets on a bare save).
  const [conns, { refetch }] = createResource(fetchConnections);
  const [findings] = createResource(fetchNetscanKnown);
  const byService = () => {
    const m: Record<string, ConnectionSummary> = {};
    for (const c of conns() ?? []) m[c.service] = c;
    return m;
  };
  const findingByService = () => {
    const m: Record<string, NetscanFinding> = {};
    for (const f of findings() ?? []) m[f.service] = f;
    return m;
  };

  return (
    <>
      <Card title="AI Fallback (optional)">
        <form
          onSubmit={(e) => (e.preventDefault(), void save().catch(() => {}))}
        >
          <label class="mb-4 flex items-center gap-3">
            <input
              type="checkbox"
              class="h-4 w-4 rounded border-border accent-primary"
              checked={enabled()}
              onChange={(e) => {
                setEnabled(e.currentTarget.checked);
                setDirty(true);
              }}
            />
            <span class="text-sm font-medium text-fg">
              Enable AI fallback (ParseFilename runs only when DB-first parsing
              finds no studio or title)
            </span>
          </label>

          <div
            class="grid gap-3 sm:grid-cols-2"
            style={{ opacity: enabled() ? "1" : "0.5" }}
          >
            <label class="block">
              <span class={labelClass}>Provider</span>
              <select
                class={`${inputClass} mt-1`}
                aria-label="AI provider"
                value={prov()}
                onChange={(e) => {
                  setProv(e.currentTarget.value);
                  setDirty(true);
                }}
                disabled={!enabled()}
              >
                <For each={AI_PROVIDERS}>
                  {(p) => <option value={p}>{p}</option>}
                </For>
              </select>
            </label>
            <label class="block">
              <span class={labelClass}>Model</span>
              <input
                type="text"
                class={`${inputClass} mt-1`}
                placeholder="e.g. qwen2.5vl:7b, gpt-4o-mini, gemini-2.5-flash, claude-haiku-4-5"
                value={mdl()}
                onInput={(e) => {
                  setMdl(e.currentTarget.value);
                  setDirty(true);
                }}
                disabled={!enabled()}
              />
            </label>
          </div>

          <div class="mt-3 flex items-center gap-2">
            {/* Own Save button only when standalone; inside the AI tab's
                SectionSave the one section button commits this form too. */}
            <Show when={!batched()}>
              <Button variant="primary" type="submit">
                Save
              </Button>
            </Show>
            <SaveStatus
              text={status.status().text}
              error={status.status().error}
            />
          </div>
        </form>
        <Muted class="mt-2">
          DB-first parsing (studio/performer lookup tables) runs first and
          requires no AI. Enable the fallback only if you want ParseFilename to
          fill in fields that the entity cache couldn't resolve.
        </Muted>
      </Card>

      <Show when={enabled()}>
        <Card title="Selected provider connection">
          <Show when={conns() !== undefined}>
            <ConnectionMiniTable>
              {/* keyed on provider so switching the dropdown remounts the row */}
              <Show when={prov()} keyed>
                {(p) => (
                  <ConnectionRow
                    service={p}
                    existing={byService()[p]}
                    finding={findingByService()[p]}
                    onChanged={() => void refetch()}
                  />
                )}
              </Show>
            </ConnectionMiniTable>
          </Show>
        </Card>

        <Card title="Web search grounding (Brave)">
          <Muted class="mb-3">
            Used for Adult identification regardless of which AI provider above
            is active.
          </Muted>
          <Show when={conns() !== undefined}>
            <ConnectionMiniTable>
              <ConnectionRow
                service="brave"
                existing={byService()["brave"]}
                finding={findingByService()["brave"]}
                onChanged={() => void refetch()}
              />
            </ConnectionMiniTable>
          </Show>
        </Card>
      </Show>
    </>
  );
};

// EntityDatabaseCard shows parse_studios / parse_performers counts and
// per-source sync state with on-demand "Sync now" buttons.
const EntityDatabaseCard: Component = () => {
  const [status, { refetch }] = createResource(fetchEntitySyncStatus);
  const [syncing, setSyncing] = createSignal<EntitySyncSource | null>(null);
  const [syncError, setSyncError] = createSignal<string | null>(null);

  const sync = async (source: EntitySyncSource) => {
    setSyncing(source);
    setSyncError(null);
    try {
      await triggerEntitySync(source);
    } catch (e) {
      setSyncError(e instanceof Error ? e.message : String(e));
    } finally {
      setSyncing(null);
    }
  };

  const SOURCE_LABELS: Record<string, string> = {
    stash: "Stash (local)",
    tpdb: "ThePornDB",
    stashdb: "StashDB",
    fansdb: "FansDB",
  };

  return (
    <Card title="Entity Database">
      <Show when={status()} fallback={<Muted>Loading…</Muted>}>
        {(s) => (
          <>
            <div class="mb-4 flex gap-6 text-sm text-fg">
              <span>
                <span class="font-semibold">{s().studioCount}</span> studios
              </span>
              <span>
                <span class="font-semibold">{s().performerCount}</span>{" "}
                performers
              </span>
            </div>

            <div class="space-y-2">
              <For each={s().sources}>
                {(src) => (
                  <div class="flex items-center justify-between gap-4 rounded border border-border px-3 py-2 text-sm">
                    <div>
                      <span class="font-medium text-fg">
                        {SOURCE_LABELS[src.source] ?? src.source}
                      </span>
                      <span class="ml-3 text-muted">
                        {src.syncedAt
                          ? `Last synced ${src.syncedAt}`
                          : "Never synced"}
                      </span>
                    </div>
                    <Button
                      variant="secondary"
                      onClick={() =>
                        void sync(src.source as EntitySyncSource)
                      }
                      disabled={syncing() !== null}
                    >
                      {syncing() === src.source ? "Syncing…" : "Sync now"}
                    </Button>
                  </div>
                )}
              </For>
            </div>

            <Show when={syncError()}>
              <p class="mt-2 text-sm text-red-500">{syncError()}</p>
            </Show>

            <div class="mt-3">
              <Button variant="secondary" onClick={() => void refetch()}>
                Refresh counts
              </Button>
            </div>
          </>
        )}
      </Show>
    </Card>
  );
};
