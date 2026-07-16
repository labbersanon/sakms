// Advanced section — the per-mode bounded-integer settings (phash threshold,
// match-confidence threshold, global recheck interval) and the Adult-only
// identify-enabled toggle. Extracted from the original single-file Settings.tsx.

import {
  type Component,
  createEffect,
  createResource,
  createSignal,
  on,
  Show,
} from "solid-js";
import type { Mode } from "../../api/discover";
import {
  fetchConfidenceThreshold,
  fetchIdentifyEnabled,
  fetchPHashThreshold,
  fetchRecheckInterval,
  putConfidenceThreshold,
  putIdentifyEnabled,
  putPHashThreshold,
  putRecheckInterval,
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
    <Card title={`Advanced Settings (${MODE_LABELS[props.mode()]})`}>
      <SectionSave>
      <NumberSetting
        label="Background recheck interval (seconds) — global"
        help="0 turns the background recheck job off (the opt-in default). Any positive number of seconds enables it; a change takes effect on the running loop's next tick, or on next restart if it was off at boot."
        value={() => recheck()}
        min={0}
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
  );
};
