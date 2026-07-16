// Advanced section — the per-mode bounded-integer settings (phash threshold,
// match-confidence threshold, global recheck interval) and the Adult-only
// identify-enabled toggle. Extracted from the original single-file Settings.tsx.

import {
  type Component,
  createEffect,
  createResource,
  createSignal,
  For,
  on,
  Show,
} from "solid-js";
import type { Mode } from "../../api/discover";
import {
  fetchConfidenceThreshold,
  fetchEntitySyncInterval,
  fetchEntitySyncStatus,
  fetchIdentifyEnabled,
  fetchPHashThreshold,
  fetchRecheckInterval,
  putConfidenceThreshold,
  putEntitySyncInterval,
  putIdentifyEnabled,
  putPHashThreshold,
  putRecheckInterval,
  triggerEntitySync,
  type EntitySyncSource,
} from "../../api/settings";
import { Button, Muted, inputClass, labelClass } from "../../components/ui";
import {
  Card,
  MODE_LABELS,
  SaveStatus,
  SectionSave,
  useSaveStatus,
  useSectionSaveItem,
} from "./shared";

// DurationUnit is the fixed set of units the DurationSetting picker lets an
// operator express a value in — each with its own bound (a slider that only
// ever needs to reach "one unit's worth" of the next: 59 minutes, 23 hours,
// 30 days). Want more than 23 hours? Switch to Days — there is no composite
// "Xd Yh Zm" total; the picker represents ONE unit's amount at a time.
type DurationUnit = "minutes" | "hours" | "days";

const DURATION_UNITS: DurationUnit[] = ["days", "hours", "minutes"];
const UNIT_SECONDS: Record<DurationUnit, number> = {
  minutes: 60,
  hours: 3600,
  days: 86400,
};
const UNIT_MAX: Record<DurationUnit, number> = {
  minutes: 59,
  hours: 23,
  days: 30,
};
const UNIT_LABELS: Record<DurationUnit, string> = {
  minutes: "Minutes",
  hours: "Hours",
  days: "Days",
};

// secondsToUnitAmount picks the best-fit unit+amount to display a stored
// seconds value in: the smallest unit that divides the value evenly and
// fits within that unit's bound, preferring fidelity (exact) over reach.
//
// A value that doesn't divide evenly into any unit — e.g. a value from
// BEFORE this picker existed, when recheck-interval and the Adult-newest
// scan interval were plain free-typed "seconds" boxes with no multiple-of-60
// requirement (a stored 90, or 45) — falls back to the finest unit that can
// still express it as a NON-ZERO rounded amount. This must never round a
// positive value down to 0: an active job's stored interval silently
// display-and-saving as "0 = off" would disable it out from under the
// operator the moment they open this card, without them touching anything.
export function secondsToUnitAmount(totalSeconds: number): {
  unit: DurationUnit;
  amount: number;
} {
  if (totalSeconds <= 0) return { unit: "hours", amount: 0 };
  for (const unit of ["minutes", "hours", "days"] as DurationUnit[]) {
    const perUnit = UNIT_SECONDS[unit];
    if (totalSeconds % perUnit === 0) {
      const amount = totalSeconds / perUnit;
      if (amount <= UNIT_MAX[unit]) return { unit, amount };
    }
  }
  for (const unit of ["minutes", "hours"] as DurationUnit[]) {
    const rounded = Math.max(1, Math.round(totalSeconds / UNIT_SECONDS[unit]));
    if (rounded <= UNIT_MAX[unit]) return { unit, amount: rounded };
  }
  const days = Math.max(1, Math.round(totalSeconds / UNIT_SECONDS.days));
  return { unit: "days", amount: Math.min(days, UNIT_MAX.days) };
}

// DurationSetting is the Days/Hours/Minutes duration picker for the app's
// genuine time-interval settings (background recheck interval, Adult newest
// rows scan interval, entity-sync interval) — deliberately NOT used for
// NumberSetting's dimensionless-score fields (phash similarity, match
// confidence), which keep a plain bounded number box since a unit selector
// makes no sense for a 0-64 or 0-100 score. Persists/loads whole seconds (the
// wire format every one of these endpoints already uses); the unit+slider is
// purely a display/input convenience layered on top. 0 (in any unit) always
// means "off" — the same convention every interval setting in this app uses.
export const DurationSetting: Component<{
  label: string;
  help: string;
  value: () => number | undefined; // seconds
  onSave: (v: number) => Promise<void>;
}> = (props) => {
  const [unit, setUnit] = createSignal<DurationUnit>("hours");
  const [amount, setAmount] = createSignal(0);
  const [dirty, setDirty] = createSignal(false);

  createEffect(() => {
    const v = props.value();
    if (v !== undefined) {
      const fit = secondsToUnitAmount(v);
      setUnit(fit.unit);
      setAmount(fit.amount);
      setDirty(false);
    }
  });

  const totalSeconds = () => amount() * UNIT_SECONDS[unit()];

  const status = useSaveStatus();
  const save = async () => {
    try {
      await props.onSave(totalSeconds());
      setDirty(false);
      status.saved();
    } catch (e) {
      status.failed(e);
      throw e;
    }
  };
  // Batched inside an enclosing SectionSave when present (Advanced tab);
  // standalone with its own Save button otherwise (Adult newest rows' own
  // card, and Entity Database's card below — same dual-mode contract as
  // NumberSetting).
  const batched = useSectionSaveItem({
    id: `duration:${props.label}`,
    label: props.label,
    dirty,
    save,
  });

  const changeUnit = (u: DurationUnit) => {
    // Preserve the current total duration, re-expressed (rounded, clamped)
    // in the newly selected unit — switching to Days after maxing out Hours
    // shows the equivalent day count rather than resetting to 0.
    const converted = Math.round(totalSeconds() / UNIT_SECONDS[u]);
    setUnit(u);
    setAmount(Math.min(converted, UNIT_MAX[u]));
    setDirty(true);
  };

  // setClampedAmount clamps into [0, unit max] and writes the clamped value
  // straight back into the element that fired the event. That extra write is
  // necessary, not defensive-programming noise: if the clamped result equals
  // the signal's CURRENT value (e.g. typing "-5" when amount is already 0),
  // Solid's fine-grained reactivity sees no change and skips the DOM update,
  // leaving the input showing the raw out-of-range text the operator typed
  // even though the value actually being saved is the clamped one.
  const setClampedAmount = (e: { currentTarget: HTMLInputElement }, v: number) => {
    const clamped = Math.min(Math.max(0, Math.round(v)), UNIT_MAX[unit()]);
    setAmount(clamped);
    setDirty(true);
    e.currentTarget.value = String(clamped);
  };

  return (
    <div class="mb-3">
      <span class={labelClass}>{props.label}</span>
      <div class="mt-1 flex gap-1">
        <For each={DURATION_UNITS}>
          {(u) => (
            <button
              type="button"
              aria-pressed={unit() === u}
              class={`rounded border px-2 py-1 text-xs ${
                unit() === u
                  ? "border-accent bg-accent text-accent-fg"
                  : "border-border text-fg"
              }`}
              onClick={() => changeUnit(u)}
            >
              {UNIT_LABELS[u]}
            </button>
          )}
        </For>
      </div>
      <div class="mt-2 flex items-center gap-3">
        <input
          type="range"
          min={0}
          max={UNIT_MAX[unit()]}
          value={amount()}
          aria-label={`${props.label} slider (${UNIT_LABELS[unit()]})`}
          class="h-2 flex-1 accent-accent"
          onInput={(e) => setClampedAmount(e, Number(e.currentTarget.value))}
        />
        <input
          type="number"
          class={`${inputClass} !w-20`}
          min={0}
          max={UNIT_MAX[unit()]}
          aria-label={props.label}
          value={amount()}
          onInput={(e) => setClampedAmount(e, Number(e.currentTarget.value))}
        />
        <span class="text-xs text-muted">
          {UNIT_LABELS[unit()].toLowerCase()}
        </span>
      </div>
      <div class="mt-2 flex items-center gap-2">
        <Show when={!batched()}>
          <Button variant="primary" onClick={() => void save().catch(() => {})}>
            Save
          </Button>
        </Show>
        <SaveStatus text={status.status().text} error={status.status().error} />
      </div>
      <Muted class="mt-1">
        {props.help}{" "}
        {amount() === 0
          ? "(0 = off, the default)"
          : `— every ${amount()} ${UNIT_LABELS[unit()].toLowerCase()}`}
      </Muted>
    </div>
  );
};

// NumberSetting is one bounded integer field (phash-threshold,
// match-confidence-threshold, recheck-interval). It mirrors the backend's range
// client-side (min/max) before submitting; the backend re-validates. save
// disabled while out of range so the operator sees the bound, never a 400.
// Exported so AdultRowAdmin can reuse the exact same control for its own global
// scan-interval field (same 0 = off convention).
export const NumberSetting: Component<{
  label: string;
  help: string;
  value: () => number | undefined;
  min: number;
  max?: number;
  onSave: (v: number) => Promise<void>;
}> = (props) => {
  const [val, setVal] = createSignal(0);
  const [dirty, setDirty] = createSignal(false);
  createEffect(() => {
    const v = props.value();
    if (v !== undefined) {
      setVal(v);
      setDirty(false);
    }
  });
  const status = useSaveStatus();
  const outOfRange = () =>
    val() < props.min || (props.max !== undefined && val() > props.max);
  // save rethrows on failure — including the client-side out-of-range guard — so
  // a batched section Save reports this field as failed rather than a false
  // "saved" (no PUT is fired for an out-of-range value in either mode).
  const save = async () => {
    if (outOfRange()) {
      const err = new Error(
        props.max !== undefined
          ? `must be between ${props.min} and ${props.max}`
          : `must be ${props.min} or greater`,
      );
      status.failed(err);
      throw err;
    }
    try {
      await props.onSave(val());
      setDirty(false);
      status.saved();
    } catch (e) {
      status.failed(e);
      throw e;
    }
  };
  // Batched inside the Advanced tab's SectionSave; standalone (returns false) in
  // AdultRowAdmin, where it keeps its own per-card Save button. Label is unique
  // per instance, so it doubles as the registration id.
  const batched = useSectionSaveItem({
    id: `number:${props.label}`,
    label: props.label,
    dirty,
    save,
  });
  return (
    <div class="mb-3">
      <label class="block">
        <span class={labelClass}>{props.label}</span>
        <input
          type="number"
          class={`${inputClass} mt-1 !w-40`}
          min={props.min}
          max={props.max}
          aria-label={props.label}
          value={val()}
          onInput={(e) => {
            setVal(Number(e.currentTarget.value));
            setDirty(true);
          }}
        />
      </label>
      <div class="mt-2 flex items-center gap-2">
        <Show when={!batched()}>
          <Button variant="primary" onClick={() => void save().catch(() => {})}>
            Save
          </Button>
        </Show>
        <SaveStatus text={status.status().text} error={status.status().error} />
      </div>
      <Muted class="mt-1">{props.help}</Muted>
    </div>
  );
};

const IdentifyEnabledSetting: Component<{ mode: () => Mode }> = (props) => {
  const [current] = createResource(props.mode, fetchIdentifyEnabled);
  const [enabled, setEnabled] = createSignal(true);
  const [dirty, setDirty] = createSignal(false);
  createEffect(
    on(current, (v) => {
      if (v !== undefined) {
        setEnabled(v);
        setDirty(false);
      }
    }),
  );
  const status = useSaveStatus();
  const save = async () => {
    try {
      await putIdentifyEnabled(props.mode(), enabled());
      setDirty(false);
      status.saved();
    } catch (e) {
      status.failed(e);
      throw e;
    }
  };
  const batched = useSectionSaveItem({
    id: "identify-enabled",
    label: "identify enabled",
    dirty,
    save,
  });
  return (
    <div class="mb-3">
      <label class="flex items-center gap-2">
        <input
          type="checkbox"
          aria-label="Adult phash-first identification enabled"
          checked={enabled()}
          onChange={(e) => {
            setEnabled(e.currentTarget.checked);
            setDirty(true);
          }}
        />
        <span class="text-sm text-fg">
          Adult phash-first identification enabled
        </span>
      </label>
      <div class="mt-2 flex items-center gap-2">
        <Show when={!batched()}>
          <Button variant="primary" onClick={() => void save().catch(() => {})}>
            Save
          </Button>
        </Show>
        <SaveStatus text={status.status().text} error={status.status().error} />
      </div>
      <Muted class="mt-1">
        When on, Adult Rename identifies scenes by perceptual hash first (no live
        Stash required). Turn off to skip identification.
      </Muted>
    </div>
  );
};

export const AdvancedSection: Component<{ mode: () => Mode }> = (props) => {
  // recheck-interval is GLOBAL, not per-mode — fetched once, independent of the
  // mode tab.
  const [recheck] = createResource(fetchRecheckInterval);
  // phash-threshold is per-mode-generic; confidence is Movies/Series only;
  // identify-enabled is Adult only. Each keyed on the mode accessor.
  const [phash] = createResource(props.mode, fetchPHashThreshold);
  const [confidence] = createResource(
    () => (props.mode() === "adult" ? undefined : props.mode()),
    fetchConfidenceThreshold,
  );

  return (
    <>
      <Card title={`Advanced Settings (${MODE_LABELS[props.mode()]})`}>
        <SectionSave>
        <DurationSetting
          label="Background recheck interval — global"
          help="Re-checks availability for every watched title on this cadence."
          value={() => recheck()}
          onSave={(v) => putRecheckInterval(v)}
        />
        <NumberSetting
          label="Dedup phash similarity threshold (0–64)"
          help="Per-frame average Hamming bits below which two files are treated as perceptual duplicates by Dedup. Lower is stricter."
          value={() => phash()}
          min={0}
          max={64}
          onSave={(v) => putPHashThreshold(props.mode(), v)}
        />
        <Show when={props.mode() !== "adult"}>
          <NumberSetting
            label="Rename match-confidence threshold (0–100)"
            help="Minimum TMDB match confidence (a percentage) before Rename auto-accepts a match instead of surfacing it for manual re-pick."
            value={() => confidence()}
            min={0}
            max={100}
            onSave={(v) => putConfidenceThreshold(props.mode(), v)}
          />
        </Show>
        <Show when={props.mode() === "adult"}>
          <IdentifyEnabledSetting mode={props.mode} />
        </Show>
        </SectionSave>
      </Card>
      <Show when={props.mode() === "adult"}>
        <EntityDatabaseSection />
      </Show>
    </>
  );
};

// EntityDatabaseSection shows the parse_studios/parse_performers entity cache
// — counts, per-source manual "Sync now" triggers, and the shared background
// sync interval — moved here from the AI tab (Settings → Connections → AI)
// since it's a library-content admin concern, not an AI/connection one, and
// scoped to Adult only (it exists purely to back Adult filename parsing; no
// other mode reads this cache). The interval setting sits in its OWN Card,
// outside the Advanced tab's SectionSave, with its own standalone Save
// button — same shape as Adult newest rows' "background scan" card
// (AdultRowAdmin.tsx) — so it can be saved independently of the mode-generic
// fields above without an accidental combined commit.
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
