// Global section — settings that don't change with the Movies/Series/Adult
// mode selector, pulled out of Advanced.tsx (which is scoped to the per-mode
// ModeSelector) so they're not visually tied to a mode that doesn't affect
// them: monitored-title refresh interval + manual trigger, the Entity
// Database cache (now unconditionally visible, no longer Adult-gated), and
// Watch Folders. None of these are wrapped in a SectionSave batch — each
// keeps (or gains, for recheck-interval) its own standalone Save button,
// same shape as before the move.

import {
  type Component,
  createEffect,
  createResource,
  createSignal,
  For,
  on,
  Show,
} from "solid-js";
import {
  fetchEntitySyncInterval,
  fetchEntitySyncStatus,
  fetchRecheckInterval,
  fetchWatchFolders,
  fetchWatchFoldersPollInterval,
  putEntitySyncInterval,
  putRecheckInterval,
  putWatchFoldersEnabled,
  putWatchFoldersPollInterval,
  triggerEntitySync,
  triggerRecheck,
  type EntitySyncSource,
} from "../../api/settings";
import { Button, Muted } from "../../components/ui";
import { Card, SaveStatus, useSaveStatus } from "./shared";
import { DurationSetting } from "./Advanced";

// RecheckTriggerButton is the manual "Refresh now" action for the
// monitored-title refresh — an immediate, always-available fire-and-forget
// POST, not a tracked/dirty field, so it doesn't register with any enclosing
// SectionSave (same as Entity Database's per-source "Sync now" buttons). The
// request only confirms the refresh STARTED (202 Accepted); there's no count
// or last-run timestamp to poll afterward, unlike Entity Database's sync
// status, since a monitored-title refresh just flips flags on entries
// nothing else in this screen surfaces.
const RecheckTriggerButton: Component = () => {
  const [state, setState] = createSignal<
    "idle" | "triggering" | "started" | "error"
  >("idle");
  const [error, setError] = createSignal<string | null>(null);

  const trigger = async () => {
    setState("triggering");
    setError(null);
    try {
      await triggerRecheck();
      setState("started");
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setState("error");
    }
  };

  return (
    <div class="mb-3 flex items-center gap-2">
      <Button
        variant="secondary"
        onClick={() => void trigger()}
        disabled={state() === "triggering"}
      >
        {state() === "triggering" ? "Starting…" : "Refresh now"}
      </Button>
      <Show when={state() === "started"}>
        <span class="text-xs text-muted">
          Refresh started — runs in the background.
        </span>
      </Show>
      <Show when={state() === "error"}>
        <span class="text-xs text-red-500">{error()}</span>
      </Show>
    </div>
  );
};

// RecheckSection owns the global monitored-title refresh interval and its
// manual trigger. Deliberately NOT wrapped in a SectionSave: DurationSetting
// already renders its own standalone Save button whenever it isn't inside a
// SectionSave batch (see its `!batched()` / useSectionSaveItem handling in
// Advanced.tsx), so no new save-status code is needed here — this is the
// same standalone-save shape EntityDatabaseSection's entity-sync-interval and
// WatchFoldersSection's watch-folders-poll-interval already use.
const RecheckSection: Component = () => {
  const [recheck] = createResource(fetchRecheckInterval);

  return (
    <Card title="Monitored Title Refresh — global">
      <DurationSetting
        id="recheck-interval"
        label="Monitored title refresh interval — global"
        help="Re-checks availability for every monitored title on this cadence."
        value={() => recheck()}
        onSave={(v) => putRecheckInterval(v)}
      />
      <RecheckTriggerButton />
    </Card>
  );
};

// WatchFoldersSection is a global (not per-mode) card — shown once, regardless
// of which mode tab is active.
const WatchFoldersSection: Component = () => {
  const [status, { refetch }] = createResource(fetchWatchFolders);
  const [pollInterval] = createResource(fetchWatchFoldersPollInterval);
  const [enabled, setEnabled] = createSignal(false);
  const [dirty, setDirty] = createSignal(false);
  const saveStatus = useSaveStatus();

  createEffect(
    on(status, (v) => {
      if (v !== undefined) {
        setEnabled(v.enabled);
        setDirty(false);
      }
    }),
  );

  const save = async () => {
    try {
      await putWatchFoldersEnabled(enabled());
      setDirty(false);
      saveStatus.saved();
      void refetch();
    } catch (e) {
      saveStatus.failed(e);
    }
  };

  return (
    <Card title="Watch Folders — global">
      <p class="mb-3 text-sm text-muted">
        When enabled, SAK monitors each mode's configured library root folder
        for new content and automatically runs a Rename Scan. Only Scan is
        triggered — proposals still require a human Apply click. Takes effect
        within one config-poll interval (default 30s, configurable below) of
        toggling.
      </p>
      <label class="mb-3 flex items-center gap-2">
        <input
          type="checkbox"
          aria-label="Watch folders enabled"
          checked={enabled()}
          onChange={(e) => {
            setEnabled(e.currentTarget.checked);
            setDirty(true);
          }}
        />
        <span class="text-sm text-fg">Watch folders enabled</span>
      </label>
      <DurationSetting
        id="watch-folders-poll-interval"
        label="Config poll interval — global"
        help="How often SAK re-reads the enabled toggle and each mode's root path above — NOT how often folders are scanned (scanning is event-driven off filesystem events, unrelated to this cadence)."
        value={() => pollInterval()}
        onSave={(v) => putWatchFoldersPollInterval(v)}
        zeroLabel="(0 = use the default 30-second cadence)"
      />
      <Show when={status()}>
        {(s) => {
          const roots = Object.entries(s().roots);
          return (
            <Show when={roots.length > 0}>
              <ul class="mb-3 space-y-1 text-xs text-muted">
                <For each={roots}>
                  {([mode, path]) => (
                    <li>
                      <span class="font-medium capitalize">{mode}:</span> {path}
                    </li>
                  )}
                </For>
              </ul>
            </Show>
          );
        }}
      </Show>
      <div class="flex items-center gap-3">
        <Show when={dirty()}>
          <button
            class="rounded bg-accent px-3 py-1.5 text-sm font-medium text-white hover:bg-accent/80"
            onClick={() => void save()}
          >
            Save
          </button>
        </Show>
        <SaveStatus text={saveStatus.status().text} error={saveStatus.status().error} />
      </div>
    </Card>
  );
};

// EntityDatabaseSection shows the parse_studios/parse_performers entity cache
// — counts, per-source manual "Sync now" triggers, and the shared background
// sync interval — moved here from the AI tab (Settings → Connections → AI)
// since it's a library-content admin concern, not an AI/connection one. Now
// unconditionally visible (no longer Adult-only-gated) since it lives on the
// Global tab, which has no mode selector. The interval setting sits in its
// OWN Card, with its own standalone Save button — same shape as Adult newest
// rows' "background scan" card (AdultRowAdmin.tsx) — so it can be saved
// independently of any other field without an accidental combined commit.
const EntityDatabaseSection: Component = () => {
  const [status, { refetch }] = createResource(fetchEntitySyncStatus);
  const [interval] = createResource(fetchEntitySyncInterval);
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
    <>
      <Card title="Entity Database — background sync">
        <DurationSetting
          id="entity-sync-interval"
          label="Entity sync interval (all sources)"
          help="How often Stash/ThePornDB/StashDB/FansDB are synced together to keep the entity cache current, on top of the manual per-source buttons below."
          value={() => interval()}
          onSave={(v) => putEntitySyncInterval(v)}
        />
      </Card>
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
    </>
  );
};

export const GlobalSection: Component = () => (
  <>
    <RecheckSection />
    <EntityDatabaseSection />
    <WatchFoldersSection />
  </>
);
