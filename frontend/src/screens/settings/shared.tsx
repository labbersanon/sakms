// Shared Settings primitives used by every section panel: the MODE_LABELS map,
// the SaveStatus inline line, and the per-panel useSaveStatus signal helper.
// Extracted verbatim from the original single-file Settings.tsx — pieces
// already shared within that file, relocated.

import {
  type Accessor,
  type Component,
  type JSX,
  createContext,
  createSignal,
  onCleanup,
  onMount,
  Show,
  useContext,
} from "solid-js";
import type { Mode } from "../../api/discover";
import { Button, inputClass, labelClass } from "../../components/ui";

export const MODE_LABELS: Record<Mode, string> = {
  movies: "Movies",
  series: "Series",
  adult: "Adult",
};

// Card lives in components/ui.tsx (shared with Discover, not Settings-only) —
// re-exported here so every existing `from "./shared"` import keeps working.
// Do NOT redefine Card in this file: this codebase already had two competing
// Card implementations once (this one, safe; components/ui.tsx's, a raw
// <fieldset>/<legend> pair that visibly straddles its own border) — only one
// of the two ever got fixed, so screens still importing the other one kept
// shipping the bug. One implementation, re-exported, is how that stays fixed.
export { Card } from "../../components/ui";

// SaveStatus renders the inline "saved" / error line every panel's Save button
// pairs with. text is empty until an action runs.
export const SaveStatus: Component<{ text: string; error: boolean }> = (
  props,
) => (
  <Show when={props.text}>
    <span class={`text-sm ${props.error ? "text-danger" : "text-muted"}`}>
      {props.text}
    </span>
  </Show>
);

// useSaveStatus is the tiny per-panel status signal helper.
export function useSaveStatus() {
  const [status, setStatus] = createSignal<{ text: string; error: boolean }>({
    text: "",
    error: false,
  });
  return {
    status,
    saved: () => setStatus({ text: "saved", error: false }),
    failed: (e: unknown) =>
      setStatus({ text: (e as Error).message, error: true }),
    set: (text: string) => setStatus({ text, error: false }),
  };
}

// ---- Auto-grab slot budgets (Usenet and Torrent cards render the same pair) --

// AUTOGRAB_SLOTS_MAX mirrors the PUT handler's own upper bound, so the inputs
// and autoGrabSlotsValid can't drift from what the server accepts.
export const AUTOGRAB_SLOTS_MAX = 100;

// autoGrabSlotsValid is the client-side half of the PUT's range check, wired
// into SectionSaveItem.valid so an out-of-range field blocks Save up front.
// minPerCycle differs per protocol: Usenet needs at least 1, Torrent accepts 0.
export function autoGrabSlotsValid(
  perCycle: number,
  perSeries: number,
  minPerCycle: number,
): boolean {
  return (
    perCycle >= minPerCycle &&
    perCycle <= AUTOGRAB_SLOTS_MAX &&
    perSeries >= 0 &&
    perSeries <= AUTOGRAB_SLOTS_MAX
  );
}

// AutoGrabSlotFields renders the per-cycle / per-series slot inputs shared by
// the Usenet and Torrent auto-grab cards. `protocol` prefixes the aria-labels
// so both cards' fields stay distinguishable on the Download tab.
export const AutoGrabSlotFields: Component<{
  protocol: "Usenet" | "Torrent";
  minPerCycle: number;
  disabled: boolean;
  perCycle: number;
  perSeries: number;
  onChange: (patch: { perCycle?: number; perSeries?: number }) => void;
}> = (props) => (
  <div class="mt-4 grid gap-3 sm:grid-cols-2">
    <label class="block">
      <span class={labelClass}>Slots per cycle</span>
      <input
        type="number"
        min={props.minPerCycle}
        max={AUTOGRAB_SLOTS_MAX}
        class={`${inputClass} mt-1`}
        aria-label={`${props.protocol} slots per cycle`}
        disabled={props.disabled}
        value={props.perCycle}
        onInput={(e) =>
          props.onChange({ perCycle: Number(e.currentTarget.value) || 0 })
        }
      />
    </label>
    <label class="block">
      <span class={labelClass}>Slots per series per cycle</span>
      <input
        type="number"
        min={0}
        max={AUTOGRAB_SLOTS_MAX}
        class={`${inputClass} mt-1`}
        aria-label={`${props.protocol} slots per series per cycle`}
        disabled={props.disabled}
        value={props.perSeries}
        onInput={(e) =>
          props.onChange({ perSeries: Number(e.currentTarget.value) || 0 })
        }
      />
    </label>
  </div>
);

// ---- Section-level batched save (one Save button per Settings tab) ----------
//
// useSectionSave batches the SAVE TRIGGER only — it never centralizes or merges
// per-row field/secret state. Each child keeps its own local signals and its own
// request-body construction (e.g. ConnectionRow's keyTouched + buildConnection-
// UpsertBody, so an untouched API key is OMITTED entirely, never sent as "" —
// the safety-critical three-state secret invariant this project's #1 incident
// class turns on). A child registers { id, label, dirty, save } with the
// enclosing SectionSave; the section's single Save button is enabled while any
// child is dirty and, on click, fires each dirty child's OWN save() concurrently
// (one PUT per dirty row/field-group, never a merged payload). Each save() still
// sets its own inline SaveStatus (so per-row failure visibility isn't regressed)
// AND throws on failure so the section can additionally report which rows failed.

// A save has THREE outcomes, not two. Resolving means saved; rejecting means
// failed; resolving with "not-applied" is the third: the change was neither
// stored nor applied, but nothing is wrong with it and retrying is the whole
// remedy. The torrent settings PUT's 409 is the case this exists for — the
// engine couldn't restart while a download was mid-flight. A child in that
// situation renders its own explanation, and the section must not contradict
// it: rejecting would print a red "failed: …" summary next to a card saying
// "try again in a moment", and resolving plainly would claim a success that
// never happened.
export type SectionSaveOutcome = void | "not-applied";

export interface SectionSaveItem {
  id: string;
  label: string;
  dirty: Accessor<boolean>;
  // valid is optional — most registered items (ConnectionRow, toggles, the
  // AI form) have nothing to client-side validate, so omitting it defaults
  // to "always valid". Fields with a client-checkable range (NumberSetting)
  // supply it so the section's one Save button disables itself the moment
  // ANY registered item is out of range, instead of letting the operator
  // click Save and find out via an error afterward. save()'s own
  // out-of-range guard stays as defense-in-depth (e.g. a direct call
  // bypassing the disabled button in a test), but in normal use it becomes
  // unreachable once this is wired up, since the button can't be clicked.
  valid?: Accessor<boolean>;
  // save runs the child's own existing save logic (its own body-building, its own
  // inline status). It MUST reject on failure — including client-side validation
  // early-outs — so the section summary never falsely reports "saved". A refusal
  // that is not a failure resolves with "not-applied" instead of rejecting; see
  // SectionSaveOutcome for when that applies and what the section does with it.
  save: () => Promise<SectionSaveOutcome>;
}

interface SectionSaveRegistry {
  register: (item: SectionSaveItem) => void;
  unregister: (id: string) => void;
}

const SectionSaveContext = createContext<SectionSaveRegistry>();

// useSectionSaveItem registers a child with the enclosing SectionSave (if any)
// for the child's lifetime. Returns an accessor that is true when a section
// context was found — the child then hides its own inline Save button and lets
// the section's one button drive it — and false when standalone, in which case
// the child keeps its own Save button (e.g. AdultRowAdmin's DurationSetting
// cards, which are deliberately NOT batched). Mirrors useScreenTabs' register/
// cleanup shape.
export function useSectionSaveItem(item: SectionSaveItem): () => boolean {
  const reg = useContext(SectionSaveContext);
  if (!reg) return () => false;
  onMount(() => reg.register(item));
  onCleanup(() => reg.unregister(item.id));
  return () => true;
}

// SectionSave provides the registry to its descendants and renders them followed
// by the one section-level Save button + status. Disabled until some child is
// dirty, AND disabled again the moment any registered item reports itself
// invalid (see SectionSaveItem.valid) — one out-of-range field blocks the
// whole batch from saving, not just its own row, so the operator sees the
// block before clicking rather than an error after. A click runs every dirty
// child's own save() via allSettled so one failure never skips the rest,
// then reports which (if any) failed — or, for a child that resolved with
// "not-applied", which were refused rather than failed (see SectionSaveOutcome).
export const SectionSave: Component<{
  label?: string;
  children: JSX.Element;
}> = (props) => {
  const [items, setItems] = createSignal<SectionSaveItem[]>([]);
  const registry: SectionSaveRegistry = {
    register: (item) =>
      setItems((prev) => [...prev.filter((i) => i.id !== item.id), item]),
    unregister: (id) => setItems((prev) => prev.filter((i) => i.id !== id)),
  };
  const dirty = () => items().some((i) => i.dirty());
  const anyInvalid = () => items().some((i) => i.valid && !i.valid());
  const status = useSaveStatus();
  const saveAll = async () => {
    const pending = items().filter((i) => i.dirty());
    if (pending.length === 0) return;
    status.set("saving…");
    const results = await Promise.allSettled(pending.map((i) => i.save()));
    const failed = pending.filter(
      (_, idx) => results[idx]?.status === "rejected",
    );
    const notApplied = pending.filter((_, idx) => {
      const r = results[idx];
      return r?.status === "fulfilled" && r.value === "not-applied";
    });
    // Order matters: a real failure outranks a retryable refusal, and both
    // outrank "saved". The refusal line is deliberately NOT status.failed() —
    // it is not red, because the child that reported it is simultaneously
    // rendering its own calmer explanation of why retrying is all that's
    // needed. It still isn't status.saved() either: nothing was stored.
    if (failed.length)
      status.failed(
        new Error(`failed: ${failed.map((i) => i.label).join(", ")}`),
      );
    else if (notApplied.length)
      status.set(`not applied: ${notApplied.map((i) => i.label).join(", ")}`);
    else status.saved();
  };
  return (
    <SectionSaveContext.Provider value={registry}>
      {props.children}
      <div class="mt-2 flex items-center gap-2">
        <Button
          variant="primary"
          disabled={!dirty() || anyInvalid()}
          onClick={() => void saveAll()}
        >
          {props.label ?? "Save"}
        </Button>
        <SaveStatus text={status.status().text} error={status.status().error} />
      </div>
    </SectionSaveContext.Provider>
  );
};
