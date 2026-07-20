// AI section — BYOAI fallback toggle + provider/model selection.
// Extracted from the original single-file Settings.tsx.
//
// With DB-first filename parsing (internal/parseentity), the AI path is now an
// optional fallback — off by default. The toggle at the top of the AI card
// gates provider+model+connection fields: when disabled, those fields are shown
// dimmed (so the operator can still pre-configure them) but the backend
// buildAIClient returns nil and ParseFilename is never called.
//
// The "Entity Database" card (parse_studios/parse_performers cache admin,
// Phase 6) has MOVED to Settings → Advanced → Adult — it's a library-content
// concern scoped to Adult, not an AI/connection one. See Advanced.tsx's
// EntityDatabaseSection.

import {
  type Component,
  createEffect,
  createResource,
  createSignal,
  For,
  Show,
  untrack,
} from "solid-js";
import {
  AI_PROVIDER_MODELS,
  AI_PROVIDERS,
  fetchAIFallbackEnabled,
  fetchAIModel,
  fetchAIProvider,
  fetchConnections,
  fetchNetscanKnown,
  fetchOllamaModels,
  putAIFallbackEnabled,
  putAIModel,
  putAIProvider,
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
// connection rows all sit INSIDE one SectionSave (so their registrations see
// its context). The Entity Database card used to live here as a sibling,
// outside the batched Save — it's moved to Advanced → Adult now (see above).
export const AISection: Component = () => (
  <SectionSave>
    <AIProviderModelCard />
  </SectionSave>
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
  // useCustom is cloud-provider-only: true when the model <select> is showing
  // the "Other (type manually)" option and its revealed text input, rather
  // than one of AI_PROVIDER_MODELS' curated options.
  const [useCustom, setUseCustom] = createSignal(false);
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
  // Recomputes useCustom whenever the provider changes or the stored model
  // first resolves — never on every keystroke (mdl() is read untracked) so
  // typing in the custom text box doesn't fight this effect. Covers both the
  // initial "stored model not in the curated list" load case and switching
  // providers, where the previous model almost never matches the new
  // provider's curated list — mirrors the free-text field's old behavior of
  // never clearing the model value out from under the operator.
  createEffect(() => {
    const p = prov();
    const loaded = model();
    if (p === "ollama" || loaded === undefined) return;
    const curated =
      AI_PROVIDER_MODELS[p as keyof typeof AI_PROVIDER_MODELS] ?? [];
    const current = untrack(mdl);
    setUseCustom(!curated.some((o) => o.value === current));
  });

  const status = useSaveStatus();
  const save = async () => {
    try {
      await putAIFallbackEnabled(enabled());
      // Only write provider/model when the resources have loaded — prevents
      // overwriting stored values with the unseeded "ollama"/"" defaults on
      // a pre-resolve Save click.
      const resolvedProvider = provider();
      const resolvedModel = model();
      if (resolvedProvider !== undefined && resolvedModel !== undefined) {
        await putAIProvider(prov());
        await putAIModel(mdl());
      }
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

  // ollamaModels live-fetches the model list from the SAVED Ollama connection
  // URL (byService()["ollama"]?.url) whenever the provider is ollama — never
  // the live in-progress URL being edited inside ConnectionRow below, which
  // isn't reachable from here without an unplanned state-lift (see plan ADR).
  // No source (undefined) when the provider isn't ollama, or no URL is saved
  // yet, so the resource simply doesn't fetch in either case.
  const [ollamaModels] = createResource(
    () =>
      prov() === "ollama"
        ? byService()["ollama"]?.url || undefined
        : undefined,
    (url) => fetchOllamaModels(url),
  );
  // ollamaOptions appends the stored model as a selectable option, labeled
  // "(not currently installed)", when it's reachable but no longer in the
  // live /api/tags result — never drops it from the dropdown. Guards on
  // ollamaModels.error (a safe property read) before calling ollamaModels()
  // — Solid resources re-throw on invocation once the fetcher has errored,
  // and calling it unguarded here would throw mid-render (the exact bug
  // class documented in this project's CLAUDE.md re: GrabDialog).
  const ollamaOptions = (): { value: string; label: string }[] => {
    const list = ollamaModels.error ? [] : (ollamaModels() ?? []);
    const opts = list.map((m) => ({ value: m, label: m }));
    const current = mdl();
    if (current && !list.includes(current)) {
      opts.push({
        value: current,
        label: `${current} (not currently installed)`,
      });
    }
    return opts;
  };
  const cloudModelOptions = (): { value: string; label: string }[] => {
    const p = prov();
    if (p === "ollama") return [];
    return AI_PROVIDER_MODELS[p as keyof typeof AI_PROVIDER_MODELS] ?? [];
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
              <Show
                when={prov() === "ollama"}
                fallback={
                  <>
                    <select
                      class={`${inputClass} mt-1`}
                      aria-label="AI model"
                      value={useCustom() ? "__custom__" : mdl()}
                      onChange={(e) => {
                        const v = e.currentTarget.value;
                        if (v === "__custom__") {
                          setUseCustom(true);
                        } else {
                          setUseCustom(false);
                          setMdl(v);
                        }
                        setDirty(true);
                      }}
                      disabled={!enabled()}
                    >
                      <For each={cloudModelOptions()}>
                        {(o) => <option value={o.value}>{o.label}</option>}
                      </For>
                      <option value="__custom__">Other (type manually)</option>
                    </select>
                    <Show when={useCustom()}>
                      <input
                        type="text"
                        class={`${inputClass} mt-1`}
                        placeholder="e.g. gpt-4o-mini, gemini-2.5-flash, claude-haiku-4-5"
                        aria-label="Custom AI model"
                        value={mdl()}
                        onInput={(e) => {
                          setMdl(e.currentTarget.value);
                          setDirty(true);
                        }}
                        disabled={!enabled()}
                      />
                    </Show>
                  </>
                }
              >
                <select
                  class={`${inputClass} mt-1`}
                  aria-label="Ollama model"
                  value={mdl()}
                  onChange={(e) => {
                    setMdl(e.currentTarget.value);
                    setDirty(true);
                  }}
                  disabled={!enabled() || ollamaModels.loading}
                >
                  <For each={ollamaOptions()}>
                    {(o) => <option value={o.value}>{o.label}</option>}
                  </For>
                </select>
                <Show when={ollamaModels.loading}>
                  <Muted class="mt-1 text-xs">Loading models…</Muted>
                </Show>
                <Show when={ollamaModels.error}>
                  <div class="mt-1 text-xs text-danger">
                    Couldn't reach Ollama:{" "}
                    {(ollamaModels.error as Error)?.message ??
                      "unknown error"}
                  </div>
                </Show>
              </Show>
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
