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

// STATUS_BADGE maps an aria2 status to a badge color class.
const STATUS_BADGE: Record<string, string> = {
  active: "bg-accent/20 text-accent",
  waiting: "bg-surface-2 text-muted",
  paused: "bg-warn/20 text-warn",
  complete: "bg-ok/20 text-ok",
  error: "bg-danger/20 text-danger",
  removed: "bg-surface-2 text-muted",
};

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
  onAction: (fn: () => Promise<void>) => void;
  selected: boolean;
  onToggle: () => void;
}> = (props) => {
  const percent = () =>
    props.dl.totalLength > 0
      ? (props.dl.completedLength / props.dl.totalLength) * 100
      : 0;
  const isPaused = () => props.dl.status === "paused";
  const isActive = () => props.dl.status === "active";
  const isDone = () =>
    props.dl.status === "complete" || props.dl.status === "error";
  const isTorrent = () => props.dl.protocol === "torrent";
  const seedLabel = () => (props.dl.seedCount === 1 ? "seed" : "seeds");

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
          class={`shrink-0 rounded-full px-2 py-0.5 text-[11px] font-medium ${STATUS_BADGE[props.dl.status] ?? "bg-surface-2 text-muted"}`}
        >
          {props.dl.status}
        </span>
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
        <Show when={isActive()}>
          <span class="text-fg" aria-label="Download speed">
            <span aria-hidden="true">↓ </span>
            {formatBps(props.dl.downloadSpeed)}
          </span>
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
        setDownloads(list);
        setHasData(true);
        setReconnecting(false);
      } catch {
        /* ignore a malformed frame — the next one should be fine */
      }
    };
    es.onerror = () => setReconnecting(true);
  });

  onCleanup(() => es?.close());

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

  const gids = (): string[] => downloads().map((d) => d.gid);
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
            <For each={downloads()}>
              {(dl) => (
                <DownloadRow
                  dl={dl}
                  onAction={runAction}
                  selected={selection.has(dl.gid)}
                  onToggle={() => selection.toggle(dl.gid)}
                />
              )}
            </For>
          </ul>
        </Show>
      </Show>
    </div>
  );
};
