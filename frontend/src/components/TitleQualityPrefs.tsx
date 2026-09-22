// TitleQualityPrefs — quality tier + max resolution for one monitored/
// tracked title. Defaults light up from mode Settings when Inherited.
// Saving a higher preference queues upgrades for on-disk files below the
// new highest tier.

import {
  type Component,
  For,
  Show,
  createEffect,
  createResource,
  createSignal,
  on,
} from "solid-js";
import type { Mode } from "../api/discover";
import {
  fetchTitleQualityPrefs,
  putTitleQualityPrefs,
  type TitleQualityKey,
} from "../api/titlequality";
import { MAX_RESOLUTIONS, QUALITY_TIERS } from "../api/settings";
import { ErrorText, Muted, PillSelector } from "./ui";

const TIER_LABELS: Record<string, string> = {
  low: "Low",
  medium: "Medium",
  high: "High",
  lossless: "Lossless",
};
const RESOLUTION_OPTIONS = MAX_RESOLUTIONS.map(String);
const RESOLUTION_LABELS: Record<string, string> = Object.fromEntries(
  MAX_RESOLUTIONS.map((r) => [String(r), r === 0 ? "No cap" : `${r}p`]),
);

export const TitleQualityPrefs: Component<{
  mode: Mode;
  titleKey: TitleQualityKey;
}> = (props) => {
  const resourceKey = () => ({ mode: props.mode, ...props.titleKey });
  const [prefs, { refetch }] = createResource(resourceKey, (k) =>
    fetchTitleQualityPrefs(k.mode, k),
  );
  const [tiers, setTiers] = createSignal<string[]>([]);
  const [maxRes, setMaxRes] = createSignal(0);
  const [busy, setBusy] = createSignal(false);
  const [writeError, setWriteError] = createSignal("");
  const [upgradeNote, setUpgradeNote] = createSignal("");

  createEffect(
    on(prefs, (p) => {
      if (!p) return;
      setTiers(p.tiers?.length ? [...p.tiers] : ["high", "lossless"]);
      setMaxRes(p.maxResolution ?? 0);
      setWriteError("");
      setUpgradeNote("");
    }),
  );

  const dirty = () => {
    const p = prefs();
    if (!p) return false;
    const a = [...tiers()].sort().join(",");
    const b = [...(p.tiers ?? [])].sort().join(",");
    return a !== b || maxRes() !== p.maxResolution;
  };

  const toggleTier = (t: string, on: boolean) => {
    setTiers((cur) => {
      if (on) return cur.includes(t) ? cur : [...cur, t];
      return cur.filter((x) => x !== t);
    });
  };

  const save = async () => {
    setBusy(true);
    setWriteError("");
    setUpgradeNote("");
    try {
      const out = await putTitleQualityPrefs(props.mode, props.titleKey, {
        tiers: tiers(),
        maxResolution: maxRes(),
      });
      if (out.upgradeQueued) {
        setUpgradeNote(
          `Queued ${out.upgradeQueued} upgrade search${out.upgradeQueued === 1 ? "" : "es"} for lower-quality files.`,
        );
      }
      await refetch();
    } catch (e) {
      setWriteError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const resetToDefaults = async () => {
    setBusy(true);
    setWriteError("");
    setUpgradeNote("");
    try {
      await putTitleQualityPrefs(props.mode, props.titleKey, {
        tiers: [],
        maxResolution: 0,
        clear: true,
      });
      await refetch();
    } catch (e) {
      setWriteError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div class="mb-3 border-t border-border pt-3">
      <p class="mb-1 text-[11px] font-medium uppercase tracking-wide text-muted">
        Quality
      </p>
      <Show when={prefs.error}>
        <ErrorText>{(prefs.error as Error).message}</ErrorText>
      </Show>
      <Show when={!prefs.loading && prefs()}>
        <Muted class="mb-2">
          {prefs()!.inherited
            ? "Using Settings defaults — change below to override for this title."
            : "Custom override for this title."}
        </Muted>
        <PillSelector
          label="Resolution"
          options={RESOLUTION_OPTIONS}
          optionLabels={RESOLUTION_LABELS}
          selected={String(maxRes())}
          onSelect={(r) => setMaxRes(Number(r))}
        />
        <div class="mb-2">
          <span class="mb-1 block text-[11px] font-medium uppercase tracking-wide text-muted">
            Quality tier
          </span>
          <div class="mt-1 flex flex-wrap gap-3">
            <For each={[...QUALITY_TIERS]}>
              {(t) => (
                <label class="flex items-center gap-1.5 text-sm text-fg">
                  <input
                    type="checkbox"
                    aria-label={`Quality tier ${TIER_LABELS[t]}`}
                    checked={tiers().includes(t)}
                    onChange={(e) => toggleTier(t, e.currentTarget.checked)}
                  />
                  {TIER_LABELS[t]}
                </label>
              )}
            </For>
          </div>
        </div>
        <div class="mt-2 flex flex-wrap items-center gap-2">
          <button
            type="button"
            class="rounded bg-accent px-2 py-1 text-xs text-accent-fg disabled:opacity-50"
            disabled={busy() || !dirty() || tiers().length === 0}
            onClick={() => void save()}
          >
            {busy() ? "Saving…" : "Save"}
          </button>
          <Show when={!prefs()!.inherited}>
            <button
              type="button"
              class="rounded px-2 py-1 text-xs text-muted hover:text-fg disabled:opacity-50"
              disabled={busy()}
              onClick={() => void resetToDefaults()}
            >
              Use Settings defaults
            </button>
          </Show>
        </div>
        <Show when={writeError()}>
          <ErrorText>{writeError()}</ErrorText>
        </Show>
        <Show when={upgradeNote()}>
          <Muted class="mt-1">{upgradeNote()}</Muted>
        </Show>
      </Show>
    </div>
  );
};
