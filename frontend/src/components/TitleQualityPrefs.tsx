// TitleQualityPrefs — per-resolution bitrate floors + minimum height for one
// monitored/tracked title. Inherited lights up from mode Settings (same 480/
// 720/1080/2160 pills); resolution minimum defaults to Any (0). Saving a
// higher floor queues upgrades for on-disk files below it.

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
  putMovieUpgradeWatch,
  putSeriesUpgradeWatch,
  putTitleQualityPrefs,
  type TitleQualityKey,
} from "../api/titlequality";
import { MAX_RESOLUTIONS, QUALITY_TIERS } from "../api/settings";
import { ErrorText, Muted, PillSelector, Switch } from "./ui";

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
const FLOOR_RUNGS = ["480", "720", "1080", "2160"] as const;
const CODEC_RANK_DEFAULT = ["av1", "x265", "x264"];
const CODEC_LABELS: Record<string, string> = {
  av1: "AV1",
  x265: "x265",
  x264: "x264",
};
const MAX_RESOLUTION_LABELS: Record<string, string> = Object.fromEntries(
  MAX_RESOLUTIONS.map((r) => [String(r), r === 0 ? "No cap" : `${r}p`]),
);
const synthesizeFloors = (floor: string): Record<string, string> =>
  Object.fromEntries(FLOOR_RUNGS.map((r) => [r, floor]));
const floorsEqual = (a: Record<string, string>, b: Record<string, string>) =>
  FLOOR_RUNGS.every((r) => (a[r] ?? "") === (b[r] ?? ""));

export const TitleQualityPrefs: Component<{
  mode: Mode;
  titleKey: TitleQualityKey;
}> = (props) => {
  const resourceKey = () => ({ mode: props.mode, ...props.titleKey });
  const [prefs, { refetch }] = createResource(resourceKey, (k) =>
    fetchTitleQualityPrefs(k.mode, k),
  );
  const [floor, setFloor] = createSignal("high");
  const [ceiling, setCeiling] = createSignal("lossless");
  const [minRes, setMinRes] = createSignal(0);
  const [maxRes, setMaxRes] = createSignal(0);
  const [codecRank, setCodecRank] = createSignal<string[]>([...CODEC_RANK_DEFAULT]);
  const [featureUpgrade, setFeatureUpgrade] = createSignal(false);
  const [resFloors, setResFloors] = createSignal<Record<string, string>>(
    synthesizeFloors("high"),
  );
  const [busy, setBusy] = createSignal(false);
  const [writeError, setWriteError] = createSignal("");
  const [upgradeNote, setUpgradeNote] = createSignal("");

  createEffect(
    on(prefs, (p) => {
      if (!p) return;
      const nextFloor = p.floor || "high";
      setFloor(nextFloor);
      setCeiling(p.ceiling || "lossless");
      setResFloors({
        ...synthesizeFloors(nextFloor),
        ...(p.resolutionFloors ?? {}),
      });
      setMinRes(p.minResolution ?? 0);
      setMaxRes(p.maxResolution ?? 0);
      setCodecRank(p.codecRank?.length ? [...p.codecRank] : [...CODEC_RANK_DEFAULT]);
      setFeatureUpgrade(p.featureUpgrade === true);
      setWriteError("");
      setUpgradeNote("");
    }),
  );

  const savedFloors = () => {
    const p = prefs();
    if (!p) return synthesizeFloors("high");
    return { ...synthesizeFloors(p.floor || "high"), ...(p.resolutionFloors ?? {}) };
  };

  const dirty = () => {
    const p = prefs();
    if (!p) return false;
    return (
      floor() !== (p.floor || "high") ||
      minRes() !== (p.minResolution ?? 0) ||
      maxRes() !== (p.maxResolution ?? 0) ||
      ceiling() !== (p.ceiling || "lossless") ||
      featureUpgrade() !== (p.featureUpgrade === true) ||
      codecRank().join(",") !== (p.codecRank ?? CODEC_RANK_DEFAULT).join(",") ||
      !floorsEqual(resFloors(), savedFloors())
    );
  };

  const save = async () => {
    setBusy(true);
    setWriteError("");
    setUpgradeNote("");
    try {
      const out = await putTitleQualityPrefs(props.mode, props.titleKey, {
        floor: floor(),
        ceiling: ceiling(),
        minResolution: minRes(),
        maxResolution: maxRes(),
        resolutionFloors: resFloors(),
        codecRank: codecRank(),
        featureUpgrade: featureUpgrade(),
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

  const setWatch = async (next: boolean) => {
    setBusy(true);
    setWriteError("");
    try {
      if (props.mode === "series") {
        await putSeriesUpgradeWatch(props.titleKey, next);
      } else {
        const tmdbId = props.titleKey.tmdbId;
        if (tmdbId == null) return;
        await putMovieUpgradeWatch(tmdbId, next);
      }
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
            ? "Using Settings bitrate floors per resolution — resolution minimum is Any until you override."
            : "Bitrate floors per resolution and minimum height for unattended grabs of this title."}
        </Muted>
        <PillSelector
          label="Quality floor"
          options={[...QUALITY_TIERS]}
          optionLabels={TIER_LABELS}
          selected={floor()}
          onSelect={setFloor}
        />
        <PillSelector
          label="Quality ceiling"
          options={[...QUALITY_TIERS]}
          optionLabels={TIER_LABELS}
          selected={ceiling()}
          onSelect={setCeiling}
        />
        <PillSelector
          label="Minimum resolution"
          options={RESOLUTION_OPTIONS}
          optionLabels={RESOLUTION_LABELS}
          selected={String(minRes())}
          onSelect={(r) => setMinRes(Number(r))}
        />
        <PillSelector
          label="Maximum resolution"
          options={RESOLUTION_OPTIONS}
          optionLabels={MAX_RESOLUTION_LABELS}
          selected={String(maxRes())}
          onSelect={(r) => setMaxRes(Number(r))}
        />
        <div class="mb-2">
          <For each={FLOOR_RUNGS}>
            {(r) => (
              <div role="group" aria-label={`${r}p bitrate floor`}>
                <PillSelector
                  label={`${r}p`}
                  options={[...QUALITY_TIERS]}
                  optionLabels={TIER_LABELS}
                  selected={resFloors()[r] ?? "high"}
                  onSelect={(t) => setResFloors((prev) => ({ ...prev, [r]: t }))}
                />
              </div>
            )}
          </For>
        </div>
        <div class="mb-2">
          <p class="mb-1 text-[11px] font-medium uppercase tracking-wide text-muted">
            Preferred codecs
          </p>
          <ol class="space-y-1">
            <For each={codecRank()}>
              {(c, i) => (
                <li class="flex items-center gap-2">
                  <span class="w-16 text-sm text-fg">{CODEC_LABELS[c] ?? c}</span>
                  <button
                    type="button"
                    class="rounded px-2 py-0.5 text-xs text-muted hover:text-fg disabled:opacity-40"
                    aria-label={`Move ${CODEC_LABELS[c] ?? c} up`}
                    disabled={i() === 0}
                    onClick={() =>
                      setCodecRank((prev) => {
                        const next = [...prev];
                        const j = i() - 1;
                        if (j < 0) return prev;
                        const a = next[i()];
                        const b = next[j];
                        if (a === undefined || b === undefined) return prev;
                        next[i()] = b;
                        next[j] = a;
                        return next;
                      })
                    }
                  >
                    Up
                  </button>
                  <button
                    type="button"
                    class="rounded px-2 py-0.5 text-xs text-muted hover:text-fg disabled:opacity-40"
                    aria-label={`Move ${CODEC_LABELS[c] ?? c} down`}
                    disabled={i() === codecRank().length - 1}
                    onClick={() =>
                      setCodecRank((prev) => {
                        const next = [...prev];
                        const j = i() + 1;
                        if (j >= next.length) return prev;
                        const a = next[i()];
                        const b = next[j];
                        if (a === undefined || b === undefined) return prev;
                        next[i()] = b;
                        next[j] = a;
                        return next;
                      })
                    }
                  >
                    Down
                  </button>
                </li>
              )}
            </For>
          </ol>
        </div>
        <div class="mb-3 flex items-start justify-between gap-3">
          <div>
            <p class="text-xs font-medium text-fg">Upgrade for HDR / more channels</p>
            <Muted class="mt-0.5">
              Off by default. Hunt a sitting SDR or stereo file for HDR / more
              channels.
            </Muted>
          </div>
          <Switch
            checked={featureUpgrade()}
            ariaLabel="Upgrade for HDR / more channels"
            onChange={setFeatureUpgrade}
          />
        </div>
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
        {/* Claude 2026-09-29: opt-in upgrade-watch on the quality card.
            Reason: owned-title hunt for movies and series; GET quality-prefs
              carries upgradeWatchAvailable so unowned Discover titles hide it.
            Troubleshooting: PUT .../upgrade-watch; sixth retry-cycle pass.
            Review if: Adult gets a matching per-title watch control. */}
        <Show when={prefs()!.upgradeWatchAvailable}>
          <div class="mt-3 flex items-start justify-between gap-3">
            <div>
              <p class="text-xs font-medium text-fg">Watch for better release</p>
              <Muted class="mt-0.5">
                Search each auto-grab cycle until a release meets this title's
                quality minimum. Stops while the file on disk already meets that
                floor. For series, hunts owned episodes only.
              </Muted>
            </div>
            <Switch
              checked={prefs()!.upgradeWatch === true}
              disabled={busy()}
              ariaLabel="Watch for better release"
              onChange={(next) => void setWatch(next)}
            />
          </div>
        </Show>
      </Show>
    </div>
  );
};
