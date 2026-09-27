// Claude 2026-09-26: Downloads detail popup — pipeline layout (mockup B).
// Reason: the first popup was a label/value list; operator asked for the
//   stage-rail + current-file stack graphic. Card click lives in Downloads.tsx.
// Troubleshooting: stage rail stuck on Downloading during PAR2 — read dl.phase.
// Review if: Usenet engine emits the full RAR/PAR2 file list for the stack.

import { type Component, For, Show, createSignal, onCleanup, onMount } from "solid-js";
import type { Download, TorrentFileDetail } from "@dto";
import {
  pauseDownload,
  reannounceDownload,
  recheckDownload,
  resumeDownload,
  setDownloadFilePriority,
} from "../api/downloads";
import { Button } from "../components/ui";

export const USENET_STAGES = [
  { id: "precheck", label: "Precheck" },
  { id: "waiting", label: "Waiting" },
  { id: "downloading", label: "Downloading" },
  { id: "repairing", label: "Repairing" },
  { id: "unpacking", label: "Unpacking" },
] as const;

export function usenetStageId(dl: Download): string {
  if (dl.status === "complete") return "complete";
  if (dl.phase && USENET_STAGES.some((s) => s.id === dl.phase)) return dl.phase;
  if (dl.status === "paused") return "paused";
  if (dl.status === "error") return dl.phase || "error";
  return "downloading";
}

export function usenetStageIndex(dl: Download): number {
  if (dl.status === "complete") return USENET_STAGES.length;
  const i = USENET_STAGES.findIndex((s) => s.id === usenetStageId(dl));
  return i >= 0 ? i : 2;
}

function formatSize(bytes: number): string {
  if (!bytes) return "0 B";
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  if (bytes < 1024 * 1024 * 1024)
    return `${(bytes / (1024 * 1024)).toFixed(0)} MB`;
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GB`;
}

function formatBps(bps: number): string {
  if (bps <= 0) return "—";
  if (bps < 1024) return `${Math.round(bps)} B/s`;
  if (bps < 1024 * 1024) return `${Math.round(bps / 1024)} KB/s`;
  return `${(bps / (1024 * 1024)).toFixed(1)} MB/s`;
}

function formatAge(sec: number | undefined): string {
  if (sec == null) return "";
  if (sec < 60) return `${sec}s ago`;
  if (sec < 3600) return `${Math.round(sec / 60)}m ago`;
  return `${(sec / 3600).toFixed(1)}h ago`;
}

function tickMarks(done: number, total: number, buckets = 20): number[] {
  if (total <= 0) return Array.from({ length: buckets }, () => 0);
  const filled = Math.round((done / total) * buckets);
  return Array.from({ length: buckets }, (_, i) => (i < filled ? 1 : 0));
}

async function copyText(value: string): Promise<void> {
  await navigator.clipboard.writeText(value);
}

const PILL =
  "shrink-0 rounded-full border border-border bg-surface-2 px-2 py-0.5 text-[11px] font-medium text-fg";

export const DownloadDetailPopup: Component<{
  dl: Download;
  onClose: () => void;
  onAction: (fn: () => Promise<void>) => void;
}> = (props) => {
  onMount(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") props.onClose();
    };
    document.addEventListener("keydown", onKey);
    onCleanup(() => document.removeEventListener("keydown", onKey));
  });

  const isUsenet = () => props.dl.protocol === "usenet";
  const isTorrent = () => props.dl.protocol === "torrent";
  const title = () => props.dl.catalogTitle || props.dl.filename || props.dl.gid;
  const subtitle = () =>
    props.dl.catalogTitle &&
    props.dl.filename &&
    props.dl.catalogTitle !== props.dl.filename
      ? props.dl.filename
      : "";
  const isActive = () =>
    props.dl.status === "active" ||
    props.dl.phase === "precheck" ||
    props.dl.phase === "waiting" ||
    props.dl.phase === "downloading" ||
    props.dl.phase === "repairing" ||
    props.dl.phase === "unpacking";
  const isPaused = () => props.dl.status === "paused";

  return (
    <div
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"
      onClick={props.onClose}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label="Download details"
        class="max-h-[85vh] w-full max-w-3xl overflow-y-auto rounded-xl border border-border bg-surface p-5 shadow-2xl"
        onClick={(e) => e.stopPropagation()}
      >
        <div class="mb-4 flex items-start justify-between gap-3">
          <div class="min-w-0">
            <h3 class="truncate text-xl font-semibold text-fg">{title()}</h3>
            <Show when={subtitle()}>
              <p class="mt-0.5 truncate text-xs text-muted">{subtitle()}</p>
            </Show>
            <div class="mt-2 flex flex-wrap gap-1.5">
              <Show when={props.dl.indexer}>
                <span class={PILL}>{props.dl.indexer}</span>
              </Show>
              <Show when={props.dl.protocol}>
                <span class={PILL}>
                  {props.dl.protocol === "usenet" ? "Usenet" : "Torrent"}
                </span>
              </Show>
              <Show when={props.dl.status === "error"}>
                <span class={`${PILL} border-danger/40 text-danger`}>Failed</span>
              </Show>
            </div>
          </div>
          <Button onClick={props.onClose}>Close</Button>
        </div>

        <Show when={isUsenet()}>
          <UsenetPipeline dl={props.dl} />
        </Show>
        <Show when={isTorrent()}>
          <TorrentPipeline dl={props.dl} onAction={props.onAction} />
        </Show>

        <div class="mt-5 flex flex-wrap gap-2">
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
          <Show when={isTorrent()}>
            <Button onClick={() => props.onAction(() => reannounceDownload(props.dl.gid))}>
              Force reannounce
            </Button>
            <Button onClick={() => props.onAction(() => recheckDownload(props.dl.gid))}>
              Recheck
            </Button>
          </Show>
        </div>
      </div>
    </div>
  );
};

const UsenetPipeline: Component<{ dl: Download }> = (props) => {
  const u = () => props.dl.usenet;
  const doneAll = () => props.dl.status === "complete";
  const failed = () => props.dl.status === "error";
  const currentIdx = () => usenetStageIndex(props.dl);
  const segDone = () => u()?.segmentDone ?? 0;
  const segTotal = () => u()?.segmentTotal ?? 0;
  const currentFileLabel = () => {
    const name = u()?.currentFile;
    if (!name) return "";
    const n = u()?.currentSeg ?? 0;
    const t = u()?.currentSegTotal ?? 0;
    return t > 0 ? `${name} ${n}/${t}` : name;
  };

  return (
    <div class="flex flex-col gap-4">
      <ol
        class="relative flex items-start justify-between gap-1"
        aria-label="Usenet stages"
      >
        <div
          class="absolute top-3 right-[10%] left-[10%] h-0.5 bg-border"
          aria-hidden="true"
        />
        <For each={[...USENET_STAGES]}>
          {(s, i) => {
            const state = () => {
              if (doneAll() || i() < currentIdx()) return "done";
              if (i() === currentIdx()) return failed() ? "failed" : "current";
              return "todo";
            };
            return (
              <li class="flex min-w-0 flex-1 flex-col items-center gap-1">
                <div class="flex w-full items-center">
                  <span
                    class={`relative z-10 mx-auto flex h-6 w-6 items-center justify-center rounded-full border text-[11px] font-semibold ${
                      state() === "done"
                        ? "border-ok bg-ok text-white"
                        : state() === "current"
                          ? "border-accent bg-accent text-accent-fg"
                          : state() === "failed"
                            ? "border-danger bg-danger text-white"
                            : "border-border bg-surface-2 text-muted"
                    }`}
                    aria-current={state() === "current" ? "step" : undefined}
                  >
                    {state() === "done" ? "✓" : i() + 1}
                  </span>
                </div>
                <span
                  class={`text-[10px] font-medium ${
                    state() === "current" ? "text-fg" : "text-muted"
                  }`}
                >
                  {s.label}
                </span>
              </li>
            );
          }}
        </For>
      </ol>

      <Show when={u()?.waitReason}>
        <p class="rounded-md border border-border bg-surface-2 px-3 py-2 text-xs text-fg">
          {u()?.waitReason}
        </p>
      </Show>

      <div class="grid gap-4 sm:grid-cols-2">
        <div>
          <p class="mb-2 text-[11px] font-semibold uppercase tracking-wide text-muted">
            Archive parts
          </p>
          <div class="flex flex-col gap-1.5">
            <Show when={u()?.currentFile} fallback={
              <div class="rounded-md border border-dashed border-border px-3 py-4 text-xs text-muted">
                No current file yet
              </div>
            }>
              <div class="rounded-md border border-accent bg-accent/15 px-3 py-2">
                <div class="flex items-center justify-between gap-2 text-xs font-medium text-fg">
                  <span class="truncate">{currentFileLabel()}</span>
                  <Show when={(u()?.currentSegTotal ?? 0) > 0}>
                    <span>
                      {Math.round(
                        ((u()?.currentSeg ?? 0) / (u()?.currentSegTotal ?? 1)) *
                          100,
                      )}
                      %
                    </span>
                  </Show>
                </div>
                <div class="mt-1.5 h-1.5 overflow-hidden rounded-full bg-surface">
                  <div
                    class="h-full bg-accent"
                    style={{
                      width: `${Math.min(
                        100,
                        ((u()?.currentSeg ?? 0) / (u()?.currentSegTotal || 1)) *
                          100,
                      )}%`,
                    }}
                  />
                </div>
              </div>
            </Show>
            <Show when={u()?.repairFile}>
              <div class="rounded-md border border-status-blue/30 bg-status-blue/10 px-3 py-2 text-xs text-fg">
                PAR2 {u()?.repairFile}
              </div>
            </Show>
          </div>
        </div>

        <div>
          <p class="mb-2 text-[11px] font-semibold uppercase tracking-wide text-muted">
            Article / segment progress
          </p>
          <p class="text-3xl font-semibold tabular-nums text-fg">
            {segTotal() > 0 ? (
              <>
                {segDone()} <span class="text-lg font-medium text-muted">of</span>{" "}
                {segTotal()}
              </>
            ) : (
              <span class="text-lg text-muted">No segment counts yet</span>
            )}
          </p>
          <Show when={segTotal() > 0}>
            <p class="mt-1 text-xs text-muted">{`${segDone()} / ${segTotal()}`}</p>
            <div class="mt-2 h-2 overflow-hidden rounded-full bg-surface-2">
              <div
                class="h-full bg-accent"
                style={{
                  width: `${Math.min(100, (segDone() / segTotal()) * 100)}%`,
                }}
              />
            </div>
            <div class="mt-2 flex gap-0.5" aria-hidden="true">
              <For each={tickMarks(segDone(), segTotal())}>
                {(on) => (
                  <span
                    class={`h-6 flex-1 rounded-sm ${on ? "bg-accent" : "bg-surface-2"}`}
                  />
                )}
              </For>
            </div>
          </Show>
        </div>
      </div>

      <div class="flex flex-wrap gap-x-4 gap-y-1 rounded-md border border-border bg-bg px-3 py-2 text-xs text-fg">
        <span>↓ {formatBps(props.dl.downloadSpeed)}</span>
        <Show when={(u()?.maxConns ?? 0) > 0}>
          <span>
            {u()?.activeConns ?? 0} / {u()?.maxConns} connections
          </span>
        </Show>
        <Show when={u()?.sidecarAgeSec != null}>
          <span>sidecar {formatAge(u()?.sidecarAgeSec)}</span>
        </Show>
        <Show when={u()?.stagingPath}>
          <span class="min-w-0 truncate" title={u()?.stagingPath}>
            {formatSize(u()?.bytesOnDisk ?? 0)} on disk
          </span>
        </Show>
        <Show when={(u()?.statTotal ?? 0) > 0}>
          <span>
            STAT {u()?.statDone ?? 0} / {u()?.statTotal}
          </span>
        </Show>
      </div>

      <Show when={props.dl.errorMessage || u()?.failingSegment}>
        <div class="rounded-md border border-danger/40 bg-danger/10 px-3 py-2 text-xs text-danger">
          <Show when={u()?.failingSegment}>
            <div>{u()?.failingSegment}</div>
          </Show>
          <Show when={props.dl.errorMessage}>
            <div>{props.dl.errorMessage}</div>
          </Show>
        </div>
      </Show>

      <Show when={(u()?.triedReleases?.length ?? 0) > 0}>
        <div>
          <p class="mb-2 text-[11px] font-semibold uppercase tracking-wide text-muted">
            Park history
          </p>
          <ol class="flex flex-col gap-2 border-l-2 border-border pl-3">
            <For each={u()?.triedReleases}>
              {(item) => (
                <li class="text-xs text-fg">
                  {item.label}
                  <Show when={item.reason}>
                    <span class="text-muted"> — {item.reason}</span>
                  </Show>
                </li>
              )}
            </For>
          </ol>
        </div>
      </Show>
    </div>
  );
};

const TORRENT_STAGES = [
  { id: "waiting", label: "Queued" },
  { id: "downloading", label: "Downloading" },
  { id: "complete", label: "Seeding" },
] as const;

function torrentStageId(dl: Download): string {
  if (dl.status === "paused") return "paused";
  if (dl.status === "error") return "error";
  if (dl.status === "waiting") return "waiting";
  if (dl.status === "complete") return "complete";
  return "downloading";
}

const TorrentPipeline: Component<{
  dl: Download;
  onAction: (fn: () => Promise<void>) => void;
}> = (props) => {
  const t = () => props.dl.torrent;
  const [copied, setCopied] = createSignal("");
  const stage = () => torrentStageId(props.dl);
  const currentIdx = () => {
    if (stage() === "complete") return TORRENT_STAGES.length;
    const i = TORRENT_STAGES.findIndex((s) => s.id === stage());
    return i >= 0 ? i : 1;
  };
  const copy = (kind: string, value: string) => {
    void copyText(value).then(() => {
      setCopied(kind);
      setTimeout(() => setCopied(""), 1500);
    });
  };
  const pct = () =>
    props.dl.totalLength > 0
      ? Math.round((props.dl.completedLength / props.dl.totalLength) * 100)
      : 0;

  return (
    <div class="flex flex-col gap-4">
      <ol class="flex items-start justify-between gap-1" aria-label="Torrent stages">
        <For each={[...TORRENT_STAGES]}>
          {(s, i) => {
            const state = () => {
              if (props.dl.status === "complete" || i() < currentIdx()) return "done";
              if (i() === currentIdx())
                return props.dl.status === "error" ? "failed" : "current";
              return "todo";
            };
            return (
              <li class="flex min-w-0 flex-1 flex-col items-center gap-1">
                <span
                  class={`flex h-6 w-6 items-center justify-center rounded-full border text-[11px] font-semibold ${
                    state() === "done"
                      ? "border-ok bg-ok text-white"
                      : state() === "current"
                        ? "border-accent bg-accent text-accent-fg"
                        : state() === "failed"
                          ? "border-danger bg-danger text-white"
                          : "border-border bg-surface-2 text-muted"
                  }`}
                >
                  {state() === "done" ? "✓" : i() + 1}
                </span>
                <span class="text-[10px] font-medium text-muted">{s.label}</span>
              </li>
            );
          }}
        </For>
      </ol>

      <div class="grid gap-4 sm:grid-cols-2">
        <div>
          <p class="mb-2 text-[11px] font-semibold uppercase tracking-wide text-muted">
            Files
          </p>
          <ul class="flex flex-col gap-1.5">
            <For each={t()?.files ?? []}>
              {(file) => (
                <TorrentFileRow
                  gid={props.dl.gid}
                  file={file}
                  onAction={props.onAction}
                />
              )}
            </For>
          </ul>
        </div>
        <div>
          <p class="text-3xl font-semibold tabular-nums text-fg">{pct()}%</p>
          <p class="text-xs text-muted">
            {formatSize(props.dl.completedLength)} / {formatSize(props.dl.totalLength)}
          </p>
          <div class="mt-2 h-2 overflow-hidden rounded-full bg-surface-2">
            <div class="h-full bg-accent" style={{ width: `${pct()}%` }} />
          </div>
          <Show when={t()?.pieceHeatmap}>
            <div
              class="mt-3 flex flex-wrap gap-0.5"
              aria-label="Piece heatmap"
            >
              <For each={[...(t()?.pieceHeatmap ?? "")]}>
                {(ch) => {
                  const n = Number(ch);
                  const tone =
                    n >= 8 ? "bg-ok" : n >= 4 ? "bg-accent" : "bg-surface-2";
                  return <span class={`h-2.5 w-2.5 rounded-sm ${tone}`} />;
                }}
              </For>
            </div>
          </Show>
          <Show when={(t()?.piecesTotal ?? 0) > 0}>
            <p class="mt-1 text-xs text-muted">
              {t()?.piecesHave ?? 0} / {t()?.piecesTotal} pieces
            </p>
          </Show>
        </div>
      </div>

      <div class="flex flex-wrap gap-x-4 gap-y-1 rounded-md border border-border bg-bg px-3 py-2 text-xs text-fg">
        <span>↓ {formatBps(props.dl.downloadSpeed)}</span>
        <span>↑ {props.dl.uploadSpeed <= 0 ? "0 KB/s" : formatBps(props.dl.uploadSpeed)}</span>
        <span>
          {props.dl.seedCount} seeders · {t()?.peerCount ?? 0} peers
          <Show when={t()?.availability != null}>
            {` · ${Math.round((t()?.availability ?? 0) * 100)}% available`}
          </Show>
        </span>
        <span>
          ratio {(t()?.ratio ?? 0).toFixed(2)}
          <Show when={(t()?.seedRatioGoal ?? 0) > 0}>
            {` / ${t()?.seedRatioGoal}`}
          </Show>
        </span>
        <Show when={t()?.savePath}>
          <span>{formatSize(t()?.bytesOnDisk ?? 0)} on disk</span>
        </Show>
      </div>

      <div class="flex flex-wrap gap-2">
        <Show when={t()?.infoHash}>
          <Button
            class="!px-2 !py-1 !text-xs"
            onClick={() => copy("hash", t()?.infoHash ?? "")}
          >
            {copied() === "hash" ? "Copied hash" : "Copy infohash"}
          </Button>
        </Show>
        <Show when={t()?.magnet}>
          <Button
            class="!px-2 !py-1 !text-xs"
            onClick={() => copy("magnet", t()?.magnet ?? "")}
          >
            {copied() === "magnet" ? "Copied magnet" : "Copy magnet"}
          </Button>
        </Show>
      </div>

      <Show when={(t()?.trackers?.length ?? 0) > 0}>
        <ul class="text-xs text-fg">
          <For each={t()?.trackers}>
            {(tr) => (
              <li class="flex items-center gap-2 truncate">
                <span
                  class={`h-2 w-2 shrink-0 rounded-full ${
                    tr.status === "working" ? "bg-ok" : "bg-warn"
                  }`}
                />
                <span class="text-muted">{tr.status}</span> {tr.url}
              </li>
            )}
          </For>
        </ul>
      </Show>
    </div>
  );
};

const TorrentFileRow: Component<{
  gid: string;
  file: TorrentFileDetail;
  onAction: (fn: () => Promise<void>) => void;
}> = (props) => {
  const pct = () =>
    props.file.length > 0
      ? Math.round((props.file.completed / props.file.length) * 100)
      : 0;
  return (
    <li class="rounded-md border border-border px-3 py-2">
      <div class="flex items-center gap-2 text-xs">
        <span class="min-w-0 flex-1 truncate text-fg" title={props.file.path}>
          {props.file.path}
        </span>
        <span class="shrink-0 text-muted">
          {pct()}% · {formatSize(props.file.length)}
        </span>
        <select
          class="rounded border border-border bg-bg px-1 py-0.5 text-xs text-fg"
          aria-label={`Priority for ${props.file.path}`}
          value={props.file.priority || "normal"}
          onChange={(e) => {
            const priority = e.currentTarget.value;
            props.onAction(() =>
              setDownloadFilePriority(props.gid, props.file.path, priority),
            );
          }}
        >
          <option value="skip">Skip</option>
          <option value="normal">Normal</option>
          <option value="high">High</option>
        </select>
      </div>
      <div class="mt-1.5 h-1.5 overflow-hidden rounded-full bg-surface-2">
        <div class="h-full bg-accent" style={{ width: `${pct()}%` }} />
      </div>
    </li>
  );
};
