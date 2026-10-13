// Library section — the per-mode (Movies/Series) panels: root folder, search
// quality preferences, file/folder naming preset, and kids classification path.
// Extracted from the original single-file Settings.tsx.
//
// Plus one SERIES-ONLY panel: new-season discovery
// (SeriesNewSeasonDiscoverySection). It lives here rather than under Settings →
// Download → Usenet deliberately — it is a Series-library behavior, and the
// Usenet sub-tab's own doc says that page must not grow unrelated rows. That the backend gates
// the resulting dispatch on the usenet auto-grab flag is an implementation
// detail of dispatch, not a reason to file the control under Usenet.

import {
  type Component,
  createEffect,
  createResource,
  createSignal,
  on,
  onCleanup,
  onMount,
  For,
  Show,
} from "solid-js";
import type { Mode } from "../../api/discover";
import {
  fetchSeriesNewSeasonDiscovery,
  putSeriesNewSeasonDiscovery,
} from "../../api/seasons";
import {
  LIBRARY_MODE_SERVICES,
  NAMING_PRESETS,
  QUALITY_TIERS,
  fetchKidsRootPath,
  fetchLibraryRootFolder,
  fetchProposeNestedMoves,
  fetchRenameScanStatus,
  fetchNamingPreset,
  fetchQualityPrefs,
  postLibraryRescan,
  putKidsRootPath,
  putLibraryRootFolder,
  putNamingPreset,
  putProposeNestedMoves,
  putQualityPrefs,
  testLibraryRootFolder,
} from "../../api/settings";
import {
  Button,
  ErrorText,
  Muted,
  DualRangeSlider,
  PillSelector,
  RESOLUTION_SLIDER_STOPS,
  resolutionStopsFromWire,
  resolutionStopsToWire,
  Switch,
  inputClass,
  labelClass,
} from "../../components/ui";
import { FolderPicker } from "../../components/FolderPicker";
import { ConnectionServiceTable } from "./ConnectionRow";
import {
  Card,
  MODE_LABELS,
  SaveStatus,
  SectionSave,
  useSaveStatus,
  useSectionSaveItem,
} from "./shared";

function scanStatusLine(s: {
  running?: boolean;
  total?: number;
  current?: number;
  name?: string;
  phase?: string;
  error?: string;
}): string {
  if (s.running) {
    const n = s.total ? `${s.current ?? 0}/${s.total}` : "…";
    return `Scanning ${n}${s.name ? ` · ${s.name}` : ""}`;
  }
  if (s.phase === "error") {
    return s.error || "Scan failed";
  }
  if (s.phase === "done") {
    return "Scan finished — identified titles are in Library.";
  }
  return "";
}

// ---- Per-mode: library root folder ----------------------------------------

export const LibraryRootFolderSection: Component<{ mode: () => Mode }> = (
  props,
) => {
  const [current] = createResource(props.mode, fetchLibraryRootFolder);
  const [path, setPath] = createSignal("");
  const [dirty, setDirty] = createSignal(false);
  createEffect(
    on(current, (p) => {
      if (p !== undefined) {
        setPath(p ?? "");
        setDirty(false);
      }
    }),
  );
  const status = useSaveStatus();
  const [scanLine, setScanLine] = createSignal("");
  let scanTimer: number | undefined;
  const stopScanPoll = () => {
    if (scanTimer !== undefined) {
      window.clearInterval(scanTimer);
      scanTimer = undefined;
    }
  };
  const pollScan = () => {
    stopScanPoll();
    scanTimer = window.setInterval(() => {
      void fetchRenameScanStatus(props.mode()).then((s) => {
        const line = scanStatusLine(s);
        if (s.running) {
          setScanLine(line);
          return;
        }
        stopScanPoll();
        if (line) {
          setScanLine(line);
        }
      });
    }, 1000);
  };
  onCleanup(stopScanPoll);
  const save = async () => {
    try {
      await putLibraryRootFolder(props.mode(), path());
      setDirty(false);
      status.saved();
      if (path().trim() !== "") {
        setScanLine("Starting scan…");
        pollScan();
      }
    } catch (e) {
      status.failed(e);
      throw e;
    }
  };
  const rescan = async () => {
    try {
      await postLibraryRescan(props.mode());
      setScanLine("Starting scan…");
      pollScan();
    } catch (e) {
      status.failed(e);
    }
  };
  // testFailed red-tints the path input after a failed path test; cleared on a
  // passing test or once the operator edits the path (the old result no longer
  // applies to the new value).
  const [testFailed, setTestFailed] = createSignal(false);
  const testStatus = useSaveStatus();
  const testPath = async () => {
    testStatus.set("testing…");
    try {
      const r = await testLibraryRootFolder(props.mode(), path());
      if (r.ok) {
        setTestFailed(false);
        testStatus.set("✓ ok");
      } else {
        setTestFailed(true);
        // This endpoint's errors ARE safe to show directly (path does not
        // exist / not a directory / not writable / …), unlike the stored
        // connection test's fixed generic string.
        testStatus.failed(new Error(r.error || "path test failed"));
      }
    } catch (e) {
      testStatus.failed(e);
    }
  };
  const setPathDirty = (p: string) => {
    setPath(p);
    setDirty(true);
    setTestFailed(false);
  };
  const batched = useSectionSaveItem({
    id: "library-root",
    label: "root folder",
    dirty,
    save,
  });
  return (
    <Card title={`${MODE_LABELS[props.mode()]} library`}>
      <form onSubmit={(e) => (e.preventDefault(), void save().catch(() => {}))}>
        <label class="block">
          <span class={labelClass}>Root folder</span>
          <FolderPicker
            value={path}
            onChange={setPathDirty}
            ariaLabel="Library root folder"
            placeholder={`/path/to/${MODE_LABELS[props.mode()]}`}
            invalid={testFailed}
          />
        </label>
        <div class="mt-3 flex items-center gap-2">
          <Show when={!batched()}>
            <Button variant="primary" type="submit">
              Save
            </Button>
          </Show>
          <Button onClick={() => void testPath()}>Test</Button>
          <Button
            type="button"
            aria-label="Rescan library"
            onClick={() => void rescan()}
          >
            Rescan
          </Button>
          <SaveStatus
            text={status.status().text}
            error={status.status().error}
          />
          <SaveStatus
            text={testStatus.status().text}
            error={testStatus.status().error}
          />
        </div>
      </form>
      <Show when={scanLine()}>
        <p class="mt-2 text-sm text-fg" aria-live="polite">
          {scanLine()}
        </p>
      </Show>
      <Muted class="mt-2">
        Where Rename/Purge/Dedup and Search's Check &amp; Import look for and
        place {MODE_LABELS[props.mode()]} files — no{" "}
        {props.mode() === "movies"
          ? "Radarr"
          : props.mode() === "series"
            ? "Sonarr"
            : "Whisparr"}{" "}
        involved.
      </Muted>
    </Card>
  );
};

// ---- Per-mode: quality preferences ----------------------------------------

// QUALITY_TIER_LABELS/PROTOCOL_LABELS label DualRangeSlider and PillSelector.
// QUALITY_TIERS (api/settings.ts) are the wire values.
const QUALITY_TIER_LABELS: Record<string, string> = {
  low: "Low",
  medium: "Medium",
  high: "High",
  lossless: "Lossless",
};
const RESOLUTION_SLIDER_LABELS: Record<string, string> = {
  "480": "480p",
  "720": "720p",
  "1080": "1080p",
  "2160": "2160p",
};
const FLOOR_RUNGS = ["480", "720", "1080", "2160"] as const;
const synthesizeFloors = (floor: string): Record<string, string> =>
  Object.fromEntries(FLOOR_RUNGS.map((r) => [r, floor]));
const PROTOCOL_OPTIONS = ["", "usenet", "torrent"];
const PROTOCOL_LABELS: Record<string, string> = {
  "": "No preference",
  usenet: "Usenet",
  torrent: "Torrent",
};
const CODEC_RANK_DEFAULT = ["av1", "x265", "x264"];
const CODEC_LABELS: Record<string, string> = {
  av1: "AV1",
  x265: "x265",
  x264: "x264",
};
const expandBand = (floor: string, ceiling: string): string[] => {
  const a = QUALITY_TIERS.indexOf(floor);
  const b = QUALITY_TIERS.indexOf(ceiling);
  if (a < 0 || b < 0) return ["high", "lossless"];
  const lo = Math.min(a, b);
  const hi = Math.max(a, b);
  return QUALITY_TIERS.slice(lo, hi + 1);
};

export const QualityPrefsSection: Component<{ mode: () => Mode }> = (props) => {
  const [prefs] = createResource(props.mode, fetchQualityPrefs);
  const [floor, setFloor] = createSignal("high");
  const [ceiling, setCeiling] = createSignal("lossless");
  const [minRes, setMinRes] = createSignal(0);
  const [maxRes, setMaxRes] = createSignal(0);
  const [codecRank, setCodecRank] = createSignal<string[]>([...CODEC_RANK_DEFAULT]);
  const [featureUpgrade, setFeatureUpgrade] = createSignal(false);
  const [protocol, setProtocol] = createSignal("");
  // 10 is rename.DefaultUndoDepth (internal/rename/undo_store.go). It is only
  // ever a pre-load placeholder — the mount GET always answers with a real
  // value, since the backend substitutes the same default when nothing is
  // stored.
  const [undoDepth, setUndoDepth] = createSignal(10);
  const [dirty, setDirty] = createSignal(false);
  const expandFloor = (floor: string): string[] => {
    const idx = QUALITY_TIERS.indexOf(floor);
    return idx >= 0 ? QUALITY_TIERS.slice(idx) : ["high", "lossless"];
  };
  createEffect(
    on(prefs, (p) => {
      if (p) {
        const nextTiers = p.tiers?.length ? [...p.tiers] : expandFloor(p.tier);
        const nextFloor =
          QUALITY_TIERS.find((t) => nextTiers.includes(t)) ?? p.tier ?? "high";
        const nextCeiling =
          [...QUALITY_TIERS].reverse().find((t) => nextTiers.includes(t)) ??
          p.ceiling ??
          "lossless";
        setFloor(nextFloor);
        setCeiling(nextCeiling);
        setMinRes(p.minResolution ?? 0);
        setMaxRes(p.maxResolution);
        setCodecRank(
          p.codecRank?.length ? [...p.codecRank] : [...CODEC_RANK_DEFAULT],
        );
        setFeatureUpgrade(p.featureUpgrade === true);
        setProtocol(p.protocol);
        setUndoDepth(p.undoDepth);
        setDirty(false);
      }
    }),
  );
  // Claude 2026-08-10: undoDepth's range is mirrored client-side, and BOTH
  //   Save buttons disable while it is out of range.
  // Reason: the backend rejects undoDepth < 1 or > MaxUndoDepth (100) with a
  //   400 for the WHOLE quality-prefs request — so a cleared field (an empty
  //   number input reads as 0) or a typo'd 150 would fail the tier /
  //   maxResolution / protocol save alongside it, for a field the operator may
  //   not even have meant to touch. `min`/`max` on the input do not prevent
  //   this: browsers let out-of-range values be typed, and they are not
  //   enforced for programmatic input at all. Same shape and same rationale as
  //   Advanced.tsx's NumberSetting (which registers the identical `valid`
  //   predicate) — the operator sees the block before clicking rather than an
  //   error after.
  // Troubleshooting: clearing the depth field and clicking Save 400ing the
  //   whole card with "undoDepth must be between 1 and 100".
  // Review if: the backend's MaxUndoDepth changes — this constant must follow.
  const MAX_UNDO_DEPTH = 100;
  // Number.isInteger FIRST, and it is doing two jobs a bare range check cannot.
  // (1) FRACTIONS: a type="number" input hands back "5.5" for typed decimal
  //     input — a step mismatch does not blank the value, only unparseable
  //     input does — and 5.5 sits happily inside 1..100, so the range check
  //     alone would leave Save enabled and PUT a float that Go's *int decode
  //     rejects, 400ing the whole card. That is precisely the failure this
  //     mirror exists to prevent, so the mirror has to model the wire type too,
  //     not just the range. (2) NaN: `NaN < 1` and `NaN > 100` are BOTH false,
  //     so a bare range check is NaN-permissive; Number.isInteger(NaN) is
  //     false, which closes that hole in the same expression.
  // step={1} on the input is the matching native affordance (spinner steps and
  // browser validation UI), NOT the enforcement — this predicate is.
  const undoDepthOutOfRange = () =>
    !Number.isInteger(undoDepth()) ||
    undoDepth() < 1 ||
    undoDepth() > MAX_UNDO_DEPTH;
  const bandOk = () => {
    const a = QUALITY_TIERS.indexOf(floor());
    const b = QUALITY_TIERS.indexOf(ceiling());
    return a >= 0 && b >= 0 && a <= b;
  };
  const resBandOk = () => {
    if (minRes() === 0 || maxRes() === 0) return true;
    return minRes() <= maxRes();
  };
  const moveCodec = (i: number, dir: -1 | 1) => {
    setCodecRank((prev) => {
      const next = [...prev];
      const j = i + dir;
      if (j < 0 || j >= next.length) return prev;
      const a = next[i];
      const b = next[j];
      if (a === undefined || b === undefined) return prev;
      next[i] = b;
      next[j] = a;
      return next;
    });
    setDirty(true);
  };
  const status = useSaveStatus();
  const save = async () => {
    // Defense in depth: both Save buttons are disabled while out of range, so
    // normal use cannot reach this — but a direct save() call must reject
    // rather than PUT, so the section summary never falsely reports "saved".
    if (undoDepthOutOfRange()) {
      const err = new Error(
        `Undoable recent Applies must be a whole number between 1 and ${MAX_UNDO_DEPTH}`,
      );
      status.failed(err);
      throw err;
    }
    if (!bandOk() || !resBandOk()) {
      const err = new Error("Quality and resolution floor must be at or below the ceiling");
      status.failed(err);
      throw err;
    }
    try {
      await putQualityPrefs(props.mode(), {
        tier: floor(),
        ceiling: ceiling(),
        tiers: expandBand(floor(), ceiling()),
        minResolution: minRes(),
        maxResolution: maxRes(),
        protocol: protocol(),
        undoDepth: undoDepth(),
        resolutionFloors: synthesizeFloors(floor()),
        codecRank: codecRank(),
        featureUpgrade: featureUpgrade(),
      });
      setDirty(false);
      status.saved();
    } catch (e) {
      status.failed(e);
      throw e;
    }
  };
  const batched = useSectionSaveItem({
    id: "library-quality",
    label: "quality preferences",
    dirty,
    valid: () => !undoDepthOutOfRange() && bandOk() && resBandOk(),
    save,
  });
  return (
    <Card title={`Search quality preferences (${MODE_LABELS[props.mode()]})`}>
      <DualRangeSlider
        label="Quality"
        options={[...QUALITY_TIERS]}
        optionLabels={QUALITY_TIER_LABELS}
        low={floor()}
        high={ceiling()}
        lowAriaLabel="Quality floor"
        highAriaLabel="Quality ceiling"
        onChange={(lo, hi) => {
          setFloor(lo);
          setCeiling(hi);
          setDirty(true);
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
          setDirty(true);
        }}
      />
      <div class="mb-3">
        <span class={labelClass}>Preferred codecs</span>
        <Muted class="mb-1">
          Among releases that clear the bitrate floor, HDR then audio channels
          then this order.
        </Muted>
        <ol class="mt-1 space-y-1">
          <For each={codecRank()}>
            {(c, i) => (
              <li class="flex items-center gap-2">
                <span class="w-16 text-sm text-fg">{CODEC_LABELS[c] ?? c}</span>
                <button
                  type="button"
                  class="rounded px-2 py-0.5 text-xs text-muted hover:text-fg disabled:opacity-40"
                  aria-label={`Move ${CODEC_LABELS[c] ?? c} up`}
                  disabled={i() === 0}
                  onClick={() => moveCodec(i(), -1)}
                >
                  Up
                </button>
                <button
                  type="button"
                  class="rounded px-2 py-0.5 text-xs text-muted hover:text-fg disabled:opacity-40"
                  aria-label={`Move ${CODEC_LABELS[c] ?? c} down`}
                  disabled={i() === codecRank().length - 1}
                  onClick={() => moveCodec(i(), 1)}
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
            Off by default. When on, sitting SDR or stereo files still enter
            upgrade-watch even if they already meet the bitrate floor.
          </Muted>
        </div>
        <Switch
          checked={featureUpgrade()}
          ariaLabel="Upgrade for HDR / more channels"
          onChange={(next) => {
            setFeatureUpgrade(next);
            setDirty(true);
          }}
        />
      </div>
      <PillSelector
        label="Protocol"
        options={PROTOCOL_OPTIONS}
        optionLabels={PROTOCOL_LABELS}
        selected={protocol()}
        onSelect={(v) => {
          setProtocol(v);
          setDirty(true);
        }}
      />
      {/* A bounded integer, not a small fixed option set, so a plain number
          input rather than a fourth PillSelector — the Usenet sub-tab's port
          field is the shape reused here. It rides the same dirty/save flow as
          the three pills above; nothing about this field saves on its own. */}
      <label class="mb-3 block">
        <span class={labelClass}>Undoable recent Applies</span>
        <input
          type="number"
          min={1}
          max={MAX_UNDO_DEPTH}
          step={1}
          class={`${inputClass} !w-32`}
          aria-label="Undoable recent Applies"
          value={undoDepth()}
          onInput={(e) => {
            // Plain Number(), NOT `|| 0`: a cleared field reads as 0, which is
            // out of range and blocks Save — exactly what should happen. `|| 0`
            // would have collapsed a real 0 into the same value silently.
            setUndoDepth(Number(e.currentTarget.value));
            setDirty(true);
          }}
        />
      </label>
      <div class="mt-3 flex items-center gap-2">
        <Show when={!batched()}>
          <Button
            variant="primary"
            disabled={undoDepthOutOfRange() || !bandOk() || !resBandOk()}
            onClick={() => void save().catch(() => {})}
          >
            Save
          </Button>
        </Show>
        <SaveStatus text={status.status().text} error={status.status().error} />
      </div>
      <Muted class="mt-2">
        The quality slider is the accepted band (floor through ceiling).
        Auto-grab compares each release to that floor’s bitrate at the
        release’s resolution — Medium at 480p is a lower Mbps bar than
        Medium at 1080p. The resolution slider is a hard min/max; a full
        span (480p–2160p) leaves both open. Auto-grab tries accepted tiers
        highest first. Protocol is the Discover popup’s default pick when
        both are available; it still falls back to whichever protocol
        actually has a release. Undoable recent Applies (1–100) is how many
        recent Applies stay undoable before the oldest is pruned — Rename’s
        “Recently Applied” list shows them. Lowering it shrinks that list
        immediately, though the entries it stops showing stay undoable until
        a later Apply actually evicts them.
      </Muted>
    </Card>
  );
};

// ---- Per-mode: naming preset ----------------------------------------------

export const NamingPresetSection: Component<{ mode: () => Mode }> = (props) => {
  const [current] = createResource(props.mode, fetchNamingPreset);
  const [preset, setPreset] = createSignal("jellyfin");
  const [dirty, setDirty] = createSignal(false);
  createEffect(
    on(current, (p) => {
      if (p) {
        setPreset(p);
        setDirty(false);
      }
    }),
  );
  const status = useSaveStatus();
  const save = async () => {
    try {
      await putNamingPreset(props.mode(), preset());
      setDirty(false);
      status.saved();
    } catch (e) {
      status.failed(e);
      throw e;
    }
  };
  const batched = useSectionSaveItem({
    id: "library-naming",
    label: "naming preset",
    dirty,
    save,
  });
  return (
    <Card title={`File/folder naming (${MODE_LABELS[props.mode()]})`}>
      <form onSubmit={(e) => (e.preventDefault(), void save().catch(() => {}))}>
        <label class="block">
          <span class={labelClass}>Naming convention</span>
          <select
            class={`${inputClass} mt-1`}
            value={preset()}
            onChange={(e) => {
              setPreset(e.currentTarget.value);
              setDirty(true);
            }}
          >
            <For each={NAMING_PRESETS}>
              {(p) => <option value={p.value}>{p.label}</option>}
            </For>
          </select>
        </label>
        <div class="mt-3 flex items-center gap-2">
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
        Jellyfin/Emby standard renames into "Title (Year) [tmdbid-N]"
        folders/files. Legacy keeps this project's original convention, so an
        already-renamed library's shape never silently changes after an upgrade.
      </Muted>
    </Card>
  );
};

// ---- Per-mode: kids root path ---------------------------------------------

export const KidsRootPathSection: Component<{ mode: () => Mode }> = (props) => {
  const [current] = createResource(props.mode, fetchKidsRootPath);
  const [path, setPath] = createSignal("");
  const [dirty, setDirty] = createSignal(false);
  createEffect(
    on(current, (p) => {
      if (p !== undefined) {
        setPath(p ?? "");
        setDirty(false);
      }
    }),
  );
  const status = useSaveStatus();
  const [scanLine, setScanLine] = createSignal("");
  let scanTimer: number | undefined;
  const stopScanPoll = () => {
    if (scanTimer !== undefined) {
      window.clearInterval(scanTimer);
      scanTimer = undefined;
    }
  };
  const pollScan = () => {
    stopScanPoll();
    scanTimer = window.setInterval(() => {
      void fetchRenameScanStatus(props.mode()).then((s) => {
        if (s.running) {
          const n = s.total ? `${s.current ?? 0}/${s.total}` : "…";
          setScanLine(`Scanning ${n}${s.name ? ` · ${s.name}` : ""}`);
          return;
        }
        stopScanPoll();
        if (s.phase === "error") {
          setScanLine(s.error || "Scan failed");
          return;
        }
        if (s.phase === "done") {
          setScanLine("Scan finished — identified titles are in Library.");
        }
      });
    }, 1000);
  };
  onCleanup(stopScanPoll);
  const save = async () => {
    try {
      await putKidsRootPath(props.mode(), path());
      setDirty(false);
      status.saved();
      if (path().trim() !== "") {
        setScanLine("Starting scan…");
        pollScan();
      } else {
        setScanLine("");
      }
    } catch (e) {
      status.failed(e);
      throw e;
    }
  };
  const setPathDirty = (p: string) => {
    setPath(p);
    setDirty(true);
  };
  const batched = useSectionSaveItem({
    id: "library-kids",
    label: "kids root path",
    dirty,
    save,
  });
  return (
    <Card title={`Kids classification (${MODE_LABELS[props.mode()]})`}>
      <form onSubmit={(e) => (e.preventDefault(), void save().catch(() => {}))}>
        <label class="block">
          <span class={labelClass}>Kids root folder path</span>
          <FolderPicker
            value={path}
            onChange={setPathDirty}
            ariaLabel="Kids root folder path"
            placeholder={`/path/to/${MODE_LABELS[props.mode()]} (Kids)`}
          />
        </label>
        <div class="mt-3 flex items-center gap-2">
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
      <Show when={scanLine()}>
        <p class="mt-2 text-sm text-fg" aria-live="polite">
          {scanLine()}
        </p>
      </Show>
      <Muted class="mt-2">
        Saving a path scans that folder. Titles with a TMDB id in an .nfo or
        [tmdbid-N] tag appear in Library. Leave blank to turn Kids
        classification off.
      </Muted>
    </Card>
  );
};

// ---- Series only: propose moves for nested shorts --------------------------

export const ProposeNestedMovesSection: Component = () => {
  const [enabled, setEnabled] = createSignal(false);
  const [dirty, setDirty] = createSignal(false);
  const [loadError, setLoadError] = createSignal<Error | null>(null);
  const status = useSaveStatus();

  onMount(() => {
    void fetchProposeNestedMoves("series")
      .then((v) => setEnabled(v))
      .catch((e) => setLoadError(e instanceof Error ? e : new Error(String(e))));
  });

  const save = async () => {
    try {
      await putProposeNestedMoves("series", enabled());
      setDirty(false);
      status.saved();
    } catch (e) {
      status.failed(e);
      throw e;
    }
  };

  const batched = useSectionSaveItem({
    id: "library-propose-nested-moves",
    label: "propose nested short moves",
    dirty,
    save,
  });

  return (
    <Card title="Nested shorts (Series)">
      <label class="mb-3 flex items-center gap-2">
        <input
          type="checkbox"
          aria-label="Propose moves for nested shorts"
          checked={enabled()}
          disabled={loadError() !== null}
          onChange={(e) => {
            setEnabled(e.currentTarget.checked);
            setDirty(true);
          }}
        />
        <span class="text-sm text-fg">Propose moves for nested shorts</span>
      </label>
      <Muted>
        Off by default. Save and Rescan identify shorts in place. Turn this on
        if Organize Scan should also stage a move into the anthology folder.
        Apply is still the only mover.
      </Muted>
      <Show when={loadError()}>
        <ErrorText>
          Couldn't load the nested-shorts setting: {loadError()?.message}.
        </ErrorText>
      </Show>
      <Show when={!batched()}>
        <div class="mt-3 flex items-center gap-2">
          <Button
            variant="primary"
            disabled={!dirty()}
            onClick={() => void save().catch(() => {})}
          >
            Save
          </Button>
          <SaveStatus text={status.status().text} error={status.status().error} />
        </div>
      </Show>
    </Card>
  );
};

// ---- Series only: new-season discovery -------------------------------------

// NEW_SEASON_DISCOVERY_COPY is a required honesty line: it names the one case
// where a season becomes monitored without an operator monitoring it. Rendered
// as a single unbroken text node in its own paragraph — an interleaved element
// child would split the sentence across nodes.
const NEW_SEASON_DISCOVERY_COPY =
  "With new-season discovery on, a brand-new season is monitored automatically — even if every existing season of that series is unmonitored.";

// SeriesNewSeasonDiscoverySection is the Series-only toggle for auto-monitoring
// entirely new seasons. Off by default, and the PUT writes no coupled interval
// (unlike the usenet auto-grab toggle) — this pass has no scheduler of its own,
// it runs inside the existing retry cycle.
//
// loadError is scoped to this card so a failed GET leaves the toggle showing its
// honest default (off) without taking down the Library tab's other panels — same
// shape as Usenet's AutoGrabCard.
export const SeriesNewSeasonDiscoverySection: Component = () => {
  const [enabled, setEnabled] = createSignal(false);
  const [dirty, setDirty] = createSignal(false);
  const [loadError, setLoadError] = createSignal<Error | null>(null);
  const status = useSaveStatus();

  onMount(() => {
    void fetchSeriesNewSeasonDiscovery()
      .then((v) => setEnabled(v))
      .catch((e) => setLoadError(e instanceof Error ? e : new Error(String(e))));
  });

  const save = async () => {
    try {
      await putSeriesNewSeasonDiscovery(enabled());
      setDirty(false);
      status.saved();
    } catch (e) {
      status.failed(e);
      throw e;
    }
  };

  const batched = useSectionSaveItem({
    id: "library-new-season-discovery",
    label: "new-season discovery",
    dirty,
    save,
  });

  return (
    <Card title="New-season discovery (Series)">
      <label class="mb-3 flex items-center gap-2">
        <input
          type="checkbox"
          aria-label="Enable new-season discovery"
          checked={enabled()}
          disabled={loadError() !== null}
          onChange={(e) => {
            setEnabled(e.currentTarget.checked);
            setDirty(true);
          }}
        />
        <span class="text-sm text-fg">Enable new-season discovery</span>
      </label>
      <Muted>{NEW_SEASON_DISCOVERY_COPY}</Muted>
      <Muted class="mt-2">
        A season that already has episodes is always kept in sync regardless of
        this setting — monitoring gates searching, never metadata.
      </Muted>
      <Show when={loadError()}>
        <ErrorText>
          Couldn't load the new-season discovery setting: {loadError()?.message}.
          Discovery is off.
        </ErrorText>
      </Show>
      <Show when={!batched()}>
        <div class="mt-3 flex items-center gap-2">
          <Button
            variant="primary"
            disabled={!dirty()}
            onClick={() => void save().catch(() => {})}
          >
            Save
          </Button>
          <SaveStatus text={status.status().text} error={status.status().error} />
        </div>
      </Show>
      <Show when={batched()}>
        <div class="mt-3">
          <SaveStatus text={status.status().text} error={status.status().error} />
        </div>
      </Show>
    </Card>
  );
};

// ---- Per-mode: metadata source connections ---------------------------------

// LibraryConnectionsSection renders the metadata-source connections that belong
// to exactly one mode, alongside that mode's other Library settings: TMDB under
// Movies, TVDB under Series, and StashDB/FansDB/TPDB under Adult. They were
// rows in the old global Connections table; each placement here was individually
// confirmed rather than derived from a bulk grouping.
//
// The Adult set needs no adult_mode_enabled guard of its own — the Library tab's
// ModeSelector omits the Adult mode entirely when the global switch is off, so
// these rows are unreachable rather than merely hidden.
export const LibraryConnectionsSection: Component<{ mode: () => Mode }> = (
  props,
) => {
  const services = () => LIBRARY_MODE_SERVICES[props.mode()];
  return (
    <Card title={`Metadata sources (${MODE_LABELS[props.mode()]})`}>
      {/* Claude 2026-09-24: card-level SectionSave for metadata source rows.
          Reason: Settings dropped tab-level batched Save; a connection table
            still needs one commit for dirty rows (three-state secret).
          Troubleshooting: each TMDB/TVDB row showing its own Save → wrapper gone.
          Review if: metadata rows become immediate-apply per field. */}
      <SectionSave>
        <ConnectionServiceTable services={services} />
      </SectionSave>
    </Card>
  );
};
