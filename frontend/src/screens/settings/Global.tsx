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
  useContext,
} from "solid-js";
import {
  fetchEntitySyncInterval,
  fetchEntitySyncStatus,
  fetchRecheckInterval,
  fetchWatchFolders,
  fetchWatchFoldersPollInterval,
  putAdultModeEnabled,
  putAdultNewestScanInterval,
  putEntitySyncInterval,
  putRecheckInterval,
  putWatchFoldersEnabled,
  putWatchFoldersPollInterval,
  triggerEntitySync,
  triggerRecheck,
  type EntitySyncSource,
} from "../../api/settings";
import { AdultModeContext, Button, Muted } from "../../components/ui";
import { Card, SaveStatus, useSaveStatus } from "./shared";
import { DurationSetting } from "./Advanced";
import { APISection } from "./APISection";
import { SectionLockSection } from "./SectionLock";

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

// AdultModeSection is the master switch for the adult_mode_enabled visibility
// gate — see ralplan-adult-disable-switch.md step 10. It is ALWAYS rendered
// unconditionally, regardless of the switch's own state (it must stay
// reachable so an operator can always re-enable Adult). Enabling is a plain,
// immediate single PUT, no confirmation. Disabling opens a confirmation
// dialog (this is a visibility switch, not backend enforcement — see the
// disclosure copy below) with an opt-in "also stop the Adult Newest
// background scanner" checkbox; on confirm this fires the
// adult-mode-enabled PUT first, then — only if the checkbox was checked —
// a second, separate adult-newest-scan-interval PUT set to 0 (two sequential
// single-key requests, per the plan's Open Question 1 — never a combined
// request). Canceling fires zero requests and changes nothing.
const AdultModeSection: Component = () => {
  const { enabled, refetch } = useContext(AdultModeContext);
  const [confirmOpen, setConfirmOpen] = createSignal(false);
  const [stopScanner, setStopScanner] = createSignal(false);
  const [busy, setBusy] = createSignal(false);
  const saveStatus = useSaveStatus();

  const onToggle = (checked: boolean) => {
    if (checked) {
      void enableNow();
    } else {
      setStopScanner(false);
      setConfirmOpen(true);
    }
  };

  const enableNow = async () => {
    setBusy(true);
    try {
      await putAdultModeEnabled(true);
      saveStatus.saved();
      refetch();
    } catch (e) {
      saveStatus.failed(e);
    } finally {
      setBusy(false);
    }
  };

  const confirmDisable = async () => {
    setBusy(true);
    try {
      // Single-key toggle first...
      await putAdultModeEnabled(false);
      // ...then, only if opted in, the pre-existing interval endpoint —
      // never a combined/compound request (Open Question 1).
      if (stopScanner()) {
        await putAdultNewestScanInterval(0);
      }
      saveStatus.saved();
      refetch();
      setConfirmOpen(false);
    } catch (e) {
      saveStatus.failed(e);
    } finally {
      setBusy(false);
    }
  };

  const cancelDisable = () => {
    // Fires zero requests and changes nothing (Critic-mandated fix — this
    // criterion was dropped from an earlier draft, restore it deliberately).
    setConfirmOpen(false);
    setStopScanner(false);
  };

  return (
    <Card title="Adult Mode">
      <label class="mb-3 flex items-center gap-2">
        <input
          type="checkbox"
          aria-label="Enable Adult mode"
          // Depends on confirmOpen() too, not just enabled() — clicking to
          // open the disable dialog natively flips the DOM checkbox before
          // Solid re-touches it; since `enabled()` itself hasn't changed yet
          // (only Cancel/Confirm change it), a `checked={enabled()}` binding
          // alone would never re-assert on Cancel and the box would stay
          // stuck showing unchecked. Tying it to confirmOpen() too forces a
          // fresh evaluation whenever the dialog opens OR closes, so
          // Cancel correctly snaps it back to the unchanged `enabled()`
          // value.
          checked={confirmOpen() ? false : enabled()}
          disabled={busy()}
          onChange={(e) => onToggle(e.currentTarget.checked)}
        />
        <span class="text-sm text-fg">Enable Adult mode</span>
      </label>
      <Muted>
        Controls whether Adult-related screens, tabs, and settings sections
        are visible anywhere in this app. This is a visibility switch only —
        it never blocks API access to Adult routes, and it never stops the 3
        shared background schedulers (watch folders, monitored title refresh,
        scan scheduler), which keep running system-wide either way.
      </Muted>
      <SaveStatus
        text={saveStatus.status().text}
        error={saveStatus.status().error}
      />

      <Show when={confirmOpen()}>
        <div
          class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"
          onClick={cancelDisable}
        >
          <div
            class="w-full max-w-md rounded-xl border border-border bg-surface p-5 shadow-2xl"
            onClick={(e) => e.stopPropagation()}
          >
            <h3 class="mb-3 text-base font-semibold text-fg">
              Disable Adult mode?
            </h3>
            <p class="mb-3 text-sm text-muted">
              Adult-related screens, tabs, and settings sections will be
              hidden everywhere in this app. This does not restrict anything
              on the backend — Adult API routes keep working exactly as
              before, and the 3 shared background schedulers (watch folders,
              monitored title refresh, scan scheduler) keep running
              system-wide, including whatever Adult work they were already
              doing.
            </p>
            <label class="mb-4 flex items-center gap-2">
              <input
                type="checkbox"
                aria-label="Also stop the Adult Newest background scanner"
                checked={stopScanner()}
                onChange={(e) => setStopScanner(e.currentTarget.checked)}
              />
              <span class="text-sm text-fg">
                Also stop the Adult Newest background scanner
              </span>
            </label>
            <div class="flex justify-end gap-2">
              <Button onClick={cancelDisable} disabled={busy()}>
                Cancel
              </Button>
              <Button
                variant="primary"
                onClick={() => void confirmDisable()}
                disabled={busy()}
              >
                Disable
              </Button>
            </div>
          </div>
        </div>
      </Show>
    </Card>
  );
};

// APISection is composed in here rather than into AdvancedSection because
// Prowlarr/Stash/media players are global, not per-mode — GlobalSection renders
// ABOVE the Advanced tab's mode selector, which is the correct semantic for
// them. It leads the fragment: connection setup is what an operator most often
// comes to this tab for.
//
// SectionLockSection is composed here for the SAME reason and follows the same
// precedent: a section PIN lock is mode-INDEPENDENT, so it must not sit inside
// AdvancedSection, which takes a `mode: () => Mode` and is governed by the
// selector below it. It is placed next to AdultModeSection because the two are
// adjacent concerns and routinely read together — one is a visibility switch
// that enforces nothing, the other is the actual enforcement boundary.
export const GlobalSection: Component = () => (
  <>
    <APISection />
    <AdultModeSection />
    <SectionLockSection />
    <RecheckSection />
    <EntityDatabaseSection />
    <WatchFoldersSection />
  </>
);
