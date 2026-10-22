// TitleQualityPrefs — quality and resolution dual sliders for one
// monitored/tracked title. Inherited lights up from mode Settings. Saving a
// higher floor queues upgrades for on-disk files below it.
//
// Claude 2026-10-13: preferred codecs are Settings-only, not a title override.
// Reason: operator codec order is global; leftover library_quality_prefs.codec_rank
//   must not rank this title differently.
// Troubleshooting: PUT writes empty codec_rank; auto-grab uses resolveCodecRank.
// Review if: title-level codec order is added back as an explicit override.

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
  putMovieUpgradeWatch,
  putSeriesUpgradeWatch,
  putTitleQualityPrefs,
  type TitleQualityKey,
} from "../api/titlequality";
import { QUALITY_TIERS } from "../api/settings";
import {
  DualRangeSlider,
  ErrorText,
  Muted,
  RESOLUTION_SLIDER_STOPS,
  Switch,
  resolutionStopsFromWire,
  resolutionStopsToWire,
} from "./ui";

const TIER_LABELS: Record<string, string> = {
  low: "Low",
  medium: "Medium",
  high: "High",
  lossless: "Lossless",
};
const FLOOR_RUNGS = ["480", "720", "1080", "2160"] as const;
const RESOLUTION_SLIDER_LABELS: Record<string, string> = {
  "480": "480p",
  "720": "720p",
  "1080": "1080p",
  "2160": "2160p",
};
const synthesizeFloors = (floor: string): Record<string, string> =>
  Object.fromEntries(FLOOR_RUNGS.map((r) => [r, floor]));

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
  const [featureUpgrade, setFeatureUpgrade] = createSignal(false);
  const [busy, setBusy] = createSignal(false);
  const [writeError, setWriteError] = createSignal("");
  const [upgradeNote, setUpgradeNote] = createSignal("");

  createEffect(
    on(prefs, (p) => {
      if (!p) return;
      const nextFloor = p.floor || "high";
      setFloor(nextFloor);
      setCeiling(p.ceiling || "lossless");
      setMinRes(p.minResolution ?? 0);
      setMaxRes(p.maxResolution ?? 0);
      setFeatureUpgrade(p.featureUpgrade === true);
      setWriteError("");
      setUpgradeNote("");
    }),
  );

  const dirty = () => {
    const p = prefs();
    if (!p) return false;
    return (
      floor() !== (p.floor || "high") ||
      minRes() !== (p.minResolution ?? 0) ||
      maxRes() !== (p.maxResolution ?? 0) ||
      ceiling() !== (p.ceiling || "lossless") ||
      featureUpgrade() !== (p.featureUpgrade === true)
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
        resolutionFloors: synthesizeFloors(floor()),
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
            ? "Using Settings quality and resolution bands until you override."
            : "Quality and resolution bands for unattended grabs of this title."}
        </Muted>
        <DualRangeSlider
          label="Quality"
          options={[...QUALITY_TIERS]}
          optionLabels={TIER_LABELS}
          low={floor()}
          high={ceiling()}
          lowAriaLabel="Quality floor"
          highAriaLabel="Quality ceiling"
          onChange={(lo, hi) => {
            setFloor(lo);
            setCeiling(hi);
          }}
        />
        <DualRangeSlider
          label="Resolution"
          options={[...RESOLUTION_SLIDER_STOPS]}
          optionLabels={RESOLUTION_SLIDER_LABELS}
          low={resolutionStopsFromWire(minRes(), maxRes()).low}
          high={resolutionStopsFromWire(minRes(), maxRes()).high}
          lowAriaLabel="Minimum resolution"
          highAriaLabel="Maximum resolution"
          onChange={(lo, hi) => {
            const next = resolutionStopsToWire(lo, hi);
            setMinRes(next.min);
            setMaxRes(next.max);
          }}
        />
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
        {/* Claude 2026-10-21: below-floor auto-grab turns upgrade-watch on.
            Reason: stopgap import must hunt preferred without a manual toggle.
            Troubleshooting: PUT .../upgrade-watch; grab origin quality-stopgap.
            Review if: Adult gets a matching per-title watch control. */}
        <Show when={prefs()!.upgradeWatchAvailable}>
          <div class="mt-3 flex items-start justify-between gap-3">
            <div>
              <p class="text-xs font-medium text-fg">Watch for better release</p>
              <Muted class="mt-0.5">
                Search each auto-grab cycle until a release meets this title's
                quality minimum. Stops while the file on disk already meets that
                floor. For series, hunts owned episodes only. A below-floor
                auto-grab turns this on automatically.
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
