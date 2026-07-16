// Shared Settings primitives used by every section panel: the MODE_LABELS map,
// the Card fieldset frame, the SaveStatus inline line, and the per-panel
// useSaveStatus signal helper. Extracted verbatim from the original single-file
// Settings.tsx — pieces already shared within that file, relocated.

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
import { Button } from "../../components/ui";

export const MODE_LABELS: Record<Mode, string> = {
  movies: "Movies",
  series: "Series",
  adult: "Adult",
};

// Card is the bordered panel frame every settings panel shares. Deliberately
// NOT a <fieldset>/<legend> pair: browsers render <legend> straddling the
// fieldset's own top border by default (half above it, half below) — with
// this card's rounded border and bg-surface fill, that reads as the title
// text bleeding out of the box into the page background behind it, on
// every single Card across every Settings tab. A plain div + heading avoids
// that native straddle-the-border behavior entirely.
export const Card: Component<{ title: string; children: JSX.Element }> = (
  props,
) => (
  <div class="mb-4 rounded-xl border border-border bg-surface p-4">
    <h3 class="mb-2 px-2 text-sm font-semibold text-fg">{props.title}</h3>
    {props.children}
  </div>
);

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

export interface SectionSaveItem {
  id: string;
  label: string;
  dirty: Accessor<boolean>;
  // save runs the child's own existing save logic (its own body-building, its own
  // inline status). It MUST reject on failure — including client-side validation
  // early-outs — so the section summary never falsely reports "saved".
  save: () => Promise<void>;
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
// the child keeps its own Save button (e.g. AdultRowAdmin's NumberSetting, which
// is deliberately NOT batched). Mirrors useScreenTabs' register/cleanup shape.
export function useSectionSaveItem(item: SectionSaveItem): () => boolean {
  const reg = useContext(SectionSaveContext);
  if (!reg) return () => false;
  onMount(() => reg.register(item));
  onCleanup(() => reg.unregister(item.id));
  return () => true;
}

// SectionSave provides the registry to its descendants and renders them followed
// by the one section-level Save button + status. Disabled until some child is
// dirty; a click runs every dirty child's own save() via allSettled so one
// failure never skips the rest, then reports which (if any) failed.
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
  const status = useSaveStatus();
  const saveAll = async () => {
    const pending = items().filter((i) => i.dirty());
    if (pending.length === 0) return;
    status.set("saving…");
    const results = await Promise.allSettled(pending.map((i) => i.save()));
    const failed = pending.filter(
      (_, idx) => results[idx]?.status === "rejected",
    );
    if (failed.length)
      status.failed(
        new Error(`failed: ${failed.map((i) => i.label).join(", ")}`),
      );
    else status.saved();
  };
  return (
    <SectionSaveContext.Provider value={registry}>
      {props.children}
      <div class="mt-2 flex items-center gap-2">
        <Button
          variant="primary"
          disabled={!dirty()}
          onClick={() => void saveAll()}
        >
          {props.label ?? "Save"}
        </Button>
        <SaveStatus text={status.status().text} error={status.status().error} />
      </div>
    </SectionSaveContext.Provider>
  );
};
