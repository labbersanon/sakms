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
  MAX_RESOLUTIONS,
  NAMING_PRESETS,
  QUALITY_TIERS,
  fetchKidsRootPath,
  fetchLibraryRootFolder,
  fetchNamingPreset,
  fetchQualityPrefs,
  putKidsRootPath,
  putLibraryRootFolder,
  putNamingPreset,
  putQualityPrefs,
  testLibraryRootFolder,
} from "../../api/settings";
import {
  Button,
  ErrorText,
  Muted,
  PillSelector,
  inputClass,
  labelClass,
} from "../../components/ui";
import { FolderPicker } from "../../components/FolderPicker";
import { ConnectionServiceTable } from "./ConnectionRow";
import {
  Card,
  MODE_LABELS,
  SaveStatus,
  useSaveStatus,
  useSectionSaveItem,
} from "./shared";

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
  const save = async () => {
    try {
      await putLibraryRootFolder(props.mode(), path());
      setDirty(false);
      status.saved();
    } catch (e) {
      status.failed(e);
      throw e;
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

// QUALITY_TIER_LABELS/RESOLUTION_LABELS/PROTOCOL_OPTIONS/PROTOCOL_LABELS give
// PillSelector its display text — QUALITY_TIERS/MAX_RESOLUTIONS themselves
// (api/settings.ts) are the wire values (lowercase tier strings, numeric
// resolutions with 0 for "no cap"), reused as-is for the request body so
// there's exactly one source of truth for what's valid.
const QUALITY_TIER_LABELS: Record<string, string> = {
  low: "Low",
  medium: "Medium",
  high: "High",
  lossless: "Lossless",
};
const RESOLUTION_OPTIONS = MAX_RESOLUTIONS.map(String);
const RESOLUTION_LABELS: Record<string, string> = Object.fromEntries(
  MAX_RESOLUTIONS.map((r) => [String(r), r === 0 ? "No cap" : `${r}p`]),
);
const PROTOCOL_OPTIONS = ["", "usenet", "torrent"];
const PROTOCOL_LABELS: Record<string, string> = {
  "": "No preference",
  usenet: "Usenet",
  torrent: "Torrent",
};

export const QualityPrefsSection: Component<{ mode: () => Mode }> = (props) => {
  const [prefs] = createResource(props.mode, fetchQualityPrefs);
  const [tiers, setTiers] = createSignal<string[]>(["high", "lossless"]);
  const [maxRes, setMaxRes] = createSignal(0);
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
        setTiers(p.tiers?.length ? [...p.tiers] : expandFloor(p.tier));
        setMaxRes(p.maxResolution);
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
  const noTiers = () => tiers().length === 0;
  const lowestTier = () =>
    QUALITY_TIERS.find((t) => tiers().includes(t)) ?? "high";
  const toggleTier = (t: string, on: boolean) => {
    setTiers((prev) => {
      if (on) return QUALITY_TIERS.filter((x) => x === t || prev.includes(x));
      const next = prev.filter((x) => x !== t);
      return next.length ? next : prev;
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
    if (noTiers()) {
      const err = new Error("Select at least one quality tier");
      status.failed(err);
      throw err;
    }
    try {
      await putQualityPrefs(props.mode(), {
        tier: lowestTier(),
        tiers: tiers(),
        maxResolution: maxRes(),
        protocol: protocol(),
        undoDepth: undoDepth(),
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
    valid: () => !undoDepthOutOfRange() && !noTiers(),
    save,
  });
  return (
    <Card title={`Search quality preferences (${MODE_LABELS[props.mode()]})`}>
      <div class="mb-3">
        <span class={labelClass}>Accepted tiers (bitrate/codec)</span>
        <div class="mt-1 flex flex-wrap gap-4">
          <For each={QUALITY_TIERS}>
            {(t) => (
              <label class="flex items-center gap-2">
                <input
                  type="checkbox"
                  aria-label={`Quality tier ${QUALITY_TIER_LABELS[t]}`}
                  checked={tiers().includes(t)}
                  onChange={(e) => toggleTier(t, e.currentTarget.checked)}
                />
                <span class="text-sm text-fg">{QUALITY_TIER_LABELS[t]}</span>
              </label>
            )}
          </For>
        </div>
      </div>
      <PillSelector
        label="Maximum resolution"
        options={RESOLUTION_OPTIONS}
        optionLabels={RESOLUTION_LABELS}
        selected={String(maxRes())}
        onSelect={(r) => {
          setMaxRes(Number(r));
          setDirty(true);
        }}
      />
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
            disabled={undoDepthOutOfRange() || noTiers()}
            onClick={() => void save().catch(() => {})}
          >
            Save
          </Button>
        </Show>
        <SaveStatus text={status.status().text} error={status.status().error} />
      </div>
      <Muted class="mt-2">
        Accepted tiers are a floor you can narrow: a stored High starts as
        High+Lossless, and unchecking Lossless means auto-grab will not
        require a remux. Auto-grab tries the remaining tiers highest first
        and grabs the best qualifying release. Tier never changes which
        resolution is preferred. Maximum resolution softly prefers
        at-or-below-cap results, falling back to whatever's available if
        nothing meets it. Protocol is the Discover popup's default pick when
        both are available; it still falls back to whichever protocol
        actually has a release. Undoable recent Applies (1–100) is how many
        recent Applies stay undoable before the oldest is pruned — Rename's
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
  const save = async () => {
    try {
      await putKidsRootPath(props.mode(), path());
      setDirty(false);
      status.saved();
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
      <Muted class="mt-2">
        Leave blank to turn Kids classification off. Applies to both newly-found
        files and already-tracked items whose classification has drifted.
      </Muted>
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
      {/* No SectionSave of its own: these rows join the Library tab's single
          one, alongside root folder / quality prefs / naming / kids. */}
      <ConnectionServiceTable services={services} />
    </Card>
  );
};
