// Downloads — the unified downloader's live queue (active + waiting + recent
// stopped), fed by the SSE stream at GET /api/downloads/stream (same pattern as
// Dashboard's sysinfo stream). Each event is a JSON array of the current
// downloads. Per item: filename, a progress bar, speed, a status badge, and
// pause/resume/cancel actions. This is NOT a mode-scoped screen (the download
// engine is global, one queue for the whole app), so it registers no mode tabs.
//
// The screen reflects aria2's own queue directly — separate from Calendar's
// History view, which tracks the grab records SAK created. A completed download
// here auto-imports server-side (the downloader's onComplete callback); this
// screen just shows the engine's live state.

import {
  type Component,
  For,
  Show,
  createEffect,
  createMemo,
  createResource,
  createSignal,
  onCleanup,
  onMount,
} from "solid-js";
import type { Download } from "@dto";
import {
  bulkCancelDownloads,
  cancelDownload,
  fetchPauseState,
  pauseDownload,
  resumeDownload,
  setPauseState,
} from "../api/downloads";
import { Button, ErrorText, Muted } from "../components/ui";
import { useBulkSelection } from "./workflowHooks";
import { matchesQueueSearch, QueueSearchField } from "./queueSearch";
import {
  type EtaTracker,
  formatElapsed,
  formatEtaCountdown,
  resolveEtaView,
  updateEtaTrackers,
} from "./downloadEta";

// Claude 2026-09-20: lock row order for this browser session.
// Reason: even with a stable server sort, a reshuffled SSE frame (reconnect,
//   mid-deploy) must not jump rows under the operator's cursor.
// Troubleshooting: Downloads list jumping; server sorts by addedAt ASC.
// Review if: operator drag-reorder is added (then this becomes the source of truth).
function stabilizeQueueOrder(prev: Download[], next: Download[]): Download[] {
  if (prev.length === 0) return next;
  const leftover = new Map(next.map((d) => [d.gid, d]));
  const kept: Download[] = [];
  for (const d of prev) {
    const fresh = leftover.get(d.gid);
    if (fresh === undefined) continue;
    kept.push(fresh);
    leftover.delete(d.gid);
  }
  // New GIDs keep the server's chronological order among themselves.
  return kept.concat([...leftover.values()]);
}

// formatBps renders a bytes/sec value: <1024 → "X B/s", <1MB → "X KB/s",
// else "X.X MB/s" (same scale as Dashboard's formatBps).
function formatBps(bps: number): string {
  if (bps <= 0) return "—";
  if (bps < 1024) return `${Math.round(bps)} B/s`;
  if (bps < 1024 * 1024) return `${Math.round(bps / 1024)} KB/s`;
  return `${(bps / (1024 * 1024)).toFixed(1)} MB/s`;
}

// formatUpBps mirrors formatBps but renders a true zero as "0 KB/s" rather
// than "—". Upload speed is always shown on torrent rows, so 0 has to read as
// a real measured rate, not as "no data" — which is exactly what formatBps's
// "—" means for download speed, and why that function is left alone.
function formatUpBps(bps: number): string {
  return bps <= 0 ? "0 KB/s" : formatBps(bps);
}

// formatSize renders a byte count as MB/GB for the progress label.
function formatSize(bytes: number): string {
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(0)} MB`;
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GB`;
}

const TAG_PILL = "shrink-0 rounded-full px-2 py-0.5 text-[11px] font-medium";

const PHASE_BADGE: Record<string, string> = {
  // Claude 2026-09-21: neon text + thin black outline (pill-neon-outline).
  // Reason: plain neon on soft chips washed out; outline + bold for contrast.
  // Troubleshooting: Downloads phase pill text still hard to read.
  // Review if: chips switch to solid dark backgrounds instead of outlined neon.
  stalled: "bg-warn/20 text-neon-red pill-neon-outline",
  repairing: "bg-accent/20 text-neon-blue pill-neon-outline",
  unpacking: "bg-accent/20 text-neon-blue pill-neon-outline",
  downloading: "bg-accent/20 text-neon-blue pill-neon-outline",
  queued: "bg-surface-2 text-muted",
  paused: "bg-warn/20 text-neon-red pill-neon-outline",
  complete: "bg-ok/20 text-ok",
  failed: "bg-danger/20 text-danger",
  removed: "bg-surface-2 text-muted",
};

const PHASE_LABEL: Record<string, string> = {
  stalled: "Stalled",
  repairing: "Repairing",
  unpacking: "Unpacking",
  downloading: "Downloading",
  queued: "Queued",
  paused: "Paused",
  complete: "Complete",
  failed: "Failed",
  removed: "Removed",
};

const STALL_MS = 5 * 60 * 1000;

function isActiveish(d: Download): boolean {
  return (
    d.status === "active" ||
    d.phase === "downloading" ||
    d.phase === "repairing" ||
    d.phase === "unpacking"
  );
}

// Stalled only overrides the downloading label — PAR2/unpack often have
// downloadSpeed 0 by design and must stay Repairing/Unpacking.
function isStallCandidate(d: Download): boolean {
  if (d.phase === "repairing" || d.phase === "unpacking") return false;
  return d.status === "active" || d.phase === "downloading";
}

function phaseKind(d: Download, stalled: boolean): string {
  if (stalled && isStallCandidate(d)) return "stalled";
  if (d.phase === "repairing") return "repairing";
  if (d.phase === "unpacking") return "unpacking";
  switch (d.status) {
    case "waiting":
      return "queued";
    case "paused":
      return "paused";
    case "complete":
      return "complete";
    case "error":
      return "failed";
    case "removed":
      return "removed";
    default:
      return "downloading";
  }
}

function phaseLabel(d: Download, stalled: boolean): string {
  return PHASE_LABEL[phaseKind(d, stalled)] ?? "Downloading";
}

function protocolLabel(protocol: string): string {
  if (protocol === "usenet") return "Usenet";
  if (protocol === "torrent") return "Torrent";
  return protocol;
}

// Claude 2026-09-21: client-side Stalled overlay (5 min no progress).
// Reason: stall on completedLength advances, not speed alone — Usenet List()
//   after fanout briefly zeroed speed; sparse segments can report 0 B/s between
//   real byte advances.
// Troubleshooting: false Stalled while completedLength climbs; row at 0 B/s still Downloading.
// Review if: engine emits a first-class stalled flag.
function updateStalledTrackers(
  lastProgressAt: Map<string, number>,
  lastCompleted: Map<string, number>,
  list: Download[],
  now: number,
): Set<string> {
  const live = new Set(list.map((d) => d.gid));
  for (const gid of [...lastProgressAt.keys()]) {
    if (!live.has(gid)) {
      lastProgressAt.delete(gid);
      lastCompleted.delete(gid);
    }
  }
  const stalled = new Set<string>();
  for (const d of list) {
    if (!isActiveish(d)) {
      lastProgressAt.delete(d.gid);
      lastCompleted.delete(d.gid);
      continue;
    }
    const prevCompleted = lastCompleted.get(d.gid);
    const advanced =
      prevCompleted === undefined ||
      d.completedLength > prevCompleted ||
      d.downloadSpeed > 0;
    if (advanced || !lastProgressAt.has(d.gid)) {
      lastProgressAt.set(d.gid, now);
    }
    lastCompleted.set(d.gid, d.completedLength);
    const since = lastProgressAt.get(d.gid) ?? now;
    if (isStallCandidate(d) && now - since >= STALL_MS) {
      stalled.add(d.gid);
    }
  }
  return stalled;
}

// resumeModeLabel is the Downloads badge text for Download.resumeMode.
// Wire values stay "resumed" | "full" | "forced-full" | "disabled".
function resumeModeLabel(mode: string): string {
  switch (mode) {
    case "resumed":
      return "resumed";
    case "forced-full":
      return "forced full";
    case "disabled":
      return "no resume";
    default:
      return "full";
  }
}

const ProgressBar: Component<{ percent: number }> = (props) => {
  const clamped = () => Math.max(0, Math.min(100, props.percent));
  return (
    <div class="h-2 w-full overflow-hidden rounded-full bg-surface-2">
      <div
        class="h-full rounded-full bg-accent transition-[width] duration-500"
        style={{ width: `${clamped()}%` }}
      />
    </div>
  );
};

const DownloadRow: Component<{
  dl: Download;
  stalled: boolean;
  etaTracker: EtaTracker | undefined;
  now: number;
  onAction: (fn: () => Promise<void>) => void;
  selected: boolean;
  onToggle: () => void;
}> = (props) => {
  const isPostprocess = () =>
    props.dl.protocol === "usenet" &&
    (props.dl.phase === "repairing" || props.dl.phase === "unpacking");
  // Claude 2026-09-21: PAR2/unpack percent from work units, not download bytes.
  // Reason: completedLength is already 100% when postprocess starts; speed is 0.
  // Troubleshooting: bar sitting at 100% while Repairing/Unpacking with no NN%.
  // Review if: torrents grow a comparable postprocess phase.
  const percent = () => {
    if (isPostprocess()) {
      const total = props.dl.phaseTotal ?? 0;
      const done = props.dl.phaseDone ?? 0;
      return total > 0 ? (done / total) * 100 : 0;
    }
    return props.dl.totalLength > 0
      ? (props.dl.completedLength / props.dl.totalLength) * 100
      : 0;
  };
  const isPaused = () => props.dl.status === "paused";
  const isActive = () => props.dl.status === "active";
  const isDone = () =>
    props.dl.status === "complete" || props.dl.status === "error";
  const isTorrent = () => props.dl.protocol === "torrent";
  const seedLabel = () => (props.dl.seedCount === 1 ? "seed" : "seeds");
  const etaView = () =>
    resolveEtaView(props.dl, props.etaTracker, props.now, props.stalled);
  const etaLabel = () => {
    const v = etaView();
    if (v.kind === "calculating") return "calculating…";
    if (v.kind === "dash") return "—";
    if (v.kind === "eta") return formatEtaCountdown(v.remainingSec);
    return "";
  };
  const elapsedLabel = () => {
    const v = etaView();
    return v.kind === "hidden" ? "" : formatElapsed(v.elapsedMs);
  };

  // Cancelling now also deletes the download's files server-side (the backend
  // DELETE changed), so the confirm makes that explicit before firing.
  const cancelWithConfirm = (): void => {
    const name = props.dl.filename || props.dl.gid;
    const verb = isDone() ? "Remove" : "Cancel";
    if (!confirm(`${verb} “${name}” and delete its downloaded files from disk?`))
      return;
    props.onAction(() => cancelDownload(props.dl.gid));
  };

  return (
    <li class="flex flex-col gap-2 rounded-md border border-border bg-surface p-3">
      <div class="flex items-center gap-3">
        <input
          type="checkbox"
          aria-label={`Select ${props.dl.filename || props.dl.gid}`}
          checked={props.selected}
          onChange={props.onToggle}
        />
        <div class="min-w-0 flex-1">
          <div class="truncate text-sm text-fg" title={props.dl.filename}>
            {props.dl.filename || props.dl.gid}
          </div>
          <Show when={props.dl.errorMessage}>
            <div class="truncate text-xs text-danger">{props.dl.errorMessage}</div>
          </Show>
        </div>
        <span
          class={`${TAG_PILL} ${PHASE_BADGE[phaseKind(props.dl, props.stalled)] ?? "bg-surface-2 text-muted"}`}
          aria-label="Download phase"
        >
          {phaseLabel(props.dl, props.stalled)}
        </span>
        <Show when={props.dl.protocol}>
          <span
            class={`${TAG_PILL} bg-surface-2 text-muted`}
            aria-label="Protocol"
          >
            {protocolLabel(props.dl.protocol)}
          </span>
        </Show>
        <Show when={props.dl.protocol === "usenet" && props.dl.resumeMode}>
          <span
            class={`${TAG_PILL} bg-surface-2 text-muted`}
            title="How this Usenet job started after the last (re)launch"
            aria-label={`Resume mode ${props.dl.resumeMode}`}
          >
            {resumeModeLabel(props.dl.resumeMode ?? "")}
          </span>
        </Show>
      </div>

      <ProgressBar percent={percent()} />

      {/* Claude 2026-08-04: upload speed and seed count always show on
          torrent rows (including seeding/complete), not gated to isActive()
          like download speed is.
          Reason: a seeding torrent's status is "complete", so gating either
          metric to isActive() would hide both on the one row where they
          matter most — the row where upload speed is actually non-zero. This
          is also why formatUpBps exists instead of reusing formatBps: a true
          zero upload rate must read as "0 KB/s" (a real measurement), not as
          formatBps's "—" ("no data"). Usenet has no seeder/upload concept, so
          isTorrent() hides both fields entirely rather than rendering a zero.
          Review if: usenet ever gains an upload concept, or the always-show
          rule is revisited. */}
      <div class="flex items-center gap-3 text-xs text-muted">
        <span>
          {formatSize(props.dl.completedLength)} / {formatSize(props.dl.totalLength)}
        </span>
        {/* Claude 2026-09-21: lifecycle ETA countdown + overall elapsed.
            Reason: remaining time is download-speed EMA + Usenet repair/unpack
              priors (B+C); phase-elapsed beside NN% was not a remaining estimate.
            Troubleshooting: Downloads showed speed or NN% with no time left.
            Review if: the engine emits remaining-time estimates of its own. */}
        <Show when={isActive()}>
          <Show
            when={isPostprocess()}
            fallback={
              <span class="text-fg" aria-label="Download speed">
                <span aria-hidden="true">↓ </span>
                {formatBps(props.dl.downloadSpeed)}
              </span>
            }
          >
            <span class="text-fg" aria-label="Postprocess progress">
              {`${Math.round(percent())}%`}
            </span>
          </Show>
          <Show when={etaView().kind !== "hidden"}>
            <span>
              <span class="text-fg" aria-label="Estimated time remaining">
                {etaLabel()}
              </span>
              <span aria-hidden="true"> · </span>
              <span class="text-[11px] text-muted" aria-label="Elapsed">
                {elapsedLabel()}
              </span>
            </span>
          </Show>
        </Show>
        <Show when={isTorrent()}>
          <span aria-label="Upload speed">
            <span aria-hidden="true">↑ </span>
            {formatUpBps(props.dl.uploadSpeed)}
          </span>
          <span aria-label="Connected seeders">
            {props.dl.seedCount} {seedLabel()}
          </span>
        </Show>
        <div class="ml-auto flex gap-2">
          <Show when={isActive()}>
            <Button onClick={() => props.onAction(() => pauseDownload(props.dl.gid))}>
              Pause
            </Button>
          </Show>
          <Show when={isPaused()}>
            <Button onClick={() => props.onAction(() => resumeDownload(props.dl.gid))}>
              Resume
            </Button>
          </Show>
          <Button onClick={cancelWithConfirm}>
            {isDone() ? "Remove" : "Cancel"}
          </Button>
        </div>
      </div>
    </li>
  );
};

export const Downloads: Component = () => {
  const [downloads, setDownloads] = createSignal<Download[]>([]);
  const lastProgressAt = new Map<string, number>();
  const lastCompleted = new Map<string, number>();
  const etaTrackers = new Map<string, EtaTracker>();
  const [etaVersion, setEtaVersion] = createSignal(0);
  const [nowMs, setNowMs] = createSignal(Date.now());
  const [stalledGids, setStalledGids] = createSignal<ReadonlySet<string>>(
    new Set(),
  );
  const [search, setSearch] = createSignal("");
  const [reconnecting, setReconnecting] = createSignal(false);
  const [actionError, setActionError] = createSignal<string | null>(null);
  // hasData tracks whether at least one stream frame has arrived, so the empty
  // state ("No active downloads") doesn't flash before the first event.
  const [hasData, setHasData] = createSignal(false);
  // Bulk selection keyed on the string gid (not the numeric proposal id the
  // workflow screens use) — the generic useBulkSelection<string> supports it.
  const selection = useBulkSelection<string>();

  // Global pause: a single system-wide toggle, distinct from each row's per-item
  // pause. Seeded once from the server, then driven locally as the operator
  // flips it (the PUT returns the persisted state).
  const [pauseData] = createResource(fetchPauseState);
  const [paused, setPaused] = createSignal(false);
  createEffect(() => {
    const d = pauseData();
    if (d) setPaused(d.paused);
  });

  let es: EventSource | undefined;

  onMount(() => {
    es = new EventSource("/api/downloads/stream");
    es.onmessage = (ev) => {
      try {
        const list = JSON.parse(ev.data) as Download[];
        const now = Date.now();
        setStalledGids(
          updateStalledTrackers(lastProgressAt, lastCompleted, list, now),
        );
        updateEtaTrackers(etaTrackers, list, now);
        setEtaVersion((v) => v + 1);
        setNowMs(now);
        setDownloads((prev) => stabilizeQueueOrder(prev, list));
        setHasData(true);
        setReconnecting(false);
      } catch {
        /* ignore a malformed frame — the next one should be fine */
      }
    };
    es.onerror = () => setReconnecting(true);
    const tick = window.setInterval(() => setNowMs(Date.now()), 1000);
    onCleanup(() => window.clearInterval(tick));
  });

  onCleanup(() => es?.close());

  const visible = createMemo(() =>
    downloads().filter((d) =>
      matchesQueueSearch(
        search(),
        d.filename,
        d.status,
        d.protocol,
        d.errorMessage,
        d.phase,
        phaseLabel(d, stalledGids().has(d.gid)),
        protocolLabel(d.protocol),
      ),
    ),
  );

  // runAction fires a mutating call and surfaces its error; the SSE stream
  // reflects the resulting queue change on the next frame, so there's nothing
  // to optimistically update here.
  const runAction = async (fn: () => Promise<void>) => {
    setActionError(null);
    try {
      await fn();
    } catch (err) {
      setActionError((err as Error).message);
    }
  };

  // Scoped to the search-visible rows: a row the operator cannot see is never
  // part of "Select all" or a bulk cancel.
  const gids = (): string[] => visible().map((d) => d.gid);
  const allSelected = (): boolean => {
    const all = gids();
    return all.length > 0 && all.every((g) => selection.has(g));
  };
  const toggleSelectAll = (): void => {
    if (allSelected()) selection.clear();
    else selection.selectAll(gids());
  };

  // cancelSelected cancels every checked download in one batch call — each also
  // deletes its files server-side, so it goes behind the same confirm. No
  // refetch: the SSE stream reflects the removals on its next frame; only the
  // selection is cleared.
  const cancelSelected = (): void => {
    const chosen = gids().filter((g) => selection.has(g));
    if (chosen.length === 0) return;
    if (
      !confirm(
        `Cancel ${chosen.length} selected download(s) and delete their files from disk?`,
      )
    )
      return;
    void runAction(async () => {
      await bulkCancelDownloads(chosen);
      selection.clear();
    });
  };

  const togglePause = async (): Promise<void> => {
    setActionError(null);
    try {
      const next = await setPauseState(!paused());
      setPaused(next.paused);
    } catch (err) {
      setActionError((err as Error).message);
    }
  };

  return (
    <div>
      <div class="mb-3">
        <QueueSearchField
          id="downloads-search"
          value={search()}
          onInput={setSearch}
          placeholder="Search filename, status…"
        />
      </div>
      {/* Global pause control + banner live OUTSIDE the queue-length gate below
          on purpose: their whole point is blocking NEW grabs, which is exactly
          when the live queue may be empty. */}
      <div class="mb-3 flex items-center gap-3">
        <Button variant={paused() ? "primary" : "secondary"} onClick={() => void togglePause()}>
          {paused() ? "Resume all downloads" : "Pause all downloads"}
        </Button>
        <Show when={selection.size() > 0}>
          <Button variant="primary" onClick={cancelSelected}>
            Cancel Selected ({selection.size()})
          </Button>
        </Show>
      </div>
      <Show when={paused()}>
        <div class="mb-4 rounded-md border border-warn/40 bg-warn/10 px-3 py-2 text-sm text-warn">
          Downloads are globally paused — active downloads are held and new
          grabs are blocked until you resume.
        </div>
      </Show>

      <Show when={reconnecting()}>
        <div class="mb-4 rounded-md border border-warn/40 bg-warn/10 px-3 py-2 text-sm text-warn">
          Connection lost — reconnecting…
        </div>
      </Show>
      <Show when={actionError()}>
        {(msg) => <ErrorText>{msg()}</ErrorText>}
      </Show>

      <Show
        when={hasData()}
        fallback={<Muted>Connecting to the download engine…</Muted>}
      >
        <Show
          when={downloads().length > 0}
          fallback={<Muted>No active downloads</Muted>}
        >
          <Show
            when={visible().length > 0}
            fallback={<Muted>No downloads match this search.</Muted>}
          >
            <label class="mb-2 flex items-center gap-2 text-xs text-muted">
              <input
                type="checkbox"
                aria-label="Select all"
                checked={allSelected()}
                onChange={toggleSelectAll}
              />
              Select all
            </label>
            <ul class="flex flex-col gap-2">
              <For each={visible()}>
                {(dl) => (
                  <DownloadRow
                    dl={dl}
                    stalled={stalledGids().has(dl.gid)}
                    etaTracker={etaVersion() >= 0 ? etaTrackers.get(dl.gid) : undefined}
                    now={nowMs()}
                    onAction={runAction}
                    selected={selection.has(dl.gid)}
                    onToggle={() => selection.toggle(dl.gid)}
                  />
                )}
              </For>
            </ul>
          </Show>
        </Show>
      </Show>
    </div>
  );
};
