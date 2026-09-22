// TitleQualityPrefs — minimum quality tier + minimum resolution for one
// monitored/tracked title. Defaults light up from mode Settings (quality
// floor) when Inherited; resolution minimum defaults to Any (0).
// Saving a higher floor queues upgrades for on-disk files below it.

import {
  type Component,
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
  MAX_RESOLUTIONS.map((r) => [String(r), r === 0 ? "Any" : `${r}p+`]),
);

export const TitleQualityPrefs: Component<{
  mode: Mode;
  titleKey: TitleQualityKey;
}> = (props) => {
  const resourceKey = () => ({ mode: props.mode, ...props.titleKey });
  const [prefs, { refetch }] = createResource(resourceKey, (k) =>
    fetchTitleQualityPrefs(k.mode, k),
  );
  const [floor, setFloor] = createSignal("high");
  const [minRes, setMinRes] = createSignal(0);
  const [busy, setBusy] = createSignal(false);
  const [writeError, setWriteError] = createSignal("");
  const [upgradeNote, setUpgradeNote] = createSignal("");

  createEffect(
    on(prefs, (p) => {
      if (!p) return;
      setFloor(p.floor || "high");
      setMinRes(p.minResolution ?? 0);
      setWriteError("");
      setUpgradeNote("");
    }),
  );

  const dirty = () => {
    const p = prefs();
    if (!p) return false;
    return floor() !== (p.floor || "high") || minRes() !== (p.minResolution ?? 0);
  };

  const save = async () => {
    setBusy(true);
    setWriteError("");
    setUpgradeNote("");
    try {
      const out = await putTitleQualityPrefs(props.mode, props.titleKey, {
        floor: floor(),
        minResolution: minRes(),
      });
      if (out.upgradeQueued) {
        setUpgradeNote(
          `Queued ${out.upgradeQueued} upgrade search${out.upgradeQueued === 1 ? "" : "es"} for below-minimum files.`,
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
        floor: "high",
        minResolution: 0,
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
            ? "Using Settings quality floor — resolution minimum is Any until you override."
            : "Minimum quality and resolution for unattended grabs of this title."}
        </Muted>
        <PillSelector
          label="Minimum resolution"
          options={RESOLUTION_OPTIONS}
          optionLabels={RESOLUTION_LABELS}
          selected={String(minRes())}
          onSelect={(r) => setMinRes(Number(r))}
        />
        <PillSelector
          label="Minimum quality"
          options={[...QUALITY_TIERS]}
          optionLabels={TIER_LABELS}
          selected={floor()}
          onSelect={setFloor}
        />
        <div class="mt-2 flex flex-wrap items-center gap-2">
          <button
            type="button"
            class="rounded bg-accent px-2 py-1 text-xs text-accent-fg disabled:opacity-50"
            disabled={busy() || !dirty()}
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
